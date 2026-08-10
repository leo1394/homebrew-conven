package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func SourceFingerprint(directory string) (string, error) {
	rootCommand := exec.Command("git", "-C", directory, "rev-parse", "--show-toplevel")
	rootOutput, err := rootCommand.Output()
	if err == nil {
		root := string(bytes.TrimSpace(rootOutput))
		arguments := []string{"-C", directory, "ls-files", "-z", "--full-name", "--cached", "--others", "--exclude-standard", "--", "."}
		filesCommand := exec.Command("git", arguments...)
		output, err := filesCommand.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("list service files in Git worktree: %w: %s", err, strings.TrimSpace(string(output)))
		}
		names := make([]string, 0)
		for _, name := range bytes.Split(output, []byte{0}) {
			if len(name) > 0 {
				names = append(names, string(name))
			}
		}
		sort.Strings(names)
		return fingerprintFiles(root, names)
	}
	return fingerprintDirectory(directory)
}

func PlanFingerprint(service PlannedService) (string, error) {
	value := struct {
		Directory   string
		Workdir     string
		RunWorkdir  string
		Artifact    string
		Ports       map[string]int
		Config      *PlannedConfig
		Prepare     []string
		Build       []string
		Run         []string
		Environment []string
		Health      HealthCheck
	}{
		Directory:   service.Directory,
		Workdir:     service.Workdir,
		RunWorkdir:  service.RunWorkdir,
		Artifact:    service.Artifact,
		Ports:       service.Ports,
		Config:      service.Config,
		Prepare:     service.Prepare,
		Build:       service.Build,
		Run:         service.Run,
		Environment: service.Environment,
		Health:      service.Health,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode service plan fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func fingerprintDirectory(directory string) (string, error) {
	names := make([]string, 0)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != directory && entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".conven") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		names = append(names, relative)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan service directory %q: %w", directory, err)
	}
	sort.Strings(names)
	return fingerprintFiles(directory, names)
}

func fingerprintFiles(root string, names []string) (string, error) {
	hash := sha256.New()
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		fmt.Fprintf(hash, "path:%s\n", filepath.ToSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(hash, "missing")
				continue
			}
			return "", fmt.Errorf("inspect source file %q: %w", path, err)
		}
		fmt.Fprintf(hash, "mode:%s\n", info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", fmt.Errorf("read source symlink %q: %w", path, err)
			}
			fmt.Fprintf(hash, "link:%s\n", target)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open source file %q: %w", path, err)
		}
		if _, err := io.Copy(hash, file); err != nil {
			file.Close()
			return "", fmt.Errorf("hash source file %q: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("close source file %q: %w", path, err)
		}
		fmt.Fprintln(hash)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
