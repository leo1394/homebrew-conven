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
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\ntype Config struct { Server rest.RestConf }\nfunc main() {}\n")

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
	writeAnalyzerFile(t, filepath.Join(repository, "go", "main.go"), "package main\n\ntype Config struct { Server rest.RestConf }\nfunc main() {}\n")

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

func TestAnalyzeRepositoryRejectsGoZeroConfigFlagWithoutParse(t *testing.T) {
	repository := newAnalyzerRepository(t, "unparsed-config-service")
	moduleDirectory := filepath.Join(repository, "go")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "go.mod"), "module example.com/unparsed-config-service\nrequire github.com/tal-tech/go-zero v1.0.0\n")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "main.go"), `package main

import "flag"

type Config struct { Server rest.RestConf }

func main() {
	configDir := flag.String("f", "../resources", "the config file")
	start(*configDir)
}

func start(string) {}
`)

	_, matched, err := AnalyzeRepository(repository)
	if err == nil {
		t.Fatal("unparsed go-zero -f flag was accepted")
	}
	if matched {
		t.Fatal("invalid go-zero repository was reported as matched")
	}
	for _, expected := range []string{"unparsed-config-service", "go/main.go", "flag.Parse()", "before reading -f"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error = %q, missing %q", err, expected)
		}
	}
}

func TestAnalyzeRepositoryAcceptsParsedGoZeroConfigFlag(t *testing.T) {
	repository := newAnalyzerRepository(t, "parsed-config-service")
	moduleDirectory := filepath.Join(repository, "go")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "go.mod"), "module example.com/parsed-config-service\nrequire github.com/tal-tech/go-zero v1.0.0\n")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "main.go"), `package main

import "flag"

type Config struct { Server rest.RestConf }

func main() {
	configDir := flag.String("f", "../resources", "the config file")
	flag.Parse()
	start(*configDir)
}

func start(string) {}
`)

	if _, matched, err := AnalyzeRepository(repository); err != nil || !matched {
		t.Fatalf("parsed go-zero repository = matched %t, error %v", matched, err)
	}
}

func TestAnalyzeRepositoryRejectsMissingGoLocalReplacement(t *testing.T) {
	repository := newAnalyzerRepository(t, "missing-module-service")
	moduleDirectory := filepath.Join(repository, "go")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "go.mod"), "module example.com/missing-module-service\nrequire github.com/tal-tech/go-zero v1.0.0\nreplace github.com/tal-tech/go-zero => ../internal/go-zero\n")
	writeAnalyzerFile(t, filepath.Join(moduleDirectory, "main.go"), `package main

import "flag"

type Config struct { Server rest.RestConf }

var configDir = flag.String("f", "../resources", "the config file")

func main() {
	flag.Parse()
	start(*configDir)
}

func start(string) {}
`)

	_, matched, err := AnalyzeRepository(repository)
	if err == nil || matched {
		t.Fatalf("missing local replacement = matched %t, error %v", matched, err)
	}
	for _, expected := range []string{"../internal/go-zero", "submodule update --init --recursive"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error = %q, missing %q", err, expected)
		}
	}
}

