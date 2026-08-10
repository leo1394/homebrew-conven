package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func TestSourceFingerprintTracksGitContentAndIgnoresIgnoredFiles(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "service.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".gitignore", "service.txt")
	first, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "ignored.txt"), []byte("generated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != first {
		t.Fatal("ignored file changed the source fingerprint")
	}
	if err := os.WriteFile(filepath.Join(repository, "service.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("tracked content change did not change the source fingerprint")
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if third == second {
		t.Fatal("untracked content did not change the source fingerprint")
	}
}

func TestSourceFingerprintFallsBackOutsideGit(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := SourceFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLog := filepath.Join(directory, ".loom", "runtime", "current", "logs", "api.log")
	if err := os.MkdirAll(filepath.Dir(runtimeLog), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeLog, []byte("first runtime log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored, err := SourceFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != first {
		t.Fatal("workspace runtime changed the filesystem source fingerprint")
	}
	if err := os.WriteFile(runtimeLog, []byte("updated runtime log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored, err = SourceFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != first {
		t.Fatal("workspace runtime log update changed the filesystem source fingerprint")
	}
	if err := os.WriteFile(path, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := SourceFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("filesystem content change did not change the source fingerprint")
	}
}

func TestSourceFingerprintIgnoresWorkspaceRuntimeInGitRepository(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init")
	boundary := filepath.Join(repository, ".loom")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundary, ".gitignore"), []byte("/runtime/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "service.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".loom/.gitignore", "service.txt")
	first, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLog := filepath.Join(boundary, "runtime", "current", "logs", "api.log")
	if err := os.MkdirAll(filepath.Dir(runtimeLog), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeLog, []byte("first runtime log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	withRuntime, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if withRuntime != first {
		t.Fatal("workspace runtime changed the Git source fingerprint")
	}
	if err := os.WriteFile(runtimeLog, []byte("updated runtime log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updatedRuntime, err := SourceFingerprint(repository)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRuntime != first {
		t.Fatal("workspace runtime log update changed the Git source fingerprint")
	}
}

func TestSourceFingerprintScopesGitWorktreeToServiceDirectory(t *testing.T) {
	repository := t.TempDir()
	service := filepath.Join(repository, "services", "api")
	if err := os.MkdirAll(service, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init")
	servicePath := filepath.Join(service, "source.txt")
	outsidePath := filepath.Join(repository, "outside.txt")
	if err := os.WriteFile(servicePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsidePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".")
	first, err := SourceFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsidePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideChanged, err := SourceFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if outsideChanged != first {
		t.Fatal("file outside the service changed its fingerprint")
	}
	if err := os.WriteFile(servicePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	serviceChanged, err := SourceFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if serviceChanged == first {
		t.Fatal("service file change did not change its fingerprint")
	}
}

func TestPlanFingerprintTracksPorts(t *testing.T) {
	service := PlannedService{
		Name:  "api",
		Ports: map[string]int{"http": 8080},
	}
	first, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	service.Ports["http"] = 8181
	second, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("port change did not change the plan fingerprint")
	}
}

func TestPlanFingerprintTracksRunWorkdir(t *testing.T) {
	service := PlannedService{Name: "api", Workdir: "/source", RunWorkdir: "/runtime/one"}
	first, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	service.RunWorkdir = "/runtime/two"
	second, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("run workdir change did not change the plan fingerprint")
	}
}

func TestPlanFingerprintTracksResolvedConfigPolicy(t *testing.T) {
	service := PlannedService{
		Name: "api",
		Config: &PlannedConfig{
			Policy: "retail",
			Plan: materialize.Plan{
				Service:      "api",
				Driver:       materialize.DriverYAMLOverlay,
				SourceDriver: materialize.SourceRepository,
				SourceDir:    "/workspace/api/resources",
				ConfigRoot:   "/workspace/.loom/runtime/current/configs",
				TargetDir:    "/workspace/.loom/runtime/current/configs/api",
				Application:  "application.yaml",
				Patches: []materialize.Patch{
					{File: "application.yaml", Path: "port", Value: 18080},
				},
			},
		},
	}
	first, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	service.Config.Plan.Patches[0].Value = 18081
	second, err := PlanFingerprint(service)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("resolved config policy change did not change the plan fingerprint")
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
