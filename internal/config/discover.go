package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type DiscoveredService struct {
	Name      string
	Path      string
	Analyzer  string
	Kind      string
	Bindings  []string
	Runner    model.Runner
}

type DiscoveryResult struct {
	Discovered []string
	Added      []string
	Existing   []string
	Updated    []string
	Missing    []string
	Pruned     []string
	Skipped    []string
}

type discoveredManifest struct {
	Version   int                          `yaml:"version"`
	Workspace discoveredManifestWorkspace `yaml:"workspace"`
	Services  map[string]discoveredEntry   `yaml:"services"`
}

type discoveredManifestWorkspace struct {
	Name string `yaml:"name"`
}

type discoveredEntry struct {
	Path      string              `yaml:"path"`
	Kind      string              `yaml:"kind,omitempty"`
	Discovery discoveredDiscovery `yaml:"discovery"`
	Runner    discoveredRunner    `yaml:"runner"`
}

type discoveredDiscovery struct {
	Analyzer string   `yaml:"analyzer"`
	Bindings []string `yaml:"bindings,omitempty"`
}

type discoveredRunner struct {
	Workdir string   `yaml:"workdir"`
	Build   []string `yaml:"build"`
	Run     []string `yaml:"run"`
}

func ScanServices(workspace string) ([]DiscoveredService, []string, error) {
	workspace, err := resolveCwd(workspace)
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil, nil, fmt.Errorf("scan workspace %q: %w", workspace, err)
	}

	discovered := make([]DiscoveredService, 0)
	skipped := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.IsDir() {
			continue
		}
		repository := filepath.Join(workspace, name)
		isRepository, err := pathExists(filepath.Join(repository, ".git"))
		if err != nil {
			return nil, nil, fmt.Errorf("inspect repository marker for %q: %w", name, err)
		}
		if !isRepository {
			continue
		}
		analysis, supported, err := AnalyzeRepository(repository)
		if err != nil {
			return nil, nil, err
		}
		if !supported {
			skipped = append(skipped, name)
			continue
		}
		kind := analysis.Kind
		if kind == RepositoryKindUnknown {
			kind = ""
		}
		bindings := make([]string, 0, len(analysis.RPCClientBindings))
		seenBindings := make(map[string]bool, len(analysis.RPCClientBindings))
		for _, binding := range analysis.RPCClientBindings {
			if seenBindings[binding.YAMLKey] {
				continue
			}
			seenBindings[binding.YAMLKey] = true
			bindings = append(bindings, binding.YAMLKey)
		}
		sort.Strings(bindings)
		discovered = append(discovered, DiscoveredService{
			Name:     analysis.ServiceName,
			Path:     name,
			Analyzer: analysis.Analyzer,
			Kind:     kind,
			Bindings: bindings,
			Runner:   analysis.Runner,
		})
	}
	sort.Slice(discovered, func(left int, right int) bool {
		return discovered[left].Name < discovered[right].Name
	})
	sort.Strings(skipped)
	return discovered, skipped, nil
}

func RenderDiscoveredManifest(workspace string, services []DiscoveredService) ([]byte, error) {
	workspace, err := resolveCwd(workspace)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]discoveredEntry, len(services))
	for _, service := range services {
		entries[service.Name] = discoveredEntry{
			Path: service.Path,
			Kind: service.Kind,
			Discovery: discoveredDiscovery{
				Analyzer: service.Analyzer,
				Bindings: append([]string(nil), service.Bindings...),
			},
			Runner: discoveredRunner{
				Workdir: service.Runner.Workdir,
				Build:   append([]string(nil), service.Runner.Build...),
				Run:     append([]string(nil), service.Runner.Run...),
			},
		}
	}
	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	err = encoder.Encode(discoveredManifest{
		Version: 1,
		Workspace: discoveredManifestWorkspace{
			Name: filepath.Base(workspace),
		},
		Services: entries,
	})
	if err != nil {
		return nil, fmt.Errorf("encode discovered manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close discovered manifest encoder: %w", err)
	}
	return data.Bytes(), nil
}

