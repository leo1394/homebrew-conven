package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const KafkaConsumersEnabledEnv = "SERVICE_KAFKA_CONSUMERS_ENABLED"
const KafkaConsumerGuardMode = "guarded"
const KafkaConsumersDefaultValue = "false"

func InspectKafkaConsumerEvidence(repository string, workdir string) ([]RepositoryConsumerEvidence, error) {
	return InspectKafkaConsumerEvidenceWithGuard(repository, workdir, KafkaConsumersEnabledEnv, "")
}

func InspectKafkaConsumerEvidenceWithGuard(repository string, workdir string, envName string, propertyName string) ([]RepositoryConsumerEvidence, error) {
	root := repository
	if filepath.IsAbs(workdir) {
		root = workdir
	} else if workdir != "" && workdir != "." {
		root = filepath.Join(repository, workdir)
	}
	root = filepath.Clean(root)
	repository = filepath.Clean(repository)
	relativeRoot, err := filepath.Rel(repository, root)
	if err != nil || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("Kafka consumer analysis workdir %q must stay within repository %q", root, repository)
	}
	result := make([]RepositoryConsumerEvidence, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (skippedAnalysisDirectory(entry.Name()) || entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "build" || entry.Name() == "target" || entry.Name() == "__pycache__" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			if path != root {
				nestedRepository, err := pathExists(filepath.Join(path, ".git"))
				if err != nil {
					return err
				}
				if nestedRepository {
					return filepath.SkipDir
				}
			}
			return nil
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".go" && extension != ".java" && extension != ".kt" && extension != ".js" && extension != ".cjs" && extension != ".mjs" && extension != ".ts" && extension != ".py" {
			return nil
		}
		if strings.HasSuffix(entry.Name(), "_test.go") || strings.HasSuffix(entry.Name(), ".test.ts") || strings.HasSuffix(entry.Name(), ".spec.ts") || strings.HasSuffix(entry.Name(), "Test.java") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 2<<20 {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		file := filepath.ToSlash(relative)
		var evidence []RepositoryConsumerEvidence
		switch extension {
		case ".go":
			evidence, err = goKafkaConsumerEvidenceInSourceWithGuard(file, source, envName)
		case ".java", ".kt":
			javaProperty := propertyName
			if javaProperty == "" && envName == KafkaConsumersEnabledEnv {
				javaProperty = "service.kafka.consumers.enabled"
			}
			evidence = javaKafkaConsumerEvidenceInSourceWithGuard(file, string(source), javaProperty)
		case ".js", ".cjs", ".mjs", ".ts":
			evidence = nodeKafkaConsumerEvidenceInSourceWithGuard(file, string(source), envName)
		case ".py":
			evidence = pythonKafkaConsumerEvidenceInSourceWithGuard(file, string(source), envName)
		}
		if err != nil {
			return err
		}
		result = append(result, evidence...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect Kafka consumers in %s: %w", repository, err)
	}
	sort.Slice(result, func(left int, right int) bool {
		return result[left].File < result[right].File
	})
	return result, nil
}

func goKafkaConsumerEvidenceInSource(file string, source []byte) ([]RepositoryConsumerEvidence, error) {
	return goKafkaConsumerEvidenceInSourceWithGuard(file, source, KafkaConsumersEnabledEnv)
}

func goKafkaConsumerEvidenceInSourceWithGuard(file string, source []byte, envName string) ([]RepositoryConsumerEvidence, error) {
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, file, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s for Kafka consumer analysis: %w", file, err)
	}
	result := make([]RepositoryConsumerEvidence, 0)
	record := func(node ast.Node, function *ast.FuncDecl) {
		consumer := false
		switch value := node.(type) {
		case *ast.CallExpr:
			consumer = recognizedGoKafkaConsumerCall(value)
		case *ast.CompositeLit:
			consumer = recognizedGoKafkaConsumerLiteral(value.Type)
		case *ast.ValueSpec:
			consumer = len(value.Values) == 0 && recognizedGoKafkaConsumerLiteral(value.Type)
		}
		if consumer {
			position := set.Position(node.Pos())
			protected := function != nil && goKafkaCallProtected(function, node, envName)
			result = append(result, RepositoryConsumerEvidence{
				Driver:    "kafka",
				File:      fmt.Sprintf("%s:%d", file, position.Line),
				Protected: protected,
			})
		}
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			general, ok := declaration.(*ast.GenDecl)
			if ok && general.Tok == token.VAR {
				ast.Inspect(general, func(node ast.Node) bool {
					record(node, nil)
					return true
				})
			}
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			record(node, function)
			return true
		})
	}
	return result, nil
}

