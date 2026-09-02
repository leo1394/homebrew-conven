package config

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type JavaMavenServiceAdapter struct{}

func init() {
	RegisterRepositoryAnalyzer(40, JavaGradleOtherServiceAdapter{})
	RegisterRepositoryAnalyzer(50, JavaMavenServiceAdapter{})
	registerDriverRepositoryCertifier("quarkus", certifyRegistrationEvidence, environmentPolicyCompatible)
	registerDriverRepositoryCertifier("micronaut", certifyRegistrationEvidence, environmentPolicyCompatible)
}

func (JavaMavenServiceAdapter) Name() string { return "java-maven-service" }

func (JavaMavenServiceAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	if !fileExists(filepath.Join(repository.Directory, "mvnw")) { return RepositoryAnalysis{}, false, nil }
	pomPath := filepath.Join(repository.Directory, "pom.xml")
	data, err := os.ReadFile(pomPath)
	if errors.Is(err, os.ErrNotExist) { return RepositoryAnalysis{}, false, nil }
	if err != nil { return RepositoryAnalysis{}, false, err }
	text := stripXMLComments(string(data))
	framework, runtimeName := javaBuildFramework(text)
	if framework == "" { return RepositoryAnalysis{}, false, nil }
	var pom struct {
		ArtifactID string `xml:"artifactId"`
		Version string `xml:"version"`
		Build struct { FinalName string `xml:"finalName"` } `xml:"build"`
	}
	if err := xml.Unmarshal(data, &pom); err != nil { return RepositoryAnalysis{}, false, fmt.Errorf("decode %s: %w", pomPath, err) }
	kinds, main, registrations, err := inspectJavaFrameworkSources(repository.Directory, framework, text)
	if err != nil { return RepositoryAnalysis{}, false, err }
	if len(kinds) == 0 { return RepositoryAnalysis{}, false, fmt.Errorf("%s repository %q has no statically proven HTTP or RPC listener", framework, repository.Name) }
	artifact := pom.Build.FinalName
	if artifact == "" {
		if strings.ContainsAny(pom.ArtifactID+pom.Version, "${}") || pom.ArtifactID == "" || pom.Version == "" {
			return RepositoryAnalysis{}, false, fmt.Errorf("%s repository %q must declare a literal build.finalName or literal artifactId and version", framework, repository.Name)
		}
		artifact = pom.ArtifactID + "-" + pom.Version
	}
	artifactPath := "${serviceDir}/target/" + artifact + ".jar"
	if framework == "quarkus" { artifactPath = "${serviceDir}/target/quarkus-app/quarkus-run.jar" }
	discovery := javaDiscoveryDriver(text, registrations)
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		ModulePath: main,
		Framework: framework,
		Runtime: runtimeName,
		Discovery: discovery,
		Runner: model.Runner{Workdir: ".", Artifact: artifactPath, Build: []string{"./mvnw", "-DskipTests", "package"}, Run: []string{"java", "-jar", "${artifact}"}},
		Kind: firstKind(kinds),
		Kinds: kinds,
		HealthChecks: tcpHealthChecks(kinds),
		Registrations: registrations,
	}, true, nil
}

type JavaGradleOtherServiceAdapter struct{}

func (JavaGradleOtherServiceAdapter) Name() string { return "java-gradle-framework" }

func (JavaGradleOtherServiceAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	if !fileExists(filepath.Join(repository.Directory, "gradlew")) { return RepositoryAnalysis{}, false, nil }
	buildPath, found, err := oneRootGradleFile(repository.Directory, "build.gradle", "build.gradle.kts")
	if err != nil || !found { return RepositoryAnalysis{}, false, err }
	settingsPath, settingsFound, err := oneRootGradleFile(repository.Directory, "settings.gradle", "settings.gradle.kts")
	if err != nil || !settingsFound { return RepositoryAnalysis{}, false, err }
	data, err := os.ReadFile(buildPath); if err != nil { return RepositoryAnalysis{}, false, err }
	text := stripCStyleComments(string(data))
	framework, runtimeName := javaBuildFramework(text)
	if framework != "quarkus" && framework != "micronaut" { return RepositoryAnalysis{}, false, nil }
	kinds, main, registrations, err := inspectJavaFrameworkSources(repository.Directory, framework, text)
	if err != nil { return RepositoryAnalysis{}, false, err }
	if len(kinds) == 0 { return RepositoryAnalysis{}, false, fmt.Errorf("%s repository %q has no statically proven HTTP or RPC listener", framework, repository.Name) }
	artifactName, err := springBootArtifactName(repository.Directory, settingsPath, text)
	if err != nil { return RepositoryAnalysis{}, false, err }
	if artifactName == "" && framework == "micronaut" {
		return RepositoryAnalysis{}, false, fmt.Errorf("Micronaut repository %q must declare a literal archiveFileName or literal rootProject.name and version", repository.Name)
	}
	artifact := "${serviceDir}/build/libs/" + artifactName
	build := []string{"./gradlew", "build"}
	if framework == "quarkus" { artifact = "${serviceDir}/build/quarkus-app/quarkus-run.jar"; build = []string{"./gradlew", "quarkusBuild"} }
	return RepositoryAnalysis{
		ServiceName: repository.Name, ModulePath: main, Framework: framework, Runtime: runtimeName,
		Discovery: javaDiscoveryDriver(text, registrations),
		Runner: model.Runner{Workdir: ".", Artifact: artifact, Build: build, Run: []string{"java", "-jar", "${artifact}"}},
		Kind: firstKind(kinds), Kinds: kinds, HealthChecks: tcpHealthChecks(kinds), Registrations: registrations,
	}, true, nil
}

func javaBuildFramework(source string) (string, string) {
	lower := strings.ToLower(source)
	switch {
	case strings.Contains(lower, "spring-boot") || strings.Contains(lower, "org.springframework.boot"):
		return "spring-boot", "spring-boot"
	case strings.Contains(lower, "quarkus"):
		return "quarkus", "quarkus"
	case strings.Contains(lower, "micronaut"):
		return "micronaut", "micronaut"
	default:
		return "", ""
	}
}

func inspectJavaFrameworkSources(directory string, framework string, build string) ([]string, string, []RepositoryRegistrationEvidence, error) {
	if framework == "spring-boot" {
		main, grpc, http, registrations, err := inspectSpringSources(directory)
		if err != nil { return nil, "", nil, err }
		if len(main) != 1 { return nil, "", nil, fmt.Errorf("Spring Boot repository requires exactly one @SpringBootApplication entry; found %d", len(main)) }
		kinds := make([]string, 0, 2)
		if http && (strings.Contains(build, "spring-boot-starter-web") || strings.Contains(build, "spring-boot-starter-webflux")) { kinds = append(kinds, RepositoryKindHTTP) }
		if grpc && strings.Contains(build, "grpc") { kinds = append(kinds, RepositoryKindRPC) }
		return kinds, main[0], registrations, nil
	}
	source, err := readTextSources(filepath.Join(directory, "src", "main"), []string{".java", ".kt"})
	if err != nil && !errors.Is(err, os.ErrNotExist) { return nil, "", nil, err }
	kinds := make([]string, 0, 2)
	if regexp.MustCompile(`@(Path|Controller|Get|Post|Route)\b`).MatchString(source) || strings.Contains(build, "resteasy") || strings.Contains(build, "micronaut-http-server") { kinds = append(kinds, RepositoryKindHTTP) }
	if strings.Contains(source, "@GrpcService") || strings.Contains(build, "grpc") { kinds = append(kinds, RepositoryKindRPC) }
	registrations := registrationEvidenceFromText("src/main", source, javaDiscoveryDriver(build, nil))
	return kinds, "src/main", registrations, nil
}

func javaDiscoveryDriver(source string, registrations []RepositoryRegistrationEvidence) string {
	lower := strings.ToLower(source)
	switch {
	case strings.Contains(lower, "nacos"): return "nacos"
	case strings.Contains(lower, "eureka"): return "eureka"
	case strings.Contains(lower, "consul") || len(registrations) > 0: return "consul"
	case strings.Contains(lower, "etcd"): return "etcd"
	default: return "passive"
	}
}

func stripXMLComments(source string) string {
	return regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(source, "")
}
