package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRepositoryMatchesRootGoModule(t *testing.T) {
	repository := newAnalyzerRepository(t, "root-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), "module example.com/root-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\nfunc main() {}\n")

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("root Go module was not matched")
	}
	if analysis.Analyzer != "go-root-module" {
		t.Fatalf("analyzer = %q", analysis.Analyzer)
	}
	if analysis.ServiceName != "root-service" || analysis.ModulePath != "example.com/root-service" {
		t.Fatalf("analysis identity = %#v", analysis)
	}
	assertAnalyzerRunner(t, analysis, ".")
}

func TestAnalyzeRepositoryMatchesGoSubdirectoryModule(t *testing.T) {
	repository := newAnalyzerRepository(t, "nested-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "go.mod"), "module example.com/nested-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "main.go"), "package main\n\nfunc main() {}\n")

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("go/ Go module was not matched")
	}
	if analysis.Analyzer != "go-subdirectory-module" {
		t.Fatalf("analyzer = %q", analysis.Analyzer)
	}
	assertAnalyzerRunner(t, analysis, "go")
}

func TestAnalyzeRepositoryRejectsMultipleAnalyzerMatches(t *testing.T) {
	repository := newAnalyzerRepository(t, "conflict-service")
	for _, directory := range []string{repository, filepath.Join(repository, "go")} {
		writeAnalyzerFile(t, filepath.Join(directory, "go.mod"), "module example.com/conflict-service\n")
		writeAnalyzerFile(t, filepath.Join(directory, "main.go"), "package main\n\nfunc main() {}\n")
	}

	_, matched, err := AnalyzeRepository(repository)
	if err == nil {
		t.Fatal("multiple analyzer matches were accepted")
	}
	if matched {
		t.Fatal("conflicting repository was reported as matched")
	}
	for _, expected := range []string{"matched multiple analyzers", "go-root-module", "go-subdirectory-module"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("conflict error is missing %q: %v", expected, err)
		}
	}
}

func TestAnalyzeRepositoryInfersKindsAndRPCClientBindings(t *testing.T) {
	tests := []struct {
		name       string
		workdir    string
		serverType string
		kind       string
		yamlKey    string
	}{
		{name: "http", workdir: ".", serverType: "rest.RestConf", kind: RepositoryKindHTTP, yamlKey: "partnerRpc"},
		{name: "rpc", workdir: "go", serverType: "zrpc.RpcServerConf", kind: RepositoryKindRPC, yamlKey: "visitRpc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositoryName := test.name + "-service"
			repository := newAnalyzerRepository(t, repositoryName)
			moduleDirectory := repository
			if test.workdir != "." {
				moduleDirectory = filepath.Join(repository, test.workdir)
			}
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "go.mod"), "module example.com/"+repositoryName+"\n")
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "main.go"), "package main\n\nfunc main() {}\n")
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "config", "config.go"), `package config

import (
	"example.com/rest"
	"example.com/zrpc"
)

type Config struct {
	Server `+test.serverType+` `+"`yaml:\",inline\"`"+`
	Client zrpc.RpcClientConf `+"`yaml:\""+test.yamlKey+"\" json:\",optional\"`"+`
	NoBinding zrpc.RpcClientConf `+"`json:\"noBinding\"`"+`
}
`)
			for _, skipped := range []string{".git", "testdata", "vendor"} {
				writeAnalyzerFile(t, filepath.Join(moduleDirectory, skipped, "broken.go"), "package\n")
			}

			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil {
				t.Fatal(err)
			}
			if !matched {
				t.Fatal("repository was not matched")
			}
			if analysis.Kind != test.kind {
				t.Fatalf("kind = %q, want %q", analysis.Kind, test.kind)
			}
			wantFile := "config/config.go"
			if test.workdir != "." {
				wantFile = test.workdir + "/" + wantFile
			}
			wantBindings := []RPCClientBindingCandidate{{
				File:       wantFile,
				StructName: "Config",
				FieldName:  "Client",
				YAMLKey:    test.yamlKey,
			}}
			if !reflect.DeepEqual(analysis.RPCClientBindings, wantBindings) {
				t.Fatalf("RPC client bindings = %#v, want %#v", analysis.RPCClientBindings, wantBindings)
			}
		})
	}
}

