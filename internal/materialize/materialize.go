package materialize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Driver string

const (
	DriverYAMLOverlay       Driver = "yaml-overlay"
	DriverPropertiesOverlay Driver = "properties-overlay"
	DriverEnvironment       Driver = "environment"
)

type SourceDriver string

const (
	SourceRepository SourceDriver = "repository"
	SourceApollo     SourceDriver = "apollo"
	SourceEnvironment SourceDriver = "environment"
)

type Apollo struct {
	Attempts   int
	RetryDelay time.Duration
	Timeout    time.Duration
}

type Patch struct {
	File      string
	Path      string
	Value     any
	IfPresent bool
}

type Guard struct {
	File        string
	Path        string
	Value       any
	AllowCreate bool
}

type Plan struct {
	Service          string
	Driver           Driver
	SourceDriver     SourceDriver
	SourceDir        string
	ConfigRoot       string
	TargetDir        string
	Application      string
	Bootstrap        string
	RuntimeBootstrap string
	Apollo           Apollo
	Patches          []Patch
	Guards           []Guard
}

func Materialize(ctx context.Context, plan Plan) error {
	validated, err := validatePlan(plan)
	if err != nil {
		return err
	}
	if err := ensureAtomicPublicationSupported(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(validated.ConfigRoot, "."+filepath.Base(validated.TargetDir)+".staging-")
	if err != nil {
		return fmt.Errorf("create materialization staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0700); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("protect materialization staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copySource(ctx, validated.SourceDir, staging); err != nil {
		return err
	}
	applicationPath, err := secureJoin(staging, validated.Application)
	if err != nil {
		return fmt.Errorf("resolve application: %w", err)
	}
	application, err := os.ReadFile(applicationPath)
	if err != nil && !(validated.SourceDriver == SourceApollo && os.IsNotExist(err)) {
		return fmt.Errorf("read application source: %w", err)
	}
	var bootstrap []byte
	if validated.Bootstrap != "" {
		bootstrapPath, err := secureJoin(staging, validated.Bootstrap)
		if err != nil {
			return fmt.Errorf("resolve bootstrap: %w", err)
		}
		bootstrap, err = os.ReadFile(bootstrapPath)
		if err != nil {
			return fmt.Errorf("read bootstrap source: %w", err)
		}
		if validated.RuntimeBootstrap != "" && validated.Bootstrap != validated.RuntimeBootstrap {
			runtimeBootstrapPath, err := secureJoin(staging, validated.RuntimeBootstrap)
			if err != nil {
				return fmt.Errorf("resolve runtime bootstrap: %w", err)
			}
			if err := ensurePrivateDirectory(staging, filepath.Dir(runtimeBootstrapPath)); err != nil {
				return fmt.Errorf("create runtime bootstrap directory: %w", err)
			}
			if err := writePrivateFile(runtimeBootstrapPath, bootstrap); err != nil {
				return fmt.Errorf("create runtime bootstrap: %w", err)
			}
		}
	}
	adapter, err := adapterFor(validated.SourceDriver)
	if err != nil {
		return err
	}
	application, err = adapter.Application(ctx, SourceInput{
		Application: application,
		Bootstrap:   bootstrap,
		Apollo:      validated.Apollo,
	})
	if err != nil {
		return fmt.Errorf("materialize %s application: %w", validated.SourceDriver, err)
	}
	if err := ensurePrivateDirectory(staging, filepath.Dir(applicationPath)); err != nil {
		return fmt.Errorf("create application directory: %w", err)
	}
	if err := writePrivateFile(applicationPath, application); err != nil {
		return fmt.Errorf("write materialized application: %w", err)
	}
	for index, guard := range validated.Guards {
		if guard.AllowCreate {
			continue
		}
		path, err := secureGuardPath(staging, guard.File)
		if err != nil {
			return fmt.Errorf("resolve guard %d file: %w", index, err)
		}
		if err := requireExistingGuard(validated.Driver, path, guard.Path); err != nil {
			return fmt.Errorf("inspect guard %d target in %s: %w", index, guard.File, err)
		}
	}
	for index, patch := range validated.Patches {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := secureJoin(staging, patch.File)
		if err != nil {
			return fmt.Errorf("resolve patch %d file: %w", index, err)
		}
		if patch.IfPresent {
			found, err := configPathExists(validated.Driver, path, patch.Path)
			if err != nil {
				return fmt.Errorf("inspect optional patch %d target in %s: %w", index, patch.File, err)
			}
			if !found {
				continue
			}
		}
		if err := applyConfigPatch(validated.Driver, path, patch.Path, patch.Value); err != nil {
			return fmt.Errorf("apply patch %d to %s: %w", index, patch.File, err)
		}
	}
	for index, guard := range validated.Guards {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := secureGuardPath(staging, guard.File)
		if err != nil {
			return fmt.Errorf("resolve guard %d file: %w", index, err)
		}
		if err := applyConfigGuard(validated.Driver, path, guard.Path, guard.Value, guard.AllowCreate); err != nil {
			return fmt.Errorf("apply guard %d to %s: %w", index, guard.File, err)
		}
	}
	if err := VerifyPlanGuards(staging, validated.Driver, validated.Guards); err != nil {
		return err
	}
	if err := protectTree(staging); err != nil {
		return err
	}
	if validated.Driver == DriverYAMLOverlay {
		if err := validateYAMLTree(staging); err != nil {
			return err
		}
	}
	if err := publishDirectory(staging, validated.TargetDir); err != nil {
		return err
	}
	return nil
}

func validatePlan(plan Plan) (Plan, error) {
	if strings.TrimSpace(plan.Service) == "" {
		return Plan{}, errors.New("materialization service is empty")
	}
	if plan.Driver != DriverYAMLOverlay && plan.Driver != DriverPropertiesOverlay {
		return Plan{}, fmt.Errorf("unsupported materialization driver %q", plan.Driver)
	}
	if plan.SourceDriver != SourceRepository && plan.SourceDriver != SourceApollo {
		return Plan{}, fmt.Errorf("unsupported config source driver %q", plan.SourceDriver)
	}
	for name, value := range map[string]string{
		"SourceDir": plan.SourceDir,
		"ConfigRoot": plan.ConfigRoot,
		"TargetDir": plan.TargetDir,
	} {
		if !filepath.IsAbs(value) {
			return Plan{}, fmt.Errorf("%s must be absolute", name)
		}
	}
	source := filepath.Clean(plan.SourceDir)
	configRoot := filepath.Clean(plan.ConfigRoot)
	target := filepath.Clean(plan.TargetDir)
	if err := requireRealDirectory(source, "source directory"); err != nil {
		return Plan{}, err
	}
	if err := requireRealDirectory(configRoot, "config root"); err != nil {
		return Plan{}, err
	}
	if filepath.Dir(target) != configRoot || filepath.Base(target) == "." || filepath.Base(target) == string(filepath.Separator) {
		return Plan{}, fmt.Errorf("target directory %q must be a direct child of config root %q", target, configRoot)
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return Plan{}, fmt.Errorf("canonicalize source directory: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(configRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("canonicalize config root: %w", err)
	}
	source = filepath.Clean(canonicalSource)
	configRoot = filepath.Clean(canonicalRoot)
	target = filepath.Join(configRoot, filepath.Base(target))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Plan{}, fmt.Errorf("target directory %q must be a real directory", target)
		}
	} else if !os.IsNotExist(err) {
		return Plan{}, fmt.Errorf("inspect target directory: %w", err)
	}
	if withinDirectory(source, target) || withinDirectory(target, source) {
		return Plan{}, fmt.Errorf("source directory %q and target directory %q must not overlap", source, target)
	}
	if withinDirectory(source, configRoot) {
		return Plan{}, fmt.Errorf("config root %q must not be inside source directory %q", configRoot, source)
	}
	if err := validateRelativeFile(plan.Application); err != nil {
		return Plan{}, fmt.Errorf("Application: %w", err)
	}
	if plan.Bootstrap != "" {
		if err := validateRelativeFile(plan.Bootstrap); err != nil {
			return Plan{}, fmt.Errorf("Bootstrap: %w", err)
		}
	}
	if plan.RuntimeBootstrap != "" {
		if plan.Bootstrap == "" {
			return Plan{}, errors.New("RuntimeBootstrap requires Bootstrap")
		}
		if err := validateRelativeFile(plan.RuntimeBootstrap); err != nil {
			return Plan{}, fmt.Errorf("RuntimeBootstrap: %w", err)
		}
	}
	if plan.SourceDriver == SourceApollo && plan.Bootstrap == "" {
		return Plan{}, errors.New("Apollo source requires Bootstrap")
	}
	for index, patch := range plan.Patches {
		if err := validateRelativeFile(patch.File); err != nil {
			return Plan{}, fmt.Errorf("patch %d file: %w", index, err)
		}
		if err := validatePatchPath(patch.Path); err != nil {
			return Plan{}, fmt.Errorf("patch %d path: %w", index, err)
		}
	}
	for index, guard := range plan.Guards {
		if err := validateGuard(guard); err != nil {
			return Plan{}, fmt.Errorf("guard %d: %w", index, err)
		}
	}
	if plan.Apollo.Attempts < 0 {
		return Plan{}, errors.New("Apollo attempts must not be negative")
	}
	if plan.Apollo.RetryDelay < 0 {
		return Plan{}, errors.New("Apollo retry delay must not be negative")
	}
	if plan.Apollo.Timeout < 0 {
		return Plan{}, errors.New("Apollo timeout must not be negative")
	}
	plan.SourceDir = source
	plan.ConfigRoot = configRoot
	plan.TargetDir = target
	return plan, nil
}

func validateGuard(guard Guard) error {
	if err := validateRelativeFile(guard.File); err != nil {
		return fmt.Errorf("file: %w", err)
	}
	if err := validatePatchPath(guard.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if _, err := encodeYAMLGuardValue(guard.Value); err != nil {
		return fmt.Errorf("value: %w", err)
	}
	return nil
}

func VerifyGuards(targetDir string, guards []Guard) error {
	return VerifyPlanGuards(targetDir, DriverYAMLOverlay, guards)
}

func VerifyPlanGuards(targetDir string, driver Driver, guards []Guard) error {
	if err := requireRealDirectory(targetDir, "guard target directory"); err != nil {
		return err
	}
	for index, guard := range guards {
		if err := validateGuard(guard); err != nil {
			return fmt.Errorf("validate guard %d: %w", index, err)
		}
		path, err := secureGuardPath(targetDir, guard.File)
		if err != nil {
			return fmt.Errorf("resolve guard %d file: %w", index, err)
		}
		if err := verifyConfigGuard(driver, path, guard.Path, guard.Value); err != nil {
			return fmt.Errorf("verify guard %d in %s: %w", index, guard.File, err)
		}
	}
	return nil
}

func requireRealDirectory(path string, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %q: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s %q must be a real directory", label, path)
	}
	return nil
}

func validateRelativeFile(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return errors.New("path must be a non-empty relative file")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes the materialization directory")
	}
	return nil
}

func secureJoin(root string, relative string) (string, error) {
	if err := validateRelativeFile(relative); err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.Clean(relative))
	if !withinDirectory(root, path) {
		return "", errors.New("path escapes the materialization directory")
	}
	return path, nil
}

func secureGuardPath(root string, relative string) (string, error) {
	path, err := secureJoin(root, relative)
	if err != nil {
		return "", err
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	current := root
	segments := strings.Split(relativePath, string(filepath.Separator))
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect guard path component %q: %w", current, err)
		}
		if index == len(segments)-1 {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", fmt.Errorf("guard file %q must be a real file", current)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("guard path component %q must be a real directory", current)
		}
	}
	return path, nil
}

func withinDirectory(root string, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func ensurePrivateDirectory(root string, path string) error {
	if !withinDirectory(root, path) {
		return errors.New("directory escapes the materialization directory")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	if err := os.Chmod(current, 0700); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("runtime bootstrap directory %q must be a real directory", current)
		}
		if err := os.Chmod(current, 0700); err != nil {
			return err
		}
	}
	return nil
}

func protectTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("materialized path %q must not be a symbolic link", path)
		}
		if entry.IsDir() {
			if err := os.Chmod(path, 0700); err != nil {
				return fmt.Errorf("protect materialized directory %q: %w", path, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("materialized path %q must be a regular file", path)
		}
		if err := os.Chmod(path, 0600); err != nil {
			return fmt.Errorf("protect materialized file %q: %w", path, err)
		}
		return nil
	})
}
