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
const KafkaConsumersDefaultValue = "true"

func InspectKafkaConsumerEvidence(repository string, workdir string) ([]RepositoryConsumerEvidence, error) {
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
			evidence, err = goKafkaConsumerEvidenceInSource(file, source)
		case ".java", ".kt":
			evidence = javaKafkaConsumerEvidenceInSource(file, string(source))
		case ".js", ".cjs", ".mjs", ".ts":
			evidence = nodeKafkaConsumerEvidenceInSource(file, string(source))
		case ".py":
			evidence = pythonKafkaConsumerEvidenceInSource(file, string(source))
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
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, file, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s for Kafka consumer analysis: %w", file, err)
	}
	result := make([]RepositoryConsumerEvidence, 0)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			consumer := false
			switch value := node.(type) {
			case *ast.CallExpr:
				consumer = recognizedGoKafkaConsumerCall(value)
			case *ast.CompositeLit:
				consumer = recognizedGoKafkaConsumerLiteral(value.Type)
			}
			if !consumer {
				return true
			}
			position := set.Position(node.Pos())
			result = append(result, RepositoryConsumerEvidence{
				Driver:    "kafka",
				File:      fmt.Sprintf("%s:%d", file, position.Line),
				Protected: goKafkaCallProtected(function, node),
			})
			return true
		})
	}
	return result, nil
}

func recognizedGoKafkaConsumerCall(callExpression *ast.CallExpr) bool {
	selector, ok := callExpression.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
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
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Reader" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == "kafka"
}

func goKafkaCallProtected(function *ast.FuncDecl, call ast.Node) bool {
	for _, statement := range function.Body.List {
		guard, ok := statement.(*ast.IfStmt)
		if !ok {
			continue
		}
		if guard.End() < call.Pos() && goKafkaDisabledCondition(guard.Cond) && blockReturns(guard.Body) {
			return true
		}
		if call.Pos() > guard.Body.Pos() && call.End() < guard.Body.End() && goKafkaEnabledCondition(guard.Cond) {
			return true
		}
	}
	return false
}

func goKafkaDisabledCondition(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return goKafkaDisabledCondition(value.X)
	case *ast.CallExpr:
		selector, ok := value.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		qualifier, qualified := selector.X.(*ast.Ident)
		return qualified && qualifier.Name == "strings" && selector.Sel.Name == "EqualFold" && len(value.Args) == 2 &&
			((goKafkaEnvironmentLookup(value.Args[0]) && goStringLiteral(value.Args[1]) == "false") ||
				(goKafkaEnvironmentLookup(value.Args[1]) && goStringLiteral(value.Args[0]) == "false"))
	case *ast.BinaryExpr:
		return value.Op == token.EQL && ((goKafkaEnvironmentLookup(value.X) && goStringLiteral(value.Y) == "false") ||
			(goKafkaEnvironmentLookup(value.Y) && goStringLiteral(value.X) == "false"))
	default:
		return false
	}
}

func goKafkaEnabledCondition(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return goKafkaEnabledCondition(value.X)
	case *ast.UnaryExpr:
		return value.Op == token.NOT && goKafkaDisabledCondition(value.X)
	case *ast.BinaryExpr:
		return value.Op == token.NEQ && ((goKafkaEnvironmentLookup(value.X) && goStringLiteral(value.Y) == "false") ||
			(goKafkaEnvironmentLookup(value.Y) && goStringLiteral(value.X) == "false"))
	default:
		return false
	}
}

func goKafkaEnvironmentLookup(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || goStringLiteral(call.Args[0]) != KafkaConsumersEnabledEnv {
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
	code := stripCStyleComments(source)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`@KafkaListener\b`),
		regexp.MustCompile(`\bnew\s+(?:[A-Za-z_][A-Za-z0-9_]*\.)*KafkaConsumer\s*[<(]`),
		regexp.MustCompile(`\b(?:ConcurrentMessageListenerContainer|KafkaMessageListenerContainer)\s*[<(]`),
	}
	trusted := trustedSpringKafkaConsumerRanges(source, code)
	return textKafkaEvidence(file, code, patterns, trusted)
}