func recognizedGoKafkaConsumerCall(callExpression *ast.CallExpr) bool {
	if function, ok := callExpression.Fun.(*ast.Ident); ok && (function.Name == "NewKafkaReader" || function.Name == "NewKafkaConsumer") { return true }
	if function, ok := callExpression.Fun.(*ast.Ident); ok && function.Name == "new" && len(callExpression.Args) == 1 {
		return recognizedGoKafkaConsumerLiteral(callExpression.Args[0])
	}
	selector, ok := callExpression.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	if selector.Sel.Name == "NewKafkaReader" || selector.Sel.Name == "NewKafkaConsumer" { return true }
	call := qualifier.Name + "." + selector.Sel.Name
	switch call {
	case "kq.NewQueue", "kq.MustNewQueue", "kafka.NewReader", "kafka.NewConsumer", "kafka.NewConsumerGroup", "sarama.NewConsumer", "sarama.NewConsumerFromClient", "sarama.NewConsumerGroup", "sarama.NewConsumerGroupFromClient":
		return true
	case "kgo.NewClient":
		for _, argument := range callExpression.Args {
			found := false
			ast.Inspect(argument, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "ConsumeTopics" || selector.Sel.Name == "ConsumePartitions") {
					found = true
				}
				return !found
			})
			if found {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func recognizedGoKafkaConsumerLiteral(expression ast.Expr) bool {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name == "KafkaReader" || identifier.Name == "KafkaConsumer"
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	// Named wrappers are consumer evidence, not proof of their internal behavior.
	if selector.Sel.Name == "KafkaReader" || selector.Sel.Name == "KafkaConsumer" { return true }
	if selector.Sel.Name != "Reader" { return false }
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == "kafka"
}

func goKafkaCallProtected(function *ast.FuncDecl, call ast.Node, envName string) bool {
	unsafeJump := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		branch, ok := node.(*ast.BranchStmt)
		if ok && branch.Tok == token.GOTO {
			unsafeJump = true
		}
		return !unsafeJump
	})
	if unsafeJump {
		return false
	}
	for _, statement := range function.Body.List {
		guard, ok := statement.(*ast.IfStmt)
		if !ok {
			continue
		}
		if guard.End() < call.Pos() && goKafkaDisabledCondition(guard.Cond, envName) && blockReturns(guard.Body) {
			return true
		}
		if call.Pos() > guard.Body.Pos() && call.End() < guard.Body.End() && goKafkaEnabledCondition(guard.Cond, envName) {
			return true
		}
	}
	return false
}

func goKafkaDisabledCondition(expression ast.Expr, envName string) bool {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return goKafkaDisabledCondition(value.X, envName)
	case *ast.CallExpr:
		selector, ok := value.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		qualifier, qualified := selector.X.(*ast.Ident)
		return qualified && qualifier.Name == "strings" && selector.Sel.Name == "EqualFold" && len(value.Args) == 2 &&
			((goKafkaEnvironmentLookup(value.Args[0], envName) && goStringLiteral(value.Args[1]) == "false") ||
				(goKafkaEnvironmentLookup(value.Args[1], envName) && goStringLiteral(value.Args[0]) == "false"))
	case *ast.BinaryExpr:
		return value.Op == token.EQL && ((goKafkaEnvironmentLookup(value.X, envName) && goStringLiteral(value.Y) == "false") ||
			(goKafkaEnvironmentLookup(value.Y, envName) && goStringLiteral(value.X) == "false"))
	default:
		return false
	}
}

func goKafkaEnabledCondition(expression ast.Expr, envName string) bool {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return goKafkaEnabledCondition(value.X, envName)
	case *ast.UnaryExpr:
		return value.Op == token.NOT && goKafkaDisabledCondition(value.X, envName)
	case *ast.BinaryExpr:
		return value.Op == token.NEQ && ((goKafkaEnvironmentLookup(value.X, envName) && goStringLiteral(value.Y) == "false") ||
			(goKafkaEnvironmentLookup(value.Y, envName) && goStringLiteral(value.X) == "false"))
	default:
		return false
	}
}

