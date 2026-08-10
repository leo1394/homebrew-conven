package convenhome

import (
	"path/filepath"
	"testing"
)

func TestRootUsesDefaultUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := Root("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".conven")
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
}

func TestRootUsesExplicitHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "ignored"))

	got, err := Root(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".conven")
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
}
