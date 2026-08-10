package plugins

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/convenhome"
)

//go:embed builtin/*
var builtinFiles embed.FS

func Directory() (string, error) {
	root, err := convenhome.Root("")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "plugins"), nil
}

func InstallBuiltins() error {
	directory, err := preparePluginDirectory()
	if err != nil {
		return err
	}
	entries, err := builtinFiles.ReadDir("builtin")
	if err != nil {
		return fmt.Errorf("read built-in Conven plugins: %w", err)
	}
	for _, entry := range entries {
		filename := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(filename, ".py") {
			continue
		}
		_, normalizedFilename, err := normalizeName(filename)
		if err != nil || normalizedFilename != filename {
			return fmt.Errorf("invalid built-in Conven plugin filename %q", filename)
		}
		content, err := builtinFiles.ReadFile("builtin/" + filename)
		if err != nil {
			return fmt.Errorf("read built-in Conven plugin %q: %w", filename, err)
		}
		if err := validatePythonShebang(bytes.NewReader(content), "built-in plugin "+filename); err != nil {
			return err
		}
		if _, err := installPlugin(directory, filename, bytes.NewReader(content), true); err != nil {
			return err
		}
	}
	return nil
}

func Install(source string) (string, error) {
	if source == "" {
		return "", errors.New("install Conven plugin: source path is empty")
	}
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve Conven plugin source %q: %w", source, err)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("inspect Conven plugin source %q: %w", sourcePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("Conven plugin source %q is a symbolic link; symbolic links are not allowed", sourcePath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Conven plugin source %q is not a regular file", sourcePath)
	}
	filename := filepath.Base(sourcePath)
	if !strings.HasSuffix(filename, ".py") {
		return "", fmt.Errorf("Conven plugin source %q must have a .py extension", sourcePath)
	}
	_, normalizedFilename, err := normalizeName(filename)
	if err != nil {
		return "", fmt.Errorf("invalid Conven plugin filename %q: %w", filename, err)
	}
	if normalizedFilename != filename {
		return "", fmt.Errorf("invalid Conven plugin filename %q", filename)
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open Conven plugin source %q: %w", sourcePath, err)
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect open Conven plugin source %q: %w", sourcePath, err)
	}
	if !os.SameFile(info, openedInfo) {
		return "", fmt.Errorf("Conven plugin source %q changed while being inspected", sourcePath)
	}
	if err := validatePythonShebang(input, sourcePath); err != nil {
		return "", err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind Conven plugin source %q: %w", sourcePath, err)
	}
	directory, err := preparePluginDirectory()
	if err != nil {
		return "", err
	}
	return installPlugin(directory, filename, input, false)
}

func installPlugin(directory string, filename string, input io.Reader, preserveExisting bool) (string, error) {
	destination := filepath.Join(directory, filename)
	if _, err := os.Lstat(destination); err == nil {
		if preserveExisting {
			return destination, nil
		}
		return "", fmt.Errorf("Conven plugin %q is already installed; refusing to overwrite it", destination)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect Conven plugin destination %q: %w", destination, err)
	}
	temporary, err := os.CreateTemp(directory, "."+filename+"-*")
	if err != nil {
		return "", fmt.Errorf("create temporary Conven plugin: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0700); err != nil {
		temporary.Close()
		return "", fmt.Errorf("protect temporary Conven plugin: %w", err)
	}
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return "", fmt.Errorf("copy Conven plugin %q: %w", filename, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync temporary Conven plugin: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary Conven plugin: %w", err)
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if os.IsExist(err) {
			if preserveExisting {
				return destination, nil
			}
			return "", fmt.Errorf("Conven plugin %q is already installed; refusing to overwrite it", destination)
		}
		return "", fmt.Errorf("install Conven plugin %q: %w", destination, err)
	}
	return destination, nil
}

func List() ([]string, error) {
	directory, exists, err := inspectPluginDirectory()
	if err != nil {
		return nil, err
	}
	if !exists {
		return []string{}, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read Conven plugin directory %q: %w", directory, err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".py") {
			continue
		}
		name, _, err := normalizeName(entry.Name())
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect Conven plugin %q: %w", filepath.Join(directory, entry.Name()), err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func Run(ctx context.Context, name string, workspace string, args []string, input io.Reader, output io.Writer, errorOutput io.Writer) error {
	if ctx == nil {
		return errors.New("plugin context is nil")
	}
	name, filename, err := normalizeName(name)
	if err != nil {
		return err
	}
	if err := validateArguments(args); err != nil {
		return err
	}
	workspace, err = canonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	directory, exists, err := inspectPluginDirectory()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Conven plugin directory %q does not exist", directory)
	}
	path := filepath.Join(directory, filename)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("Conven plugin %q is not installed", name)
		}
		return fmt.Errorf("inspect Conven plugin %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Conven plugin %q is a symbolic link; symbolic links are not allowed", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Conven plugin %q is not a regular file", path)
	}
	if info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("Conven plugin %q is not executable", path)
	}
	argv := make([]string, 0, len(args)+2)
	argv = append(argv, "--workspace", workspace)
	argv = append(argv, args...)
	command := exec.CommandContext(ctx, path, argv...)
	command.Dir = workspace
	command.Env = pluginEnvironment(workspace)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = errorOutput
	if err := command.Run(); err != nil {
		return fmt.Errorf("run Conven plugin %q: %w", name, err)
	}
	return nil
}