func goKafkaEnvironmentLookup(expression ast.Expr, envName string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || envName == "" || len(call.Args) != 1 || goStringLiteral(call.Args[0]) != envName {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Getenv" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == "os"
}

func goStringLiteral(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}

func blockReturns(block *ast.BlockStmt) bool {
	if block == nil || len(block.List) == 0 {
		return false
	}
	_, returned := block.List[len(block.List)-1].(*ast.ReturnStmt)
	return returned
}

func javaKafkaConsumerEvidenceInSource(file string, source string) []RepositoryConsumerEvidence {
	return javaKafkaConsumerEvidenceInSourceWithGuard(file, source, "service.kafka.consumers.enabled")
}

func javaKafkaConsumerEvidenceInSourceWithGuard(file string, source string, propertyName string) []RepositoryConsumerEvidence {
	code := stripCStyleComments(source)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`@KafkaListener\b`),
		regexp.MustCompile(`\bnew\s+(?:[A-Za-z_][A-Za-z0-9_]*\.)*KafkaConsumer\s*[<(]`),
		regexp.MustCompile(`\b(?:ConcurrentMessageListenerContainer|KafkaMessageListenerContainer)\s*[<(]`),
	}
	trusted := trustedSpringKafkaConsumerRanges(code, propertyName)
	evidence := textKafkaEvidence(file, code, patterns, trusted)
	for index := range evidence { evidence[index].GuardProperty = propertyName }
	return evidence
}

func trustedSpringKafkaConsumerRanges(code string, propertyName string) [][2]int {
	annotationPattern := regexp.MustCompile(`(?s)@(?:org\.springframework\.boot\.autoconfigure\.condition\.)?ConditionalOnProperty\s*\((.*?)\)`)
	classPattern := regexp.MustCompile(`(?s)\b(?:public\s+|protected\s+|private\s+|internal\s+|open\s+|final\s+|abstract\s+|data\s+)*(?:class|object|record|enum)\s+[A-Za-z_][A-Za-z0-9_]*[^\{]*\{`)
	ranges := make([][2]int, 0)
	for _, annotation := range annotationPattern.FindAllStringSubmatchIndex(code, -1) {
		if len(annotation) < 4 || !trustedSpringKafkaConsumerCondition(code[annotation[2]:annotation[3]], propertyName) {
			continue
		}
		class := classPattern.FindStringIndex(code[annotation[1]:])
		if class == nil {
			continue
		}
		classStart := annotation[1] + class[0]
		if strings.ContainsAny(code[annotation[1]:classStart], ";{}") {
			continue
		}
		opening := strings.IndexByte(code[classStart:annotation[1]+class[1]], '{')
		if opening < 0 {
			continue
		}
		opening += classStart
		closing := matchingKafkaBrace(code, opening, false)
		if closing > opening {
			cursor := opening
			for _, nested := range classPattern.FindAllStringIndex(code[opening+1:closing], -1) {
				nestedOpening := opening + 1 + nested[1] - 1
				if nestedOpening <= cursor {
					continue
				}
				nestedClosing := matchingKafkaBrace(code, nestedOpening, false)
				if nestedClosing < 0 || nestedClosing > closing {
					cursor = closing
					break
				}
				ranges = append(ranges, [2]int{cursor, nestedOpening})
				cursor = nestedClosing
			}
			if cursor < closing {
				ranges = append(ranges, [2]int{cursor, closing})
			}
		}
	}
	return ranges
}

func trustedSpringKafkaConsumerCondition(arguments string, propertyName string) bool {
	if propertyName == "" {
		return false
	}
	prefix := springConditionalStringValue(arguments, "prefix")
	name := springConditionalStringValue(arguments, "name")
	if name == "" {
		name = springConditionalStringValue(arguments, "value")
	}
	if prefix != "" {
		name = prefix + "." + name
	}
	return name == propertyName &&
		springConditionalStringValue(arguments, "havingValue") == "true" &&
		regexp.MustCompile(`(?s)(?:^|,)\s*matchIfMissing\s*=\s*true\s*(?:,|$)`).MatchString(arguments)
}

func nodeKafkaConsumerEvidenceInSource(file string, source string) []RepositoryConsumerEvidence {
	return nodeKafkaConsumerEvidenceInSourceWithGuard(file, source, KafkaConsumersEnabledEnv)
}