func DiscoverWorkspace(manifestPath string, workspace string, prune bool) (DiscoveryResult, error) {
	result := DiscoveryResult{}
	source, sourceInfo, err := readManifestForUpdate(manifestPath)
	if err != nil {
		return result, err
	}
	manifest, err := decodeManifest(source, manifestPath)
	if err != nil {
		return result, err
	}
	discovered, skipped, err := ScanServices(workspace)
	if err != nil {
		return result, err
	}
	result.Skipped = skipped
	for _, service := range discovered {
		result.Discovered = append(result.Discovered, service.Name)
	}

	existingByName := manifest.Services
	matched := make(map[string]bool)
	additions := make([]DiscoveredService, 0)
	updates := make(map[string]model.Service)
	for _, service := range discovered {
		repository := filepath.Join(workspace, service.Path)
		matches := make([]string, 0)
		for _, name := range ServiceNames(manifest) {
			if servicePathMatchesRepository(workspace, existingByName[name].Path, repository) {
				matches = append(matches, name)
			}
		}
		if len(matches) > 1 {
			return result, fmt.Errorf("discovered repository %q is already declared by multiple services: %s", service.Path, strings.Join(matches, ", "))
		}
		if existing, found := existingByName[service.Name]; found && !servicePathMatchesRepository(workspace, existing.Path, repository) {
			return result, fmt.Errorf("discovered service %q at %q conflicts with existing path %q", service.Name, service.Path, existing.Path)
		}
		if len(matches) == 1 {
			existingName := matches[0]
			matched[existingName] = true
			result.Existing = append(result.Existing, existingName)
			existing := existingByName[existingName]
			updated, changed := backfillDiscoveredDescription(existing, service)
			if changed {
				updates[existingName] = updated
				result.Updated = append(result.Updated, existingName)
			}
			continue
		}
		additions = append(additions, service)
		result.Added = append(result.Added, service.Name)
	}

	services := make(map[string]model.Service, len(manifest.Services)+len(additions))
	for name, service := range manifest.Services {
		services[name] = service
	}
	for name, service := range updates {
		services[name] = service
	}
	for _, service := range additions {
		services[service.Name] = discoveredModelService(service)
	}
	for _, name := range ServiceNames(manifest) {
		if matched[name] || !isMissingDirectChildServicePath(workspace, manifest.Services[name].Path) {
			continue
		}
		if prune {
			delete(services, name)
			result.Pruned = append(result.Pruned, name)
		} else {
			result.Missing = append(result.Missing, name)
		}
	}
	sort.Strings(result.Added)
	sort.Strings(result.Existing)
	sort.Strings(result.Updated)
	sort.Strings(result.Missing)
	sort.Strings(result.Pruned)

	candidate := *manifest
	candidate.Services = services
	if err := validateManifest(&candidate); err != nil {
		return result, fmt.Errorf("validate discovered manifest: %w", err)
	}
	if len(result.Added) == 0 && len(result.Updated) == 0 && len(result.Pruned) == 0 {
		if err := verifyManifestSnapshot(manifestPath, source, sourceInfo, "discovery"); err != nil {
			return result, err
		}
		return result, nil
	}

	document, serviceMapping, err := loadManifestDocument(source, manifestPath)
	if err != nil {
		return result, err
	}
	if len(result.Pruned) > 0 {
		removeMappingEntries(serviceMapping, result.Pruned)
	}
	for _, name := range result.Updated {
		value := mappingValue(serviceMapping, name)
		if value == nil || value.Kind != yaml.MappingNode {
			return result, fmt.Errorf("Conven manifest %q service %q must be a mapping", manifestPath, name)
		}
		appendDiscoveredDescription(value, updates[name])
	}
	for _, service := range additions {
		value, err := discoveredServiceNode(service)
		if err != nil {
			return result, err
		}
		serviceMapping.Content = append(serviceMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service.Name},
			value,
		)
	}
	if err := saveManifestDocument(manifestPath, document, source, sourceInfo, &candidate); err != nil {
		return result, err
	}
	return result, nil
}

func discoveredModelService(service DiscoveredService) model.Service {
	return model.Service{
		Path: service.Path,
		Kind: service.Kind,
		Discovery: model.ServiceDiscovery{
			Analyzer: service.Analyzer,
			Bindings: append([]string(nil), service.Bindings...),
		},
		Runner: service.Runner,
	}
}

func backfillDiscoveredDescription(existing model.Service, discovered DiscoveredService) (model.Service, bool) {
	changed := false
	if existing.Kind == "" && discovered.Kind != "" {
		existing.Kind = discovered.Kind
		changed = true
	}
	if existing.Discovery.Analyzer == "" {
		existing.Discovery = model.ServiceDiscovery{
			Analyzer: discovered.Analyzer,
			Bindings: append([]string(nil), discovered.Bindings...),
		}
		changed = true
	}
	return existing, changed
}

func appendDiscoveredDescription(mapping *yaml.Node, service model.Service) {
	if mappingValue(mapping, "kind") == nil && service.Kind != "" {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "kind"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service.Kind},
		)
	}
	if mappingValue(mapping, "discovery") == nil && service.Discovery.Analyzer != "" {
		value := &yaml.Node{}
		value.Encode(discoveredDiscovery{
			Analyzer: service.Discovery.Analyzer,
			Bindings: append([]string(nil), service.Discovery.Bindings...),
		})
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "discovery"},
			value,
		)
	}
}

func moduleName(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open Go module %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"'"), true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("read Go module %q: %w", path, err)
	}
	return "", false, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func servicePathMatchesRepository(workspace string, servicePath string, repository string) bool {
	resolved := servicePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspace, resolved)
	}
	resolved = filepath.Clean(resolved)
	if resolved == filepath.Clean(repository) {
		return true
	}
	return sameDirectory(resolved, repository)
}