func preparePluginDirectory() (string, error) {
	directory, err := Directory()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(directory)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", fmt.Errorf("create Conven home %q: %w", root, err)
	}
	if err := protectDirectory(root, "Conven home"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create Conven plugin directory %q: %w", directory, err)
	}
	if err := protectDirectory(directory, "Conven plugin directory"); err != nil {
		return "", err
	}
	return directory, nil
}

func inspectPluginDirectory() (string, bool, error) {
	directory, err := Directory()
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return directory, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect Conven plugin directory %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, fmt.Errorf("Conven plugin directory %q must be a directory; symbolic links are not allowed", directory)
	}
	return directory, true, nil
}

func protectDirectory(path string, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %q: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s %q must be a directory; symbolic links are not allowed", label, path)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("protect %s %q: %w", label, path, err)
	}
	return nil
}

func normalizeName(name string) (string, string, error) {
	if strings.HasSuffix(name, ".py") {
		name = strings.TrimSuffix(name, ".py")
		if strings.HasSuffix(name, ".py") {
			return "", "", fmt.Errorf("invalid plugin name %q", name)
		}
	}
	if name == "" || !asciiAlphaNumeric(name[0]) {
		return "", "", fmt.Errorf("invalid plugin name %q", name)
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if !asciiAlphaNumeric(character) && character != '-' && character != '_' && character != '.' {
			return "", "", fmt.Errorf("invalid plugin name %q", name)
		}
	}
	return name, name + ".py", nil
}

func validatePythonShebang(input io.Reader, source string) error {
	reader := bufio.NewReader(input)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read Conven plugin shebang from %q: %w", source, err)
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !strings.HasPrefix(line, "#!") {
		return fmt.Errorf("Conven plugin source %q must start with a python3 shebang (#!/usr/bin/env python3 or #!/path/to/python3)", source)
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	valid := len(fields) >= 2 && fields[0] == "/usr/bin/env" && fields[1] == "python3"
	if len(fields) >= 1 && filepath.IsAbs(fields[0]) && filepath.Base(fields[0]) == "python3" {
		valid = true
	}
	if !valid {
		return fmt.Errorf("Conven plugin source %q must use a python3 shebang (#!/usr/bin/env python3 or #!/path/to/python3)", source)
	}
	return nil
}

func asciiAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}

func validateArguments(args []string) error {
	for _, argument := range args {
		if argument == "--workspace" || strings.HasPrefix(argument, "--workspace=") {
			return errors.New("plugin argument --workspace is reserved by Conven")
		}
	}
	return nil
}

func canonicalWorkspace(workspace string) (string, error) {
	if workspace == "" {
		return "", errors.New("resolve plugin workspace: path is empty")
	}
	path, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve plugin workspace %q: %w", workspace, err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve plugin workspace %q: %w", workspace, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect plugin workspace %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("plugin workspace %q is not a directory", path)
	}
	return path, nil
}

func pluginEnvironment(workspace string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CONVEN_WORKSPACE=") {
			environment = append(environment, value)
		}
	}
	return append(environment, "CONVEN_WORKSPACE="+workspace)
}