func nodeKafkaConsumerEvidenceInSourceWithGuard(file string, source string, envName string) []RepositoryConsumerEvidence {
	code := stripCStyleComments(source)
	if !regexp.MustCompile(`(?i)(kafkajs|node-rdkafka|@confluentinc/kafka-javascript|from\s+["'][^"']*kafka[^"']*["'])`).MatchString(source) {
		return nil
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.\s*subscribe\s*\(`),
		regexp.MustCompile(`\.\s*run\s*\(`),
		regexp.MustCompile(`\bnew\s+KafkaConsumer\s*\(`),
	}
	return textKafkaEvidence(file, code, patterns, javascriptKafkaGuardRanges(code, envName))
}

func javascriptKafkaGuardRanges(source string, envName string) [][2]int {
	if envName == "" {
		return nil
	}
	pattern := regexp.MustCompile(`if\s*\(\s*(?:process\.env|Bun\.env)\.` + regexp.QuoteMeta(envName) + `\s*(?:!==|!=)\s*["']false["']\s*\)\s*\{`)
	return braceGuardRanges(source, pattern)
}

func pythonKafkaConsumerEvidenceInSource(file string, source string) []RepositoryConsumerEvidence {
	return pythonKafkaConsumerEvidenceInSourceWithGuard(file, source, KafkaConsumersEnabledEnv)
}

func pythonKafkaConsumerEvidenceInSourceWithGuard(file string, source string, envName string) []RepositoryConsumerEvidence {
	if !regexp.MustCompile(`(?i)(confluent_kafka|aiokafka|kafka(?:-python)?)`).MatchString(source) {
		return nil
	}
	code := stripPythonComments(source)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b(?:KafkaConsumer|AIOKafkaConsumer|Consumer)\s*\(`),
	}
	return textKafkaEvidence(file, code, patterns, pythonKafkaGuardRanges(code, envName))
}

func pythonKafkaGuardRanges(source string, envName string) [][2]int {
	if envName == "" {
		return nil
	}
	condition := regexp.MustCompile(`^if\s+os\.(?:getenv|environ\.get)\(\s*["']` + regexp.QuoteMeta(envName) + `["']\s*,\s*["']true["']\s*\)\.lower\(\)\s*!=\s*["']false["']\s*:$`)
	lines := strings.SplitAfter(source, "\n")
	offsets := make([]int, len(lines)+1)
	for index, line := range lines {
		offsets[index+1] = offsets[index] + len(line)
	}
	result := make([][2]int, 0)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !condition.MatchString(trimmed) {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		end := index + 1
		for end < len(lines) {
			candidate := lines[end]
			if strings.TrimSpace(candidate) == "" {
				end++
				continue
			}
			candidateIndent := len(candidate) - len(strings.TrimLeft(candidate, " \t"))
			if candidateIndent <= indent {
				break
			}
			end++
		}
		result = append(result, [2]int{offsets[index], offsets[end]})
	}
	return result
}

func braceGuardRanges(source string, pattern *regexp.Regexp) [][2]int {
	result := make([][2]int, 0)
	for _, match := range pattern.FindAllStringIndex(source, -1) {
		opening := strings.LastIndex(source[match[0]:match[1]], "{")
		if opening < 0 {
			continue
		}
		opening += match[0]
		closing := matchingKafkaBrace(source, opening, true)
		if closing > opening {
			result = append(result, [2]int{opening, closing})
		}
	}
	return result
}

func matchingKafkaBrace(source string, opening int, rejectSlash bool) int {
	depth := 0
	quote := byte(0)
	escaped := false
	triple := false
	for index := opening; index < len(source); index++ {
		character := source[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if quote == '`' && character == '$' && index+1 < len(source) && source[index+1] == '{' {
				return -1
			}
			if triple {
				if strings.HasPrefix(source[index:], `"""`) {
					quote = 0
					triple = false
					index += 2
				}
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '"' && strings.HasPrefix(source[index:], `"""`) {
			quote = '"'
			triple = true
			index += 2
			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
			continue
		}
		if rejectSlash && character == '/' {
			return -1
		}
		switch character {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func textKafkaEvidence(file string, source string, patterns []*regexp.Regexp, trusted [][2]int) []RepositoryConsumerEvidence {
	indexes := make([]int, 0)
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringIndex(source, -1) {
			indexes = append(indexes, match[0])
		}
	}
	sort.Ints(indexes)
	result := make([]RepositoryConsumerEvidence, 0, len(indexes))
	for _, index := range indexes {
		protected := false
		for _, sourceRange := range trusted {
			if index > sourceRange[0] && index < sourceRange[1] {
				protected = true
				break
			}
		}
		result = append(result, RepositoryConsumerEvidence{
			Driver:    "kafka",
			File:      fmt.Sprintf("%s:%d", file, 1+strings.Count(source[:index], "\n")),
			Protected: protected,
		})
	}
	return result
}