func isMissingDirectChildServicePath(workspace string, servicePath string) bool {
	resolved := servicePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspace, resolved)
	}
	relative, err := filepath.Rel(workspace, filepath.Clean(resolved))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	if strings.ContainsRune(relative, filepath.Separator) {
		return false
	}
	info, err := os.Stat(resolved)
	return os.IsNotExist(err) || err == nil && !info.IsDir()
}

func readManifestForUpdate(path string) ([]byte, os.FileInfo, error) {
	observed, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect Conven manifest %q: %w", path, err)
	}
	if observed.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("Conven manifest %q is a symbolic link; Conven refuses to replace symbolic links", path)
	}
	if !observed.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("Conven manifest %q is not a regular file", path)
	}
	file, err := openManifestNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("inspect opened Conven manifest %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(observed, info) {
		return nil, nil, fmt.Errorf("Conven manifest %q changed while it was opened; retry the command", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, fmt.Errorf("read Conven manifest %q: %w", path, err)
	}
	return data, info, nil
}

func loadManifestDocument(data []byte, path string) (*yaml.Node, *yaml.Node, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("Conven manifest %q is empty", path)
	}
	document := &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return nil, nil, fmt.Errorf("decode Conven manifest %q for discovery: %w", path, err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("Conven manifest %q must contain one mapping document", path)
	}
	services := mappingValue(document.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("Conven manifest %q services must be a mapping", path)
	}
	return document, services, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func removeMappingEntries(mapping *yaml.Node, names []string) {
	remove := make(map[string]bool, len(names))
	for _, name := range names {
		remove[name] = true
	}
	content := make([]*yaml.Node, 0, len(mapping.Content))
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if remove[mapping.Content[index].Value] {
			continue
		}
		content = append(content, mapping.Content[index], mapping.Content[index+1])
	}
	mapping.Content = content
}

func discoveredServiceNode(service DiscoveredService) (*yaml.Node, error) {
	value := &yaml.Node{}
	err := value.Encode(discoveredEntry{
		Path: service.Path,
		Kind: service.Kind,
		Discovery: discoveredDiscovery{
			Analyzer: service.Analyzer,
			Bindings: append([]string(nil), service.Bindings...),
		},
		Runner: discoveredRunner{
			Workdir: service.Runner.Workdir,
			Build:   append([]string(nil), service.Runner.Build...),
			Run:     append([]string(nil), service.Runner.Run...),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode discovered service %q: %w", service.Name, err)
	}
	return value, nil
}

func saveManifestDocument(path string, document *yaml.Node, source []byte, sourceInfo os.FileInfo, expected *model.Manifest) error {
	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		encoder.Close()
		return fmt.Errorf("encode Conven manifest %q: %w", path, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close Conven manifest encoder: %w", err)
	}
	updated, err := decodeManifest(data.Bytes(), path)
	if err != nil {
		return fmt.Errorf("validate updated Conven manifest: %w", err)
	}
	if !reflect.DeepEqual(updated, expected) {
		return fmt.Errorf("validate updated Conven manifest: encoded manifest does not match the validated discovery result")
	}
	return publishManifestUpdate(path, data.Bytes(), source, sourceInfo, "discovery")
}

func publishManifestUpdate(path string, data []byte, source []byte, sourceInfo os.FileInfo, operation string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".loom-manifest-*")
	if err != nil {
		return fmt.Errorf("create temporary Conven manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(sourceInfo.Mode().Perm()); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary Conven manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary Conven manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary Conven manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Conven manifest: %w", err)
	}
	locked, err := lockManifestSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	defer unlockManifest(locked)
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish Conven manifest %q: %w", path, err)
	}
	_ = syncDirectory(directory)
	return nil
}

func verifyManifestSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) error {
	locked, err := lockManifestSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	return unlockManifest(locked)
}

func lockManifestSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) (*os.File, error) {
	file, err := openManifestNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("reopen Conven manifest before %s: %w", operation, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("Conven manifest %q is being updated by another Conven process; retry the command", path)
		}
		return nil, fmt.Errorf("lock Conven manifest %q before %s: %w", path, operation, err)
	}
	info, err := file.Stat()
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("inspect locked Conven manifest %q: %w", path, err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reinspect Conven manifest %q before %s: %w", path, operation, err)
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(sourceInfo, info) || !os.SameFile(info, currentInfo) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven manifest %q changed during %s; retry the command", path, operation)
	}
	current, err := io.ReadAll(file)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reread locked Conven manifest %q before %s: %w", path, operation, err)
	}
	if !bytes.Equal(current, source) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven manifest %q was edited during %s; retry the command", path, operation)
	}
	return file, nil
}

func openManifestNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Conven manifest %q without following symbolic links: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func unlockManifest(file *os.File) error {
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock Conven manifest %q: %w", file.Name(), unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close locked Conven manifest %q: %w", file.Name(), closeErr)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q for sync: %w", path, err)
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close directory %q after sync: %w", path, err)
	}
	return nil
}
