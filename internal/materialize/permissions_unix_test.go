//go:build darwin || linux

package materialize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMaterializeSetsPrivateModesUnderRestrictiveUmask(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.RuntimeBootstrap = "generated/runtime/config-local.yaml"
	oldUmask := unix.Umask(0777)
	defer unix.Umask(oldUmask)
	err := Materialize(context.Background(), plan)
	unix.Umask(oldUmask)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateTree(t, target)
}

func TestMaterializeRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := unix.Mkfifo(filepath.Join(source, "blocked.pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- Materialize(context.Background(), testPlan(source, configRoot, target))
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("materialization blocked while inspecting a FIFO")
	}
}