func trustedSpringKafkaConsumerRanges(source string, code string) [][2]int {
	annotationPattern := regexp.MustCompile(`(?s)@(?:org\.springframework\.boot\.autoconfigure\.condition\.)?ConditionalOnProperty\s*\((.*?)\)`)
	classPattern := regexp.MustCompile(`(?s)\b(?:public\s+|protected\s+|private\s+|internal\s+|open\s+|final\s+|abstract\s+|data\s+)*(?:class|object)\s+[A-Za-z_][A-Za-z0-9_]*[^\{]*\{`)
	ranges := make([][2]int, 0)
	for _, annotation := range annotationPattern.FindAllStringSubmatchIndex(source, -1) {
		if len(annotation) < 4 || !trustedSpringKafkaConsumerCondition(source[annotation[2]:annotation[3]]) {
			continue
		}
		class := classPattern.FindStringIndex(code[annotation[1]:])
		if class == nil {
			continue
		}
		classStart := annotation[1] + class[0]
		opening := strings.IndexByte(code[classStart:annotation[1]+class[1]], '{')
		if opening < 0 {
			continue
		}
		opening += classStart
		closing := matchingBrace(code, opening)
		if closing > opening {
			ranges = append(ranges, [2]int{opening, closing})
		}
	}
	return ranges
}

func trustedSpringKafkaConsumerCondition(arguments string) bool {
	return springConditionalStringValue(arguments, "prefix") == "service.kafka.consumers" &&
		springConditionalStringValue(arguments, "name") == "enabled" &&
		springConditionalStringValue(arguments, "havingValue") == "true" &&
		regexp.MustCompile(`(?s)(?:^|,)\s*matchIfMissing\s*=\s*true\s*(?:,|$)`).MatchString(arguments)
}

func nodeKafkaConsumerEvidenceInSource(file string, source string) []RepositoryConsumerEvidence {
	code := stripCStyleComments(source)
	if !regexp.MustCompile(`(?i)(kafkajs|node-rdkafka|@confluentinc/kafka-javascript|from\s+["'][^"']*kafka[^"']*["'])`).MatchString(source) {
		return nil
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.\s*subscribe\s*\(`),
		regexp.MustCompile(`\.\s*run\s*\(`),
		regexp.MustCompile(`\bnew\s+KafkaConsumer\s*\(`),
	}
	return textKafkaEvidence(file, code, patterns, javascriptKafkaGuardRanges(code))
}

func javascriptKafkaGuardRanges(source string) [][2]int {
	pattern := regexp.MustCompile(`(?s)if\s*\([^)]*(?:process\.env|Bun\.env)\.` + KafkaConsumersEnabledEnv + `[^)]*(?:!==?|!=)\s*["']false["'][^)]*\)\s*\{`)
	return braceGuardRanges(source, pattern)
}

func pythonKafkaConsumerEvidenceInSource(file string, source string) []RepositoryConsumerEvidence {
	if !regexp.MustCompile(`(?i)(confluent_kafka|aiokafka|kafka(?:-python)?)`).MatchString(source) {
		return nil
	}
	code := stripPythonComments(source)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b(?:KafkaConsumer|AIOKafkaConsumer|Consumer)\s*\(`),
	}
	return textKafkaEvidence(file, code, patterns, pythonKafkaGuardRanges(code))
}

func pythonKafkaGuardRanges(source string) [][2]int {
	lines := strings.SplitAfter(source, "\n")
	offsets := make([]int, len(lines)+1)
	for index, line := range lines {
		offsets[index+1] = offsets[index] + len(line)
	}
	result := make([][2]int, 0)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "if ") || !strings.Contains(trimmed, KafkaConsumersEnabledEnv) || !strings.Contains(trimmed, "false") || !strings.HasSuffix(trimmed, ":") {
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
		closing := matchingBrace(source, opening)
		if closing > opening {
			result = append(result, [2]int{opening, closing})
		}
	}
	return result
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