func TestAnalyzeRepositoryDoesNotModifyRepository(t *testing.T) {
	repository := newAnalyzerRepository(t, "read-only-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), "module example.com/read-only-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\nfunc main() {}\n")
	writeAnalyzerFile(t, filepath.Join(repository, "config", "config.go"), "package config\n")
	before := analyzerRepositorySnapshot(t, repository)

	if _, matched, err := AnalyzeRepository(repository); err != nil {
		t.Fatal(err)
	} else if !matched {
		t.Fatal("repository was not matched")
	}

	after := analyzerRepositorySnapshot(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("repository changed during analysis:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestAnalyzeRepositoryMatchesRootGradleSpringBootRPC(t *testing.T) {
	repository := newAnalyzerRepository(t, "data-mart-service")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'datamart'\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), `plugins {
    id 'org.springframework.boot' version '2.6.5'
}
version = '0.0.1-SNAPSHOT'
dependencies {
    implementation 'net.devh:grpc-server-spring-boot-starter:2.13.1.RELEASE'
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "DataMartApplication.java"), `
@SpringBootApplication
public class DataMartApplication {
    public static void main(String[] args) {
        SpringApplication.run(DataMartApplication.class, args);
    }
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "DataMartGrpcServer.java"), `
@GrpcService
public class DataMartGrpcServer {}
`)

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("Gradle Spring Boot repository was not matched")
	}
	if analysis.Analyzer != "java-gradle-spring-boot" || analysis.Framework != "spring-boot" || analysis.Discovery != "consul" || analysis.Kind != RepositoryKindRPC {
		t.Fatalf("analysis = %#v", analysis)
	}
	if analysis.Runner.Workdir != "." || analysis.Runner.Artifact != "${serviceDir}/build/libs/datamart-0.0.1-SNAPSHOT.jar" {
		t.Fatalf("runner = %#v", analysis.Runner)
	}
	if !reflect.DeepEqual(analysis.Runner.Build, []string{"./gradlew", "bootJar"}) || !reflect.DeepEqual(analysis.Runner.Run, []string{"java", "-jar", "${artifact}"}) {
		t.Fatalf("runner commands = %#v", analysis.Runner)
	}
	if analysis.Health.Type != "tcp" || analysis.Health.Address != "127.0.0.1:${port.rpc}" {
		t.Fatalf("health = %#v", analysis.Health)
	}
}

func TestAnalyzeRepositoryAcceptsProtectedSpringCustomConsulRegistration(t *testing.T) {
	repository := newAnalyzerRepository(t, "protected-consul-service")
	writeSpringAnalyzerRPCRepository(t, repository)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "ConsulConfig.java"), `
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;

@Configuration
@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
public class ConsulConfig {
    public void register() {
        client.agentServiceRegister(newService);
    }
}
`)

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched {
		t.Fatalf("protected custom registration result = %#v, matched = %t, error = %v", analysis, matched, err)
	}
}

