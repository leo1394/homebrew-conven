package runtime

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBuildDiskSpaceThreshold(t *testing.T) {
	path := "/workspace/.loom/runtime"
	below := minimumBuildDiskSpace - 1
	if err := validateBuildDiskSpace(path, below); err == nil {
		t.Fatal("space below the hard threshold unexpectedly passed")
	} else if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), formatDiskSpace(below)) || !strings.Contains(err.Error(), "Free disk space") {
		t.Fatalf("hard threshold error is not actionable: %v", err)
	}
	if err := validateBuildDiskSpace(path, minimumBuildDiskSpace); err != nil {
		t.Fatalf("exact hard threshold failed: %v", err)
	}
}

func TestBuildDiskSpaceWarningThreshold(t *testing.T) {
	path := "/workspace/.loom/runtime"
	tests := []struct {
		name      string
		available uint64
		want      bool
	}{
		{name: "below hard threshold", available: minimumBuildDiskSpace - 1, want: false},
		{name: "exact hard threshold", available: minimumBuildDiskSpace, want: true},
		{name: "below warning threshold", available: warningBuildDiskSpace - 1, want: true},
		{name: "exact warning threshold", available: warningBuildDiskSpace, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warning := buildDiskSpaceWarning(path, test.available)
			if (warning != "") != test.want {
				t.Fatalf("warning = %q, want present=%v", warning, test.want)
			}
			if warning != "" && (!strings.Contains(warning, path) || !strings.Contains(warning, formatDiskSpace(test.available)) || !strings.Contains(warning, "Free disk space")) {
				t.Fatalf("warning is not actionable: %q", warning)
			}
		})
	}
}

func TestFormatDiskSpace(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{bytes: 512 * 1024 * 1024, want: "512.00 MiB"},
		{bytes: 1536 * 1024 * 1024, want: "1.50 GiB"},
		{bytes: 2 * 1024 * 1024 * 1024, want: "2.00 GiB"},
	}
	for _, test := range tests {
		if got := formatDiskSpace(test.bytes); got != test.want {
			t.Errorf("formatDiskSpace(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestAvailableDiskSpaceUsesFilesystemStatistics(t *testing.T) {
	available, err := availableDiskSpace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if available == 0 {
		t.Fatal("filesystem reported no available space for a fresh temporary directory")
	}
}

func TestCheckBuildDiskSpaceIdentifiesInvalidPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	_, err := checkBuildDiskSpace(path)
	if err == nil {
		t.Fatal("missing disk check path unexpectedly passed")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "check disk space") {
		t.Fatalf("disk check error does not identify the path: %v", err)
	}
}
