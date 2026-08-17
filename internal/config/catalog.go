package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const maxCatalogSize = 1 << 20

type Catalog struct {
	Version             int              `yaml:"version"`
	Services            []CatalogService `yaml:"services"`
	DisabledRPCBindings []string         `yaml:"disabledRpcBindings"`
}

type CatalogService struct {
	Repository string `yaml:"repository"`
	RPCBinding string `yaml:"rpcBinding"`
	Kind       string `yaml:"kind"`
	Port       int    `yaml:"port"`
}

type CatalogEditResult struct {
	Path      string
	Changed   bool
	DraftPath string
}

func CatalogPath(workspace string) string {
	return filepath.Join(workspace, ".conven", "catalog.yaml")
}

func LoadCatalog(path string) (*Catalog, error) {
	data, _, err := readCatalogForUpdate(path)
	if err != nil {
		return nil, err
	}
	return decodeCatalog(data, path)
}

func LoadWorkspaceCatalog(cwd string) (*Catalog, string, error) {
	workspace, err := FindWorkspace(cwd)
	if err != nil {
		return nil, "", err
	}
	path := CatalogPath(workspace)
	catalog, err := LoadCatalog(path)
	if err != nil {
		return nil, path, err
	}
	return catalog, path, nil
}

func decodeCatalog(data []byte, path string) (*Catalog, error) {
	catalog := &Catalog{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(catalog); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("Conven catalog %q is empty", path)
		}
		return nil, fmt.Errorf("decode Conven catalog %q: %w", path, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode Conven catalog %q: %w", path, err)
		}
		return nil, fmt.Errorf("Conven catalog %q contains multiple YAML documents", path)
	}
	if err := validateCatalog(catalog); err != nil {
		return nil, fmt.Errorf("validate Conven catalog %q: %w", path, err)
	}
	return catalog, nil
}

func validateCatalog(catalog *Catalog) error {
	if catalog.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", catalog.Version)
	}
	repositories := make(map[string]int)
	bindings := make(map[string]int)
	ports := make(map[int]int)
	for index, service := range catalog.Services {
		field := fmt.Sprintf("services[%d]", index)
		repository := strings.TrimSpace(service.Repository)
		binding := strings.TrimSpace(service.RPCBinding)
		if repository == "" && binding == "" {
			return fmt.Errorf("%s must declare repository, rpcBinding, or both", field)
		}
		if service.Repository != repository || repository != "" && !validCatalogRepository(repository) {
			return fmt.Errorf("%s.repository must start with a letter or digit and contain only letters, digits, '.', '_' or '-'", field)
		}
		if service.RPCBinding != binding || binding != "" && !validCatalogBinding(binding) {
			return fmt.Errorf("%s.rpcBinding must start with a letter or '_' and contain only letters, digits, '_' or '-'", field)
		}
		if service.Kind != "http" && service.Kind != "rpc" {
			return fmt.Errorf("%s.kind must be http or rpc, got %q", field, service.Kind)
		}
		if binding != "" && service.Kind != "rpc" {
			return fmt.Errorf("%s.kind must be rpc when rpcBinding is declared", field)
		}
		if service.Port < 1 || service.Port > 65535 {
			return fmt.Errorf("%s.port must be between 1 and 65535, got %d", field, service.Port)
		}
		if previous, found := repositories[repository]; repository != "" && found {
			return fmt.Errorf("%s.repository duplicates services[%d].repository %q", field, previous, repository)
		}
		if previous, found := bindings[binding]; binding != "" && found {
			return fmt.Errorf("%s.rpcBinding duplicates services[%d].rpcBinding %q", field, previous, binding)
		}
		if previous, found := ports[service.Port]; found {
			return fmt.Errorf("%s.port duplicates services[%d].port %d", field, previous, service.Port)
		}
		if repository != "" {
			repositories[repository] = index
		}
		if binding != "" {
			bindings[binding] = index
		}
		ports[service.Port] = index
	}
	disabled := make(map[string]int, len(catalog.DisabledRPCBindings))
	for index, binding := range catalog.DisabledRPCBindings {
		field := fmt.Sprintf("disabledRpcBindings[%d]", index)
		trimmed := strings.TrimSpace(binding)
		if binding != trimmed || !validCatalogBinding(trimmed) {
			return fmt.Errorf("%s must start with a letter or '_' and contain only letters, digits, '_' or '-'", field)
		}
		if previous, found := disabled[binding]; found {
			return fmt.Errorf("%s duplicates disabledRpcBindings[%d] %q", field, previous, binding)
		}
		disabled[binding] = index
	}
	return nil
}

