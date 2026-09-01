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
	mainClasses, hasGrpcService, hasController, err := inspectSpringSources(repository.Directory)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	if len(mainClasses) != 1 {
		return RepositoryAnalysis{}, false, nil
	}
	artifactName, err := springBootArtifactName(settingsPath, buildText)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("Spring Boot repository %q: %w", repository.Name, err)
	}
	if artifactName == "" {
		return RepositoryAnalysis{}, false, nil
	}

	hasGrpcStarter := strings.Contains(buildText, "grpc-server-spring-boot-starter")
	hasWebStarter := strings.Contains(buildText, "spring-boot-starter-web") || strings.Contains(buildText, "spring-boot-starter-webflux")
	kind := RepositoryKindUnknown
	grpc := hasGrpcStarter && hasGrpcService
	http := hasWebStarter && hasController
	if grpc && !http {
		kind = RepositoryKindRPC
	}
	if http && !grpc {
		kind = RepositoryKindHTTP
	}
	health := model.Health{}
	if kind == RepositoryKindHTTP || kind == RepositoryKindRPC {
		health = model.Health{Type: "tcp", Address: "127.0.0.1:${port." + kind + "}"}
	}
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		ModulePath:  mainClasses[0],
		Framework:   "spring-boot",
		Discovery:   "consul",
		Runner: model.Runner{
			Workdir:  ".",
			Artifact: "${serviceDir}/build/libs/" + artifactName,
			Build:    []string{"./gradlew", "bootJar"},
			Run:      []string{"java", "-jar", "${artifact}"},
		},
		Kind:   kind,
		Health: health,
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

func inspectSpringSources(repository string) ([]string, bool, bool, error) {
	root := filepath.Join(repository, "src", "main")
	rootExists, err := pathExists(root)
	if err != nil {
		return nil, false, false, fmt.Errorf("inspect Spring source root %s: %w", root, err)
	}
	if !rootExists {
		return nil, false, false, nil
	}
	mainClasses := make([]string, 0, 1)
	hasGrpcService := false
	hasController := false
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
		if err := validateSpringCustomConsulRegistration(repository, path, text); err != nil {
			return err
		}
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
		return nil, false, false, fmt.Errorf("inspect Spring sources: %w", err)
	}
	sort.Strings(mainClasses)
	return mainClasses, hasGrpcService, hasController, nil
}

func validateSpringCustomConsulRegistration(repository string, sourcePath string, source string) error {
	code := stripQuotedLiterals(source)
	registrations := customSpringConsulRegistrationIndexes(code)
	if len(registrations) == 0 {
		return nil
	}
	ranges := trustedSpringRegistrationRanges(source, code)
	for _, registration := range registrations {
		trusted := false
		for _, sourceRange := range ranges {
			if registration > sourceRange[0] && registration < sourceRange[1] {
				trusted = true
				break
			}
		}
		if trusted {
			continue
		}
		relative, err := filepath.Rel(repository, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve custom Consul registration source %s: %w", sourcePath, err)
		}
		return fmt.Errorf(`custom Consul registration in %s cannot be trusted because Conven could not verify a service.registration.enabled condition on the class that performs registration

Add this import and condition to that class:

import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;

@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
public class ConsulConfig {
    // existing registration and deregistration code
}

Conven passes --service.registration.enabled=false for local runs; matchIfMissing=true preserves existing deployments`, filepath.ToSlash(relative))
	}
	return nil
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

func springBootArtifactName(settingsPath string, buildText string) (string, error) {
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
	if name == "" || version == "" {
		return "", nil
	}
	if filepath.Base(name) != name || strings.ContainsAny(name+version, `/\\`) {
		return "", fmt.Errorf("rootProject.name and version must form a safe artifact file name")
	}
	return name + "-" + version + ".jar", nil
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
		case 3, 4:
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if state == 3 && character == '"' || state == 4 && character == '\'' {
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
			}
		}
	}
	return string(result)
}