func TestAnalyzeRepositoryRejectsUnprotectedSpringCustomConsulRegistrationWithExample(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "missing condition",
			source: `
@Configuration
public class ConsulConfig {
    public void register() {
        client.agentServiceRegister(newService);
    }
}
`,
		},
		{
			name: "condition belongs to another class",
			source: `
@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
class OtherConfig {}

class ConsulConfig {
    public void register() {
        client.agentServiceRegister(newService);
    }
}
`,
		},
		{
			name: "condition changes existing deployment default",
			source: `
@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = false
)
class ConsulConfig {
    public void register() {
        client.agentServiceRegister(newService);
    }
}
`,
		},
		{
			name: "Spring Consul service registry",
			source: `
class ConsulConfig {
    private ConsulServiceRegistry registry;
    public void register() {
        registry.register(registration);
    }
}
`,
		},
		{
			name: "Orbitz Consul agent client",
			source: `
class ConsulConfig {
    private AgentClient agentClient;
    public void register() {
        agentClient.register(registration);
    }
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newAnalyzerRepository(t, "unprotected-consul-service")
			writeSpringAnalyzerRPCRepository(t, repository)
			writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "ConsulConfig.java"), test.source)

			_, matched, err := AnalyzeRepository(repository)
			if err == nil || matched {
				t.Fatalf("unprotected custom registration result = matched %t, error = %v", matched, err)
			}
			for _, expected := range []string{
				"custom Consul registration",
				"src/main/java/ConsulConfig.java",
				"@ConditionalOnProperty(",
				`prefix = "service.registration"`,
				"matchIfMissing = true",
				"--service.registration.enabled=false",
			} {
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("custom registration error is missing %q: %v", expected, err)
				}
			}
		})
	}
}

func TestAnalyzeRepositoryIgnoresSpringCustomConsulExamplesInCommentsAndStrings(t *testing.T) {
	repository := newAnalyzerRepository(t, "documented-consul-service")
	writeSpringAnalyzerRPCRepository(t, repository)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "Documentation.java"), `
class Documentation {
    // client.agentServiceRegister(newService);
    String example = "client.agentServiceRegister(newService);";
}
`)

	if _, matched, err := AnalyzeRepository(repository); err != nil || !matched {
		t.Fatalf("documented custom registration result = matched %t, error = %v", matched, err)
	}
}

func TestAnalyzeRepositoryMatchesKotlinGradleSpringBootHTTPWithExplicitArtifact(t *testing.T) {
	repository := newAnalyzerRepository(t, "catalog-api")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle.kts"), "rootProject.name = \"dynamic-is-not-used\"\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle.kts"), `plugins {
    id("org.springframework.boot") version "3.4.0"
}
version = providers.gradleProperty("releaseVersion")
tasks.bootJar {
    archiveFileName.set("catalog-api.jar")
}
dependencies {
    implementation("org.springframework.boot:spring-boot-starter-web")
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "kotlin", "CatalogApplication.kt"), `
@SpringBootApplication
class CatalogApplication
fun main(args: Array<String>) {
    SpringApplication.run(CatalogApplication::class.java, *args)
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "kotlin", "CatalogController.kt"), `
@RestController
class CatalogController
`)

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched {
		t.Fatalf("analysis = %#v, matched = %t, error = %v", analysis, matched, err)
	}
	if analysis.Kind != RepositoryKindHTTP || analysis.Runner.Artifact != "${serviceDir}/build/libs/catalog-api.jar" {
		t.Fatalf("analysis = %#v", analysis)
	}
}

func TestAnalyzeRepositoryDoesNotGuessMixedSpringBootKind(t *testing.T) {
	repository := newAnalyzerRepository(t, "mixed-service")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'mixed'\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), `plugins { id 'org.springframework.boot' version '3.4.0' }
version = '1.0.0'
dependencies {
    implementation 'net.devh:grpc-server-spring-boot-starter:3.1.0.RELEASE'
    implementation 'org.springframework.boot:spring-boot-starter-web'
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "MixedApplication.java"), `
@SpringBootApplication
public class MixedApplication {
    public static void main(String[] args) { SpringApplication.run(MixedApplication.class, args); }
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "Endpoints.java"), "@GrpcService class Rpc {}\n@RestController class Http {}\n")

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched {
		t.Fatalf("analysis = %#v, matched = %t, error = %v", analysis, matched, err)
	}
	if analysis.Kind != RepositoryKindUnknown || analysis.Health.Type != "" {
		t.Fatalf("mixed repository kind was guessed: %#v", analysis)
	}
}

func TestAnalyzeRepositorySkipsDynamicSpringBootArtifact(t *testing.T) {
	repository := newAnalyzerRepository(t, "dynamic-service")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = providers.gradleProperty('name')\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), "plugins { id 'org.springframework.boot' version '3.4.0' }\nversion = releaseVersion\n")
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "DynamicApplication.java"), `
@SpringBootApplication
public class DynamicApplication {
    public static void main(String[] args) { SpringApplication.run(DynamicApplication.class, args); }
}
`)

	_, matched, err := AnalyzeRepository(repository)
	if err != nil || matched {
		t.Fatalf("dynamic artifact result = matched %t, error %v", matched, err)
	}
}

func TestAnalyzeRepositoryIgnoresCommentedSpringAnnotations(t *testing.T) {
	repository := newAnalyzerRepository(t, "commented-service")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'commented'\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), "plugins { id 'org.springframework.boot' version '3.4.0' }\nversion = '1.0.0'\ndependencies { implementation 'net.devh:grpc-server-spring-boot-starter:3.1.0.RELEASE' }\n")
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "CommentedApplication.java"), `
@SpringBootApplication
public class CommentedApplication {
    public static void main(String[] args) { SpringApplication.run(CommentedApplication.class, args); }
    // @GrpcService
    String annotation = "@GrpcService";
}
`)

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched {
		t.Fatalf("analysis = %#v, matched = %t, error = %v", analysis, matched, err)
	}
	if analysis.Kind != RepositoryKindUnknown {
		t.Fatalf("commented annotation inferred kind %q", analysis.Kind)
	}
}

func TestAnalyzeRepositorySkipsMultipleSpringBootMainClasses(t *testing.T) {
	repository := newAnalyzerRepository(t, "multiple-main-service")
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'multiple-main'\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), "plugins { id 'org.springframework.boot' version '3.4.0' }\nversion = '1.0.0'\n")
	for _, name := range []string{"FirstApplication", "SecondApplication"} {
		writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", name+".java"), "@SpringBootApplication public class "+name+" { public static void main(String[] args) { SpringApplication.run("+name+".class, args); } }\n")
	}

	_, matched, err := AnalyzeRepository(repository)
	if err != nil || matched {
		t.Fatalf("multiple main result = matched %t, error %v", matched, err)
	}
}

func assertAnalyzerRunner(t *testing.T, analysis RepositoryAnalysis, workdir string) {
	t.Helper()
	if analysis.Runner.Workdir != workdir {
		t.Fatalf("runner workdir = %q, want %q", analysis.Runner.Workdir, workdir)
	}
	wantBuild := []string{"go", "build", "-o", "${artifact}", "."}
	if !reflect.DeepEqual(analysis.Runner.Build, wantBuild) {
		t.Fatalf("runner build = %#v, want %#v", analysis.Runner.Build, wantBuild)
	}
	if !reflect.DeepEqual(analysis.Runner.Run, []string{"${artifact}"}) {
		t.Fatalf("runner run = %#v", analysis.Runner.Run)
	}
}

func newAnalyzerRepository(t *testing.T, name string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	return repository
}

func writeAnalyzerFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeSpringAnalyzerRPCRepository(t *testing.T, repository string) {
	t.Helper()
	writeAnalyzerFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeAnalyzerFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'service'\n")
	writeAnalyzerFile(t, filepath.Join(repository, "build.gradle"), "plugins { id 'org.springframework.boot' version '3.4.0' }\nversion = '1.0.0'\ndependencies { implementation 'net.devh:grpc-server-spring-boot-starter:3.1.0.RELEASE' }\n")
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "Application.java"), "@SpringBootApplication public class Application { public static void main(String[] args) { SpringApplication.run(Application.class, args); } }\n")
	writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "GrpcServer.java"), "@GrpcService public class GrpcServer {}\n")
}

func analyzerRepositorySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[filepath.ToSlash(relative)+"/"] = entry.Type().String()
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
