package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallCopiesRelativePythonPluginWithProtectedPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sourceDirectory := t.TempDir()
	const content = "#!/usr/bin/env python3\nprint('installed')\n"
	writeTestPlugin(t, filepath.Join(sourceDirectory, "sample.py"), content, 0644)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sourceDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	destination, err := Install("./sample.py")
	if err != nil {
		t.Fatal(err)
	}
	wantDestination := filepath.Join(home, ".conven", "plugins", "sample.py")
	if destination != wantDestination {
		t.Fatalf("destination = %q, want %q", destination, wantDestination)
	}
	directoryInfo, err := os.Stat(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if mode := directoryInfo.Mode().Perm(); mode != 0700 {
		t.Fatalf("plugin directory mode = %o, want 700", mode)
	}
	pluginInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if mode := pluginInfo.Mode().Perm(); mode != 0700 {
		t.Fatalf("plugin mode = %o, want 700", mode)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("plugin content = %q, want %q", data, content)
	}
}

func TestInstallAcceptsDirectPython3Shebang(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "direct.py")
	writeTestPlugin(t, source, "#!/usr/local/bin/python3\n", 0600)

	if _, err := Install(source); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalAndWorkspaceStoresUseSeparateDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}

	global, err := GlobalStore()
	if err != nil {
		t.Fatal(err)
	}
	local, err := WorkspaceStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if global.Scope() != GlobalScope {
		t.Fatalf("global scope = %q, want %q", global.Scope(), GlobalScope)
	}
	if local.Scope() != WorkspaceScope {
		t.Fatalf("workspace scope = %q, want %q", local.Scope(), WorkspaceScope)
	}
	if got, want := global.Directory(), filepath.Join(home, ".conven", "plugins"); got != want {
		t.Fatalf("global directory = %q, want %q", got, want)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := local.Directory(), filepath.Join(canonicalWorkspace, ".conven", "plugins"); got != want {
		t.Fatalf("workspace directory = %q, want %q", got, want)
	}
}

func TestScopedStoresKeepSameNamedPluginsIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	global, err := GlobalStore()
	if err != nil {
		t.Fatal(err)
	}
	local, err := WorkspaceStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	globalSource := filepath.Join(t.TempDir(), "inspect.py")
	localSource := filepath.Join(t.TempDir(), "inspect.py")
	replacementSource := filepath.Join(t.TempDir(), "inspect.py")
	const globalContent = "#!/usr/bin/env python3\nprint('global')\n"
	const localContent = "#!/usr/bin/env python3\nprint('local')\n"
	const replacementContent = "#!/usr/bin/env python3\nprint('replacement')\n"
	writeTestPlugin(t, globalSource, globalContent, 0600)
	writeTestPlugin(t, localSource, localContent, 0600)
	writeTestPlugin(t, replacementSource, replacementContent, 0600)

	globalPath, err := global.Install(globalSource)
	if err != nil {
		t.Fatal(err)
	}
	localPath, err := local.Install(localSource)
	if err != nil {
		t.Fatal(err)
	}
	if globalPath == localPath {
		t.Fatalf("global and workspace installs share path %q", globalPath)
	}
	for label, store := range map[string]Store{"global": global, "workspace": local} {
		names, err := store.List()
		if err != nil {
			t.Fatalf("list %s plugins: %v", label, err)
		}
		if !reflect.DeepEqual(names, []string{"inspect"}) {
			t.Fatalf("%s plugins = %#v, want inspect", label, names)
		}
	}

	if _, err := local.Replace(replacementSource); err != nil {
		t.Fatal(err)
	}
	globalData, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(globalData) != globalContent {
		t.Fatalf("workspace replacement changed global plugin: %q", globalData)
	}
	localData, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(localData) != replacementContent {
		t.Fatalf("workspace replacement = %q, want %q", localData, replacementContent)
	}
	removed, err := local.Remove("inspect")
	if err != nil {
		t.Fatal(err)
	}
	if removed != localPath {
		t.Fatalf("removed path = %q, want %q", removed, localPath)
	}
	if _, err := os.Stat(globalPath); err != nil {
		t.Fatalf("workspace removal changed global plugin: %v", err)
	}
}

func TestScopedRunUsesOnlySelectedStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	global, err := GlobalStore()
	if err != nil {
		t.Fatal(err)
	}
	local, err := WorkspaceStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	globalSource := filepath.Join(t.TempDir(), "inspect.py")
	localSource := filepath.Join(t.TempDir(), "inspect.py")
	writeTestPlugin(t, globalSource, "#!/usr/bin/env python3\nprint('global')\n", 0600)
	writeTestPlugin(t, localSource, "#!/usr/bin/env python3\nprint('workspace')\n", 0600)
	if _, err := global.Install(globalSource); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Install(localSource); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		store Store
		want  string
	}{
		{name: "global", store: global, want: "global\n"},
		{name: "workspace", store: local, want: "workspace\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.store.Run(context.Background(), "inspect", workspace, nil, nil, &output, nil); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestWorkspaceRunDoesNotFallBackToSameNamedGlobalPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	global, err := GlobalStore()
	if err != nil {
		t.Fatal(err)
	}
	local, err := WorkspaceStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	globalSource := filepath.Join(t.TempDir(), "inspect.py")
	localSource := filepath.Join(t.TempDir(), "inspect.py")
	writeTestPlugin(t, globalSource, "#!/usr/bin/env python3\nprint('global')\n", 0600)
	writeTestPlugin(t, localSource, "#!/usr/bin/env python3\nprint('workspace')\n", 0600)
	if _, err := global.Install(globalSource); err != nil {
		t.Fatal(err)
	}
	localPath, err := local.Install(localSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(localPath, 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = local.Run(context.Background(), "inspect", workspace, nil, nil, &output, nil)
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("workspace run error = %v, want local executable error", err)
	}
	if errors.Is(err, ErrNotInstalled) {
		t.Fatalf("invalid workspace plugin was reported as absent: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("workspace run unexpectedly used global plugin: %q", output.String())
	}
}

func TestScopedRunReportsMissingPluginWithSentinel(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	local, err := WorkspaceStore(workspace)
	if err != nil {
		t.Fatal(err)
	}

	err = local.Run(context.Background(), "inspect", workspace, nil, nil, nil, nil)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing plugin error = %v, want ErrNotInstalled", err)
	}
}

func TestWorkspaceStoreRequiresRealWorkspaceBoundary(t *testing.T) {
	workspace := t.TempDir()
	if _, err := WorkspaceStore(workspace); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing boundary error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "boundary")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, ".conven")); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceStore(workspace); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("symlink boundary error = %v", err)
	}
}

func TestInstallBuiltinsWithNoPythonFilesCreatesProtectedEmptyDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := InstallBuiltins(); err != nil {
		t.Fatal(err)
	}
	directory, err := Directory()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0700 {
		t.Fatalf("plugin directory mode = %o, want 700", mode)
	}
	installed, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 0 {
		t.Fatalf("built-in plugins = %#v, want empty", installed)
	}
}

func TestBuiltinInstallPreservesExistingUserPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	path := filepath.Join(directory, "generic.py")
	const userContent = "#!/usr/bin/env python3\nprint('user copy')\n"
	writeTestPlugin(t, path, userContent, 0700)

	destination, err := installPlugin(directory, "generic.py", strings.NewReader("#!/usr/bin/env python3\nprint('builtin')\n"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if destination != path {
		t.Fatalf("destination = %q, want %q", destination, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != userContent {
		t.Fatalf("existing user plugin was overwritten: %q", data)
	}
}

func TestInstallDoesNotOverwriteExistingPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	firstSource := filepath.Join(t.TempDir(), "inspect.py")
	secondSource := filepath.Join(t.TempDir(), "inspect.py")
	const original = "#!/usr/bin/env python3\nprint('original')\n"
	writeTestPlugin(t, firstSource, original, 0600)
	writeTestPlugin(t, secondSource, "#!/usr/bin/env python3\nprint('replacement')\n", 0600)
	destination, err := Install(firstSource)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Install(secondSource); err == nil || !errors.Is(err, ErrAlreadyInstalled) || !strings.Contains(err.Error(), "already installed") || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("install error = %v, want explicit no-overwrite error", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("existing plugin was overwritten: %q", data)
	}
}

func TestReplaceOverwritesExistingPluginWithProtectedPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	firstSource := filepath.Join(t.TempDir(), "inspect.py")
	secondSource := filepath.Join(t.TempDir(), "inspect.py")
	writeTestPlugin(t, firstSource, "#!/usr/bin/env python3\nprint('original')\n", 0600)
	const replacement = "#!/usr/bin/env python3\nprint('replacement')\n"
	writeTestPlugin(t, secondSource, replacement, 0600)
	destination, err := Install(firstSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0600); err != nil {
		t.Fatal(err)
	}

	replaced, err := Replace(secondSource)
	if err != nil {
		t.Fatal(err)
	}
	if replaced != destination {
		t.Fatalf("replacement destination = %q, want %q", replaced, destination)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != replacement {
		t.Fatalf("replacement content = %q, want %q", data, replacement)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0700 {
		t.Fatalf("replacement mode = %o, want 700", mode)
	}
}

func TestReplaceFailurePreservesExistingPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	const original = "#!/usr/bin/env python3\nprint('original')\n"
	path := filepath.Join(directory, "inspect.py")
	writeTestPlugin(t, path, original, 0700)
	invalidSource := filepath.Join(t.TempDir(), "inspect.py")
	writeTestPlugin(t, invalidSource, "print('missing shebang')\n", 0600)

	if _, err := Replace(invalidSource); err == nil || !strings.Contains(err.Error(), "python3 shebang") {
		t.Fatalf("invalid replacement error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("existing plugin changed after replacement failure: %q", data)
	}
}

func TestReplaceRejectsDestinationChangedWhileStaging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	destination := filepath.Join(directory, "inspect.py")
	writeTestPlugin(t, destination, "#!/usr/bin/env python3\nprint('original')\n", 0700)
	newer := filepath.Join(directory, "newer.py")
	const newerContent = "#!/usr/bin/env python3\nprint('newer')\n"
	writeTestPlugin(t, newer, newerContent, 0700)
	input := &callbackReader{
		input: strings.NewReader("#!/usr/bin/env python3\nprint('replacement')\n"),
		beforeFirstRead: func() error {
			return os.Rename(newer, destination)
		},
	}

	if _, err := installPlugin(directory, "inspect.py", input, false, true); err == nil || !strings.Contains(err.Error(), "changed while its replacement was prepared") {
		t.Fatalf("changed destination replacement error = %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != newerContent {
		t.Fatalf("newer plugin was overwritten: %q", data)
	}
}

func TestReplaceRejectsSymlinkDestination(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	target := filepath.Join(t.TempDir(), "target.py")
	const targetContent = "#!/usr/bin/env python3\nprint('target')\n"
	writeTestPlugin(t, target, targetContent, 0700)
	destination := filepath.Join(directory, "linked.py")
	if err := os.Symlink(target, destination); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "linked.py")
	writeTestPlugin(t, source, "#!/usr/bin/env python3\nprint('replacement')\n", 0600)

	if _, err := Replace(source); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("symlink replacement error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != targetContent {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestRemoveDeletesOnlySafeNamedRegularPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	path := filepath.Join(directory, "inspect.py")
	writeTestPlugin(t, path, "#!/usr/bin/env python3\n", 0700)

	removed, err := Remove("inspect")
	if err != nil {
		t.Fatal(err)
	}
	if removed != path {
		t.Fatalf("removed path = %q, want %q", removed, path)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("removed plugin still exists: %v", err)
	}
	if _, err := Remove("inspect.py"); err == nil || !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing plugin removal error = %v", err)
	}
}

func TestRemoveRejectsUnsafeNamesAndEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	target := filepath.Join(directory, "target.py")
	writeTestPlugin(t, target, "#!/usr/bin/env python3\n", 0700)
	if err := os.Symlink(target, filepath.Join(directory, "linked.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested.py"), 0700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		want string
	}{
		{name: "../target", want: "invalid plugin name"},
		{name: "linked", want: "symbolic links are not allowed"},
		{name: "nested", want: "not a regular file"},
	} {
		t.Run(strings.ReplaceAll(test.name, "/", "_"), func(t *testing.T) {
			if _, err := Remove(test.name); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("remove error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReplaceAndRemoveRejectSymlinkConvenHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	targetRoot := filepath.Join(t.TempDir(), "state")
	targetDirectory := filepath.Join(targetRoot, "plugins")
	if err := os.MkdirAll(targetDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	const original = "#!/usr/bin/env python3\nprint('original')\n"
	targetPlugin := filepath.Join(targetDirectory, "inspect.py")
	writeTestPlugin(t, targetPlugin, original, 0700)
	if err := os.Symlink(targetRoot, filepath.Join(home, ".conven")); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "inspect.py")
	writeTestPlugin(t, replacement, "#!/usr/bin/env python3\nprint('replacement')\n", 0600)

	if _, err := Replace(replacement); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("replacement through symlink Conven home error = %v", err)
	}
	if _, err := Remove("inspect"); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("removal through symlink Conven home error = %v", err)
	}
	data, err := os.ReadFile(targetPlugin)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("plugin outside Conven home changed: %q", data)
	}
}

func TestInstallRejectsUnsafeSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	valid := filepath.Join(root, "valid.py")
	writeTestPlugin(t, valid, "#!/usr/bin/env python3\n", 0600)
	symlink := filepath.Join(root, "linked.py")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory.py")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestPlugin(t, filepath.Join(root, "plugin.sh"), "#!/usr/bin/env python3\n", 0600)
	writeTestPlugin(t, filepath.Join(root, "bad name.py"), "#!/usr/bin/env python3\n", 0600)
	writeTestPlugin(t, filepath.Join(root, ".hidden.py"), "#!/usr/bin/env python3\n", 0600)
	writeTestPlugin(t, filepath.Join(root, "double.py.py"), "#!/usr/bin/env python3\n", 0600)
	writeTestPlugin(t, filepath.Join(root, "missing-shebang.py"), "print('missing')\n", 0600)
	writeTestPlugin(t, filepath.Join(root, "wrong-shebang.py"), "#!/usr/bin/env python2\n", 0600)

	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "empty", source: "", want: "source path is empty"},
		{name: "missing", source: filepath.Join(root, "missing.py"), want: "inspect Conven plugin source"},
		{name: "symlink", source: symlink, want: "symbolic links are not allowed"},
		{name: "directory", source: directory, want: "not a regular file"},
		{name: "extension", source: filepath.Join(root, "plugin.sh"), want: "must have a .py extension"},
		{name: "space", source: filepath.Join(root, "bad name.py"), want: "invalid Conven plugin filename"},
		{name: "hidden", source: filepath.Join(root, ".hidden.py"), want: "invalid Conven plugin filename"},
		{name: "double extension", source: filepath.Join(root, "double.py.py"), want: "invalid Conven plugin filename"},
		{name: "missing shebang", source: filepath.Join(root, "missing-shebang.py"), want: "must start with a python3 shebang"},
		{name: "wrong shebang", source: filepath.Join(root, "wrong-shebang.py"), want: "must use a python3 shebang"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Install(test.source); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("install error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestListReturnsSortedSafeExecutablePythonPlugins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	writeTestPlugin(t, filepath.Join(directory, "zeta.py"), "#!/usr/bin/env python3\n", 0700)
	writeTestPlugin(t, filepath.Join(directory, "alpha.py"), "#!/usr/bin/env python3\n", 0700)
	writeTestPlugin(t, filepath.Join(directory, "not-executable.py"), "", 0600)
	writeTestPlugin(t, filepath.Join(directory, "not-python"), "", 0700)
	writeTestPlugin(t, filepath.Join(directory, "bad name.py"), "", 0700)
	if err := os.Mkdir(filepath.Join(directory, "directory.py"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "zeta.py"), filepath.Join(directory, "linked.py")); err != nil {
		t.Fatal(err)
	}

	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins = %#v, want %#v", got, want)
	}
}

func TestListMissingDirectoryIsEmpty(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "missing", "home"))

	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("plugins = %#v, want empty", got)
	}
}

func TestInstallAndListRejectSymlinkPluginDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".conven")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "plugins")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "plugins")); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "valid.py")
	writeTestPlugin(t, source, "#!/usr/bin/env python3\n", 0600)

	if _, err := Install(source); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("install error = %v, want symbolic link rejection", err)
	}
	if _, err := List(); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("list error = %v, want symbolic link rejection", err)
	}
}

func TestRunPassesCanonicalWorkspaceArgumentsEnvironmentAndIO(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONVEN_WORKSPACE", "/stale-workspace")
	directory := prepareTestPluginDirectory(t)
	plugin := `#!/usr/bin/env python3
import os
import sys

print("cwd=" + os.getcwd())
print("workspace=" + os.environ["CONVEN_WORKSPACE"])
print("args=" + "|".join(sys.argv[1:]))
print("input=" + sys.stdin.read())
print("plugin stderr", file=sys.stderr)
`
	writeTestPlugin(t, filepath.Join(directory, "inspect.py"), plugin, 0700)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	err = Run(
		context.Background(),
		"inspect",
		workspace,
		[]string{"--output", "candidate.yaml"},
		strings.NewReader("payload"),
		&output,
		&errorOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := strings.Join([]string{
		"cwd=" + canonical,
		"workspace=" + canonical,
		"args=--workspace|" + canonical + "|--output|candidate.yaml",
		"input=payload",
		"",
	}, "\n")
	if output.String() != wantOutput {
		t.Fatalf("stdout = %q, want %q", output.String(), wantOutput)
	}
	if errorOutput.String() != "plugin stderr\n" {
		t.Fatalf("stderr = %q, want plugin stderr", errorOutput.String())
	}
}

func TestRunRejectsUnsafeOrUnexecutablePlugins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := prepareTestPluginDirectory(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestPlugin(t, filepath.Join(directory, "plain.py"), "", 0600)
	writeTestPlugin(t, filepath.Join(directory, "target.py"), "#!/usr/bin/env python3\n", 0700)
	if err := os.Symlink(filepath.Join(directory, "target.py"), filepath.Join(directory, "linked.py")); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		want string
	}{
		{name: "../target", want: "invalid plugin name"},
		{name: "/tmp/target", want: "invalid plugin name"},
		{name: "bad name", want: "invalid plugin name"},
		{name: ".hidden", want: "invalid plugin name"},
		{name: "plain", want: "not executable"},
		{name: "linked", want: "symbolic links are not allowed"},
	} {
		t.Run(strings.ReplaceAll(test.name, "/", "_"), func(t *testing.T) {
			err := Run(context.Background(), test.name, workspace, nil, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsMissingWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := Run(context.Background(), "inspect", filepath.Join(t.TempDir(), "missing"), nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve plugin workspace") {
		t.Fatalf("error = %v, want missing workspace error", err)
	}
}

func TestRunRejectsWorkspaceOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	for _, args := range [][]string{
		{"--workspace", t.TempDir()},
		{"--workspace=" + t.TempDir()},
	} {
		err := Run(context.Background(), "inspect", workspace, args, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "--workspace is reserved by Conven") {
			t.Fatalf("args = %#v, error = %v, want reserved workspace error", args, err)
		}
	}
}

func prepareTestPluginDirectory(t *testing.T) string {
	t.Helper()
	directory, err := preparePluginDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

type callbackReader struct {
	input           io.Reader
	beforeFirstRead func() error
	called          bool
}

func (reader *callbackReader) Read(buffer []byte) (int, error) {
	if !reader.called {
		reader.called = true
		if err := reader.beforeFirstRead(); err != nil {
			return 0, err
		}
	}
	return reader.input.Read(buffer)
}

func writeTestPlugin(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