func validCatalogRepository(value string) bool {
	for index, character := range value {
		if index == 0 && !asciiLetterOrDigit(character) {
			return false
		}
		if !asciiLetterOrDigit(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return value != ""
}

func validCatalogBinding(value string) bool {
	for index, character := range value {
		if index == 0 && !asciiLetter(character) && character != '_' {
			return false
		}
		if !asciiLetterOrDigit(character) && character != '_' && character != '-' {
			return false
		}
	}
	return value != ""
}

func asciiLetter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func asciiLetterOrDigit(character rune) bool {
	return asciiLetter(character) || character >= '0' && character <= '9'
}

func EditWorkspaceCatalog(cwd string, edit func(string) error) (CatalogEditResult, error) {
	result := CatalogEditResult{}
	workspace, boundary, err := policyWorkspace(cwd)
	if err != nil {
		return result, err
	}
	path := CatalogPath(workspace)
	result.Path = path
	source, sourceInfo, err := readCatalogForUpdate(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("Conven catalog %q is missing; run \"conven init\"", path)
		}
		return result, err
	}
	if edit == nil {
		return result, fmt.Errorf("catalog editor is not configured")
	}
	directory, err := ensurePolicyBackupDirectory(boundary)
	if err != nil {
		return result, err
	}
	draft, err := os.CreateTemp(directory, "catalog-edit-*.yaml")
	if err != nil {
		return result, fmt.Errorf("create catalog edit draft: %w", err)
	}
	draftPath := draft.Name()
	removeDraft := true
	defer func() {
		if removeDraft {
			_ = os.Remove(draftPath)
		}
	}()
	if err := draft.Chmod(0600); err != nil {
		draft.Close()
		return result, fmt.Errorf("protect catalog edit draft: %w", err)
	}
	if _, err := draft.Write(source); err != nil {
		draft.Close()
		return result, fmt.Errorf("write catalog edit draft: %w", err)
	}
	if err := draft.Sync(); err != nil {
		draft.Close()
		return result, fmt.Errorf("sync catalog edit draft: %w", err)
	}
	if err := draft.Close(); err != nil {
		return result, fmt.Errorf("close catalog edit draft: %w", err)
	}

	editErr := edit(draftPath)
	candidate, _, readErr := readCatalogForUpdate(draftPath)
	if readErr != nil {
		if editErr != nil {
			return result, fmt.Errorf("catalog editor failed: %v; inspect edited draft: %w", editErr, readErr)
		}
		return result, readErr
	}
	if editErr != nil {
		if bytes.Equal(candidate, source) {
			return result, fmt.Errorf("catalog editor failed: %w", editErr)
		}
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("catalog editor failed: %w; edited draft was not published by Conven and is kept at %q", editErr, draftPath)
	}
	if _, err := decodeCatalog(candidate, draftPath); err != nil {
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("edited catalog is invalid: %w; edited draft was not published by Conven and is kept at %q", err, draftPath)
	}
	if bytes.Equal(candidate, source) {
		if err := verifyCatalogSnapshot(path, source, sourceInfo, "catalog edit"); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := publishCatalogUpdate(path, candidate, source, sourceInfo, "catalog edit"); err != nil {
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("%w; edited draft was not published by Conven and is kept at %q", err, draftPath)
	}
	result.Changed = true
	return result, nil
}

func readCatalogForUpdate(path string) ([]byte, os.FileInfo, error) {
	observed, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect Conven catalog %q: %w", path, err)
	}
	if observed.Mode()&os.ModeSymlink != 0 || !observed.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("Conven catalog %q must be a real regular file, not a symbolic link", path)
	}
	file, err := openCatalogNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("inspect opened Conven catalog %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(observed, info) {
		return nil, nil, fmt.Errorf("Conven catalog %q changed while it was opened; retry the command", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read Conven catalog %q: %w", path, err)
	}
	if len(data) > maxCatalogSize {
		return nil, nil, fmt.Errorf("Conven catalog %q exceeds the %d-byte size limit", path, maxCatalogSize)
	}
	return data, info, nil
}

func publishCatalogUpdate(path string, data []byte, source []byte, sourceInfo os.FileInfo, operation string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".conven-catalog-*")
	if err != nil {
		return fmt.Errorf("create temporary Conven catalog: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(sourceInfo.Mode().Perm()); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary Conven catalog: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary Conven catalog: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary Conven catalog: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Conven catalog: %w", err)
	}
	locked, err := lockCatalogSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	defer unlockManifest(locked)
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish Conven catalog %q: %w", path, err)
	}
	_ = syncDirectory(directory)
	return nil
}

func verifyCatalogSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) error {
	locked, err := lockCatalogSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	return unlockManifest(locked)
}

func lockCatalogSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) (*os.File, error) {
	file, err := openCatalogNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("reopen Conven catalog before %s: %w", operation, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("Conven catalog %q is being updated by another Conven process; retry the command", path)
		}
		return nil, fmt.Errorf("lock Conven catalog %q before %s: %w", path, operation, err)
	}
	info, err := file.Stat()
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("inspect locked Conven catalog %q: %w", path, err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reinspect Conven catalog %q before %s: %w", path, operation, err)
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(sourceInfo, info) || !os.SameFile(info, currentInfo) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven catalog %q changed during %s; retry the command", path, operation)
	}
	current, err := io.ReadAll(file)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reread locked Conven catalog %q before %s: %w", path, operation, err)
	}
	if !bytes.Equal(current, source) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven catalog %q was edited during %s; retry the command", path, operation)
	}
	return file, nil
}

func openCatalogNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Conven catalog %q without following symbolic links: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
