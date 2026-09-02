package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type JavaGradleSpringBootAdapter struct{}

func init() {
	RegisterRepositoryAnalyzer(30, JavaGradleSpringBootAdapter{})
	registerDriverRepositoryCertifier("spring-boot", certifySpringRegistration, springPolicyCompatible)
}

func (JavaGradleSpringBootAdapter) Name() string {
	return "java-gradle-spring-boot"
}

func (JavaGradleSpringBootAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	wrapper := filepath.Join(repository.Directory, "gradlew")
	buildPath, buildFound, err := oneRootGradleFile(repository.Directory, "build.gradle", "build.gradle.kts")
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	settingsPath, settingsFound, err := oneRootGradleFile(repository.Directory, "settings.gradle", "settings.gradle.kts")
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	wrapperFound, err := regularFileExists(wrapper)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("inspect %s: %w", wrapper, err)
	}
	if !wrapperFound || !buildFound || !settingsFound {
		return RepositoryAnalysis{}, false, nil
	}
	buildSource, err := os.ReadFile(buildPath)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("read %s: %w", buildPath, err)
	}
	buildText := stripCStyleComments(string(buildSource))
	if !hasSpringBootGradlePlugin(buildText) {
		return RepositoryAnalysis{}, false, nil
	}
	mainClasses, hasGrpcService, hasController, registrations, err := inspectSpringSources(repository.Directory)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	if len(mainClasses) != 1 {
		return RepositoryAnalysis{}, false, fmt.Errorf("Spring Boot repository %q requires exactly one @SpringBootApplication entry that calls SpringApplication.run; found %d (%s); keep one executable application entry before services --registry", repository.Name, len(mainClasses), strings.Join(mainClasses, ", "))
	}
	artifactName, err := springBootArtifactName(repository.Directory, settingsPath, buildText)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("Spring Boot repository %q: %w", repository.Name, err)
	}
	if artifactName == "" {
		return RepositoryAnalysis{}, false, fmt.Errorf("Spring Boot repository %q artifact cannot be determined; set bootJar.archiveFileName to a literal JAR filename, or make rootProject.name and version literal values", repository.Name)
	}

	hasGrpcStarter := strings.Contains(buildText, "grpc-server-spring-boot-starter")
	hasWebStarter := strings.Contains(buildText, "spring-boot-starter-web") || strings.Contains(buildText, "spring-boot-starter-webflux")
	kinds := make([]string, 0, 2)
	grpc := hasGrpcStarter && hasGrpcService
	http := hasWebStarter && hasController
	if http {
		kinds = append(kinds, RepositoryKindHTTP)
	}
	if grpc {
		kinds = append(kinds, RepositoryKindRPC)
	}
	if len(kinds) == 0 {
		return RepositoryAnalysis{}, false, fmt.Errorf("Spring Boot repository %q has no statically proven listener; HTTP requires a Web starter and @RestController/@Controller, while gRPC requires the server starter and @GrpcService", repository.Name)
	}
	kind := RepositoryKindUnknown
	if len(kinds) == 1 { kind = kinds[0] }
	health := model.Health{}
	if kind == RepositoryKindHTTP || kind == RepositoryKindRPC {
		health = model.Health{Type: "tcp", Address: "127.0.0.1:${port." + kind + "}"}
	}
	healthChecks := make([]model.ServiceHealthCheck, 0, len(kinds))
	for _, server := range kinds {
		healthChecks = append(healthChecks, model.ServiceHealthCheck{Server: server, Type: "tcp", Address: "127.0.0.1:${port." + server + "}"})
	}
	discovery := springDiscoveryDriver(buildText, registrations)
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		ModulePath:  mainClasses[0],
		Framework:   "spring-boot",
		Runtime:     "spring-boot",
		Discovery:   discovery,
		Runner: model.Runner{
			Workdir:  ".",
			Artifact: "${serviceDir}/build/libs/" + artifactName,
			Build:    []string{"./gradlew", "bootJar"},
			Run:      []string{"java", "-jar", "${artifact}"},
		},
		Kind:          kind,
		Kinds:         kinds,
		Health:        health,
		HealthChecks:  healthChecks,
		Registrations: registrations,
	}, true, nil
}

func oneRootGradleFile(directory string, names ...string) (string, bool, error) {
	found := ""
	for _, name := range names {
		path := filepath.Join(directory, name)
		exists, err := regularFileExists(path)
		if err != nil {
			return "", false, fmt.Errorf("inspect %s: %w", path, err)
		}
		if !exists {
			continue
		}
		if found != "" {
			return "", false, fmt.Errorf("repository contains both %s and %s", filepath.Base(found), name)
		}
		found = path
	}
	return found, found != "", nil
}