func TestAnalyzeRepositoryRejectsMultipleAnalyzerMatches(t *testing.T) {
	repository := newAnalyzerRepository(t, "conflict-service")
	for _, directory := range []string{repository, filepath.Join(repository, "go")} {
		writeAnalyzerFile(t, filepath.Join(directory, "go.mod"), "module example.com/conflict-service\n")
		writeAnalyzerFile(t, filepath.Join(directory, "main.go"), "package main\n\ntype Config struct { Server rest.RestConf }\nfunc main() {}\n")
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
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\ntype Config struct { Server rest.RestConf }\nfunc main() {}\n")
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
	if analysis.Analyzer != "java-gradle-spring-boot" || analysis.Framework != "spring-boot" || analysis.Discovery != "passive" || analysis.Kind != RepositoryKindRPC {
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
	want := []RepositoryRegistrationEvidence{{
		Provider:  "consul",
		File:      "src/main/java/ConsulConfig.java:13",
		Protected: true,
	}}
	if !reflect.DeepEqual(analysis.Registrations, want) {
		t.Fatalf("registration evidence = %#v, want %#v", analysis.Registrations, want)
	}
}

func TestGoRegistrationEvidenceIgnoresFrameworkAndDatabaseRegistration(t *testing.T) {
	source := []byte(`package main
func main() {
    reflection.Register(grpcServer)
    callbacks.Register("before_created", callback)
    customer.RegisterCustomerServer(grpcServer, server)
}
`)
	evidence, err := goRegistrationEvidenceInSource("main.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 {
		t.Fatalf("non-registry registration evidence = %#v", evidence)
	}
}

func TestGoRegistrationEvidenceReportsRegistryLocationAndGuard(t *testing.T) {
	source := []byte(`package main
func start() {
    if !strings.EqualFold(os.Getenv("SERVICE_REGISTRATION_ENABLED"), "false") {
        consulRegistry.Register(service)
        consulRegistry.Deregister(service)
    }
}
`)
	evidence, err := goRegistrationEvidenceInSource("main.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 || evidence[0].Line != 4 || !evidence[0].Protected || evidence[1].Line != 5 || !evidence[1].Protected {
		t.Fatalf("registry registration evidence = %#v", evidence)
	}
}

func TestAnalyzeRepositoryReportsUnprotectedSpringCustomConsulRegistration(t *testing.T) {
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

			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil || !matched {
				t.Fatalf("unprotected custom registration result = %#v, matched = %t, error = %v", analysis, matched, err)
			}
			if len(analysis.Registrations) == 0 {
				t.Fatal("unprotected custom registration evidence is missing")
			}
			for _, evidence := range analysis.Registrations {
				if evidence.Provider != "consul" || !strings.HasPrefix(evidence.File, "src/main/java/ConsulConfig.java:") || evidence.Protected {
					t.Fatalf("registration evidence = %#v", analysis.Registrations)
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
	if err == nil || matched || !strings.Contains(err.Error(), "artifact cannot be determined") {
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

	_, matched, err := AnalyzeRepository(repository)
	if err == nil || matched || !strings.Contains(err.Error(), "no statically proven listener") {
		t.Fatalf("commented annotation result = matched %t, error = %v", matched, err)
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
	if err == nil || matched || !strings.Contains(err.Error(), "exactly one @SpringBootApplication") {
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
	if filepath.Base(path) == "main.go" && strings.Contains(contents, "package main") {
		contents += "\nvar _ = Getenv(\"HOST\")\nvar _ = Getenv(\"PORT\")\nvar _ = Getenv(\"HTTP_PORT\")\nvar _ = Getenv(\"RPC_PORT\")\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == "go.mod" {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "go.sum"), []byte(""), 0600); err != nil {
			t.Fatal(err)
		}
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

func TestGoKafkaConsumerEvidenceRequiresNeutralGuard(t *testing.T) {
	unguarded := []byte(`package handler
func RegisterKQHandlers() {
    kq.NewQueue(config, handler)
}

`)
	evidence, err := goKafkaConsumerEvidenceInSource("handler/kq_routes.go", unguarded)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].File != "handler/kq_routes.go:3" || evidence[0].Protected {
		t.Fatalf("unguarded Kafka evidence = %#v", evidence)
	}

	guarded := []byte(`package handler
func RegisterKQHandlers() {
    if strings.EqualFold(os.Getenv("SERVICE_KAFKA_CONSUMERS_ENABLED"), "false") {
        return
    }
    kq.NewQueue(config, handler)
}
`)
	evidence, err = goKafkaConsumerEvidenceInSource("handler/kq_routes.go", guarded)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].File != "handler/kq_routes.go:6" || !evidence[0].Protected {
		t.Fatalf("guarded Kafka evidence = %#v", evidence)
	}
}

func TestKafkaConsumerEvidenceRequiresTrustedLanguageGuard(t *testing.T) {
	java := `@ConditionalOnProperty(
    prefix = "service.kafka.consumers",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
class Events {
    @KafkaListener(topics = "events")
    void receive(String event) {}
}`
	evidence := javaKafkaConsumerEvidenceInSource("Events.java", java)
	if len(evidence) != 1 || !evidence[0].Protected {
		t.Fatalf("Spring Kafka evidence = %#v", evidence)
	}
	evidence = javaKafkaConsumerEvidenceInSource("Events.java", `class Events { @KafkaListener void receive() {} }`)
	if len(evidence) != 1 || evidence[0].Protected {
		t.Fatalf("unguarded Spring Kafka evidence = %#v", evidence)
	}

	node := `import { Kafka } from "kafkajs";
if (process.env.SERVICE_KAFKA_CONSUMERS_ENABLED !== "false") {
  await consumer.subscribe({ topic: "events" });
}`
	evidence = nodeKafkaConsumerEvidenceInSource("events.ts", node)
	if len(evidence) != 1 || !evidence[0].Protected {
		t.Fatalf("Node Kafka evidence = %#v", evidence)
	}

	python := `from kafka import KafkaConsumer
if os.getenv("SERVICE_KAFKA_CONSUMERS_ENABLED", "true").lower() != "false":
    consumer = KafkaConsumer("events")
`
	evidence = pythonKafkaConsumerEvidenceInSource("events.py", python)
	if len(evidence) != 1 || !evidence[0].Protected {
		t.Fatalf("Python Kafka evidence = %#v", evidence)
	}
}

func TestKafkaConsumerScanIgnoresNestedRepositoryImplementation(t *testing.T) {
	repository := t.TempDir()
	writeAnalyzerFile(t, filepath.Join(repository, "go", "handler", "events.go"), `package handler
func events() {
    if strings.EqualFold(os.Getenv("SERVICE_KAFKA_CONSUMERS_ENABLED"), "false") {
        return
    }
    kq.NewQueue(config, handler)
}`)
	writeAnalyzerFile(t, filepath.Join(repository, "go", "internal", "go-zero", ".git"), "gitdir: elsewhere\n")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "internal", "go-zero", "kq", "queue.go"), `package kq
func NewQueue() { kafka.NewReader(config) }`)
	evidence, err := InspectKafkaConsumerEvidence(repository, "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].File != "go/handler/events.go:6" || !evidence[0].Protected {
		t.Fatalf("Kafka evidence = %#v", evidence)
	}
}