func inspectSpringSources(repository string) ([]string, bool, bool, []RepositoryRegistrationEvidence, error) {
	root := filepath.Join(repository, "src", "main")
	rootExists, err := pathExists(root)
	if err != nil {
		return nil, false, false, nil, fmt.Errorf("inspect Spring source root %s: %w", root, err)
	}
	if !rootExists {
		return nil, false, false, nil, nil
	}
	mainClasses := make([]string, 0, 1)
	hasGrpcService := false
	hasController := false
	registrations := make([]RepositoryRegistrationEvidence, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && skippedAnalysisDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".java" && extension != ".kt" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := stripCStyleComments(string(source))
		detected, err := inspectSpringCustomConsulRegistration(repository, path, text)
		if err != nil {
			return err
		}
		registrations = append(registrations, detected...)
		if regexp.MustCompile(`(?m)^\s*@GrpcService\b`).MatchString(text) {
			hasGrpcService = true
		}
		if regexp.MustCompile(`(?m)^\s*@(?:RestController|Controller)\b`).MatchString(text) {
			hasController = true
		}
		if !regexp.MustCompile(`(?m)^\s*@SpringBootApplication\b`).MatchString(text) {
			return nil
		}
		hasMain := regexp.MustCompile(`\bstatic\s+void\s+main\s*\(`).MatchString(text) || regexp.MustCompile(`\bfun\s+main\s*\(`).MatchString(text)
		if !hasMain || !strings.Contains(text, "SpringApplication.run") {
			return nil
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		mainClasses = append(mainClasses, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, false, false, nil, fmt.Errorf("inspect Spring sources: %w", err)
	}
	sort.Strings(mainClasses)
	return mainClasses, hasGrpcService, hasController, registrations, nil
}

func inspectSpringCustomConsulRegistration(repository string, sourcePath string, source string) ([]RepositoryRegistrationEvidence, error) {
	code := stripQuotedLiterals(source)
	registrations := customSpringConsulRegistrationIndexes(code)
	if len(registrations) == 0 {
		return nil, nil
	}
	relative, err := filepath.Rel(repository, sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve custom Consul registration source %s: %w", sourcePath, err)
	}
	ranges := trustedSpringRegistrationRanges(source, code)
	evidence := make([]RepositoryRegistrationEvidence, 0, len(registrations))
	for _, registration := range registrations {
		trusted := false
		for _, sourceRange := range ranges {
			if registration > sourceRange[0] && registration < sourceRange[1] {
				trusted = true
				break
			}
		}
		evidence = append(evidence, RepositoryRegistrationEvidence{
			Provider:  "consul",
			File:      fmt.Sprintf("%s:%d", filepath.ToSlash(relative), 1+strings.Count(source[:registration], "\n")),
			Protected: trusted,
		})
	}
	return evidence, nil
}

func customSpringConsulRegistrationIndexes(code string) []int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bagentServiceRegister\s*\(`),
		regexp.MustCompile(`\.\s*agentClient\s*\(\s*\)\s*\.\s*register\s*\(`),
	}
	javaVariables := regexp.MustCompile(`\b(?:ConsulServiceRegistry|AgentClient)\s+([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(code, -1)
	for _, variable := range javaVariables {
		patterns = append(patterns, regexp.MustCompile(`\b`+regexp.QuoteMeta(variable[1])+`\s*\.\s*register\s*\(`))
	}
	kotlinVariables := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)*(?:ConsulServiceRegistry|AgentClient)\b`).FindAllStringSubmatch(code, -1)
	for _, variable := range kotlinVariables {
		patterns = append(patterns, regexp.MustCompile(`\b`+regexp.QuoteMeta(variable[1])+`\s*\.\s*register\s*\(`))
	}
	indexes := make([]int, 0)
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringIndex(code, -1) {
			indexes = append(indexes, match[0])
		}
	}
	return indexes
}

func trustedSpringRegistrationRanges(source string, code string) [][2]int {
	annotationPattern := regexp.MustCompile(`(?s)@(?:org\.springframework\.boot\.autoconfigure\.condition\.)?ConditionalOnProperty\s*\((.*?)\)`)
	classPattern := regexp.MustCompile(`(?s)\b(?:public\s+|protected\s+|private\s+|internal\s+|open\s+|final\s+|abstract\s+|data\s+)*(?:class|object)\s+[A-Za-z_][A-Za-z0-9_]*[^\{]*\{`)
	ranges := make([][2]int, 0)
	for _, annotation := range annotationPattern.FindAllStringSubmatchIndex(source, -1) {
		if len(annotation) < 4 || !trustedSpringRegistrationCondition(source[annotation[2]:annotation[3]]) {
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

func trustedSpringRegistrationCondition(arguments string) bool {
	return springConditionalStringValue(arguments, "prefix") == "service.registration" &&
		springConditionalStringValue(arguments, "name") == "enabled" &&
		springConditionalStringValue(arguments, "havingValue") == "true" &&
		regexp.MustCompile(`(?s)(?:^|,)\s*matchIfMissing\s*=\s*true\s*(?:,|$)`).MatchString(arguments)
}

func springConditionalStringValue(arguments string, name string) string {
	pattern := regexp.MustCompile(`(?s)(?:^|,)\s*` + regexp.QuoteMeta(name) + `\s*=\s*(?:\{\s*|\[\s*)?["']([^"']+)["'](?:\s*\}|\s*\])?\s*(?:,|$)`)
	match := pattern.FindStringSubmatch(arguments)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func matchingBrace(source string, opening int) int {
	depth := 0
	for index := opening; index < len(source); index++ {
		switch source[index] {
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

func stripQuotedLiterals(source string) string {
	result := []byte(source)
	quote := byte(0)
	escaped := false
	for index := 0; index < len(result); index++ {
		character := result[index]
		if quote == 0 {
			if character == '"' || character == '\'' {
				quote = character
				result[index] = ' '
			}
			continue
		}
		if character != '\n' {
			result[index] = ' '
		}
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == quote {
			quote = 0
		}
	}
	return string(result)
}

func hasSpringBootGradlePlugin(source string) bool {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bid\s*(?:\(\s*)?["']org\.springframework\.boot["']`),
		regexp.MustCompile(`\bapply\s+plugin\s*:\s*["']org\.springframework\.boot["']`),
		regexp.MustCompile(`\bapply\s*\(\s*plugin\s*=\s*["']org\.springframework\.boot["']\s*\)`),
	}
	for _, pattern := range patterns {
		if pattern.MatchString(source) {
			return true
		}
	}
	return false
}

func springBootArtifactName(directory string, settingsPath string, buildText string) (string, error) {
	explicitPattern := regexp.MustCompile(`(?m)\barchiveFileName(?:\.set)?\s*(?:=\s*|\(\s*)["']([^"']+\.jar)["']\s*\)?`)
	explicitMatches := explicitPattern.FindAllStringSubmatch(buildText, -1)
	explicit := make(map[string]bool)
	for _, match := range explicitMatches {
		explicit[match[1]] = true
	}
	if len(explicit) > 1 {
		return "", fmt.Errorf("bootJar declares multiple archiveFileName values")
	}
	for value := range explicit {
		if filepath.Base(value) != value {
			return "", fmt.Errorf("bootJar.archiveFileName must be a file name, got %q", value)
		}
		return value, nil
	}
	settingsSource, err := os.ReadFile(settingsPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", settingsPath, err)
	}
	settingsText := stripCStyleComments(string(settingsSource))
	name := oneLiteralAssignment(settingsText, `rootProject\.name`)
	version := oneLiteralAssignment(buildText, `version`)
	if version == "" {
		properties, err := os.ReadFile(filepath.Join(directory, "gradle.properties"))
		if err == nil {
			version = onePropertyAssignment(string(properties), "version")
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read gradle.properties: %w", err)
		}
	}
	if name == "" || version == "" {
		return "", nil
	}
	if filepath.Base(name) != name || strings.ContainsAny(name+version, `/\\`) {
		return "", fmt.Errorf("rootProject.name and version must form a safe artifact file name")
	}
	return name + "-" + version + ".jar", nil
}

func springDiscoveryDriver(buildText string, registrations []RepositoryRegistrationEvidence) string {
	switch {
	case strings.Contains(buildText, "nacos-discovery"):
		return "nacos"
	case strings.Contains(buildText, "eureka-client"):
		return "eureka"
	case strings.Contains(buildText, "spring-cloud-starter-consul") || len(registrations) > 0:
		return "consul"
	default:
		return "passive"
	}
}

func onePropertyAssignment(source string, key string) string {
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*([^\s#]+)\s*$`)
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		return ""
	}
	return strings.TrimSpace(matches[0][1])
}

func oneLiteralAssignment(source string, namePattern string) string {
	pattern := regexp.MustCompile(`(?m)^\s*` + namePattern + `\s*=\s*["']([^"']+)["']\s*$`)
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		return ""
	}
	return matches[0][1]
}

func stripCStyleComments(source string) string {
	result := []byte(source)
	state := byte(0)
	escaped := false
	for index := 0; index < len(result); index++ {
		character := result[index]
		next := byte(0)
		if index+1 < len(result) {
			next = result[index+1]
		}
		switch state {
		case 1:
			if character == '\n' {
				state = 0
			} else {
				result[index] = ' '
			}
		case 2:
			if character == '*' && next == '/' {
				result[index] = ' '
				result[index+1] = ' '
				index++
				state = 0
			} else if character != '\n' {
				result[index] = ' '
			}
		case 3, 4, 5:
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if state == 3 && character == '"' || state == 4 && character == '\'' || state == 5 && character == '`' {
				state = 0
			}
		default:
			if character == '/' && next == '/' {
				result[index] = ' '
				result[index+1] = ' '
				index++
				state = 1
			} else if character == '/' && next == '*' {
				result[index] = ' '
				result[index+1] = ' '
				index++
				state = 2
			} else if character == '"' {
				state = 3
			} else if character == '\'' {
				state = 4
			} else if character == '`' {
				state = 5
			}
		}
	}
	return string(result)
}
