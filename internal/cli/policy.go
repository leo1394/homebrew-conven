package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/selector"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runWorkspaceManifest(arguments []string) int {
	if len(arguments) == 0 {
		app.printWorkspaceManifestUsage(app.Error)
		return 2
	}
	action := arguments[0]
	remaining := arguments[1:]
	if action != "-h" && action != "--help" && action != "help" && action != "--migrate" {
		if err := app.requireWorkspaceManifestV3(); err != nil {
			return app.fail(err)
		}
	}
	switch action {
	case "-h", "--help", "help":
		app.printWorkspaceManifestUsage(app.Output)
		return 0
	case "--edit":
		return app.runWorkspaceManifestEdit(remaining)
	case "--validate":
		return app.runWorkspaceManifestValidate(remaining)
	case "--migrate":
		return app.runWorkspaceManifestMigrate(remaining)
	case "--import":
		return app.runWorkspaceManifestImport(remaining)
	case "--reset":
		return app.runWorkspaceManifestReset(remaining)
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown workspace action %q", action)))
		app.printWorkspaceManifestUsage(app.Error)
		return 2
	}
}

func (app App) requireWorkspaceManifestV3() error {
	path, _, err := config.ResolvePath(app.Cwd)
	if err != nil {
		return nil
	}
	manifest, err := config.Load(path)
	if err != nil {
		return err
	}
	if manifest.Version < 3 {
		return fmt.Errorf("Conven manifest %q uses version %d; run conven workspace --migrate before using this command", path, manifest.Version)
	}
	return nil
}

func (app App) runWorkspaceManifestMigrate(arguments []string) int {
	flags := flag.NewFlagSet("workspace --migrate", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven workspace --migrate")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nAtomically migrates a version 1 or 2 manifest to version 3 after verifying that no workspace services are running.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("workspace --migrate does not accept arguments"))
	}
	result, err := config.MigrateWorkspaceManifest(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	if !result.Changed {
		fmt.Fprintln(app.Output, style.Stage("Conven workspace manifest already uses version 3"))
	} else {
		fmt.Fprintln(app.Output, style.Stage(fmt.Sprintf("Migrated Conven workspace manifest from version %d to version 3", result.From)))
	}
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
	if result.BackupPath != "" {
		fmt.Fprintln(app.Output, style.Detail("Backup: "+style.Identifier(result.BackupPath)))
	}
	return 0
}

func (app App) runWorkspaceManifestImport(arguments []string) int {
	flags := flag.NewFlagSet("workspace --import", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	edit := flags.Bool("edit", false, "edit a private import draft before validation and publication")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven workspace --import [yaml-file] [--edit]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nImports the YAML file as the entire .conven/conven.yaml for the workspace resolved from cwd.")
		fmt.Fprintln(flags.Output(), "When yaml-file is omitted, selects a YAML file from the workspace root interactively.")
		fmt.Fprintln(flags.Output(), "This is a whole-file replacement, not a merge with repository scan results.")
		fmt.Fprintln(flags.Output(), "An existing manifest is backed up before replacement; --edit opens a private import-seeded draft before publication.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) > 1 {
		return app.fail(errors.New("workspace --import accepts at most one YAML file"))
	}
	importPath := ""
	if len(flags.Args()) == 1 {
		importPath = flags.Args()[0]
	} else {
		workspace, err := config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(err)
		}
		candidates, err := workspaceYAMLCandidates(workspace)
		if err != nil {
			return app.fail(err)
		}
		if len(candidates) == 0 {
			return app.fail(errors.New("no YAML manifest files were found in the workspace root; specify a YAML filename"))
		}
		selected, confirmed, err := app.SingleSelector(app.Context, app.Input, app.Output, selector.Prompt{
			Title:                "Select a workspace manifest",
			ConfirmationLabel:    "Importing workspace manifest",
			EmptySelectionNotice: "Select one manifest file before confirming.",
		}, candidates)
		if err != nil {
			if errors.Is(err, selector.ErrNotTerminal) {
				return app.fail(errors.New("workspace import selection requires an interactive terminal; specify a YAML filename explicitly"))
			}
			return app.fail(err)
		}
		if !confirmed {
			style := terminal.New(app.Output)
			fmt.Fprintln(app.Output, style.Stage("Workspace import cancelled"))
			fmt.Fprintln(app.Output, style.Detail("Conven workspace manifest was not changed."))
			return 0
		}
		importPath = filepath.Join(workspace, selected.Name)
	}
	var editImport func(string) error
	if *edit {
		editImport = func(path string) error {
			return app.WorkspaceEditor(app.Context, path)
		}
	}
	result, err := config.ImportWorkspacePolicy(app.Cwd, importPath, editImport)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	stage := "Replaced Conven workspace manifest"
	if !result.Changed {
		stage = "Conven workspace manifest already matches import"
	} else if result.Created {
		stage = "Imported Conven workspace manifest"
	}
	fmt.Fprintln(app.Output, style.Stage(stage))
	fmt.Fprintln(app.Output, style.Detail("Source: "+style.Identifier(result.SourcePath)))
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
	if result.BackupPath != "" {
		fmt.Fprintln(app.Output, style.Detail("Backup: "+style.Identifier(result.BackupPath)))
	}
	printWarningBlock(app.Error, "Workspace import treats the source as the complete workspace manifest.", []string{
		"Repository scan results were not merged.",
	}, []string{
		"conven doctor",
		"conven services --start --dry-run",
	})
	return 0
}

func workspaceYAMLCandidates(workspace string) ([]selector.Candidate, error) {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil, fmt.Errorf("read workspace root %q for manifest import: %w", workspace, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		extension := filepath.Ext(entry.Name())
		if !strings.EqualFold(extension, ".yaml") && !strings.EqualFold(extension, ".yml") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect manifest import candidate %q: %w", filepath.Join(workspace, entry.Name()), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	candidates := make([]selector.Candidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, selector.Candidate{Name: name})
	}
	return candidates, nil
}

func (app App) runWorkspaceManifestEdit(arguments []string) int {
	flags := flag.NewFlagSet("workspace --edit", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven workspace --edit")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nvi, vim, and nvim use two-space YAML indentation with expandtab. Leading indentation tabs are normalized to the same two-column stops before validation; other YAML formatting is preserved exactly.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("workspace --edit does not accept arguments"))
	}
	result, err := config.EditWorkspacePolicy(app.Cwd, func(path string) error {
		return app.WorkspaceEditor(app.Context, path)
	})
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	if result.Changed {
		fmt.Fprintln(app.Output, style.Stage("Updated Conven workspace manifest"))
	} else {
		fmt.Fprintln(app.Output, style.Stage("Conven workspace manifest unchanged"))
	}
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
	return 0
}

func (app App) runWorkspaceManifestValidate(arguments []string) int {
	flags := flag.NewFlagSet("workspace --validate", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven workspace --validate")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("workspace --validate does not accept arguments"))
	}
	workspace, err := config.FindWorkspace(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	path := config.ManifestPath(workspace)
	manifest, err := config.Load(path)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	fmt.Fprintln(app.Output, style.Success("✓ Conven workspace manifest is valid."))
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(path)))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Services: %d", len(manifest.Services))))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Policies: %d", len(manifest.Policies))))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Environments: %d", len(manifest.Environments))))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Disabled bindings: %d", len(manifest.Workspace.DisabledBindings))))
	return 0
}

func (app App) runWorkspaceManifestReset(arguments []string) int {
	flags := flag.NewFlagSet("workspace --reset", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven workspace --reset")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nDestructive: rebuilds the entire conven.yaml from read-only repository analysis.")
		fmt.Fprintln(flags.Output(), "Company policies, environments, ports, dependencies, patches, manual runner changes, and comments cannot be recovered by scanning.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("workspace --reset does not accept arguments"))
	}
	result, err := config.ResetWorkspacePolicyFromScan(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	if !result.Changed {
		fmt.Fprintln(app.Output, style.Stage("Conven workspace manifest already matches scan baseline"))
	} else if result.Created {
		fmt.Fprintln(app.Output, style.Stage("Created Conven workspace manifest from scan baseline"))
	} else {
		fmt.Fprintln(app.Output, style.Stage("Reset Conven workspace manifest to scan baseline"))
	}
	if len(result.Discovered) == 0 {
		fmt.Fprintln(app.Output, style.Detail("Discovered services: none"))
	} else {
		fmt.Fprintln(app.Output, style.Detail("Discovered services: "+style.Identifiers(result.Discovered, ", ")))
	}
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
	if result.BackupPath != "" {
		fmt.Fprintln(app.Output, style.Detail("Backup: "+style.Identifier(result.BackupPath)))
	}
	if result.Changed {
		warningDetails := []string{"Review and restore manually declared policies, environments, ports, dependencies, health checks, patches, and runner changes."}
		if len(result.Skipped) > 0 {
			warningDetails = append(warningDetails, "Skipped repositories: "+strings.Join(result.Skipped, ", "))
		}
		printWarningBlock(app.Error, "Workspace reset rebuilds the complete workspace manifest.", warningDetails, []string{
			"conven doctor",
			"conven services --start --dry-run",
		})
	}
	return 0
}

func (app App) printWorkspaceManifestUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven workspace --edit
  conven workspace --validate
  conven workspace --migrate
  conven workspace --import [yaml-file] [--edit]
  conven workspace --reset

--edit opens a temporary copy of the workspace's sole .conven/conven.yaml and
publishes it only after strict validation. vi, vim, and nvim use two-space YAML
indentation with expandtab; leading indentation tabs are normalized before
validation without re-encoding the rest of the YAML. --validate checks the
current manifest without changing it. --import installs a local YAML file,
or interactively selects a .yaml/.yml file from the workspace root when omitted,
as that entire manifest without merging repository scan results. Non-interactive
use must specify the YAML filename. Schema validation does not prove that its
service paths or infrastructure work.
--migrate atomically upgrades a stopped version 1 or 2 workspace to version 3.
--reset destructively rebuilds the manifest from repository facts that analyzers
can prove.
`)
}

func launchWorkspaceEditor(ctx context.Context, input *os.File, output io.Writer, errorOutput io.Writer, path string) error {
	return launchEditor(ctx, input, output, errorOutput, path, "conven-workspace-editor", "workspace")
}

func launchEditor(ctx context.Context, input *os.File, output io.Writer, errorOutput io.Writer, path string, commandName string, label string) error {
	editor := ""
	for _, name := range []string{"CONVEN_EDITOR", "VISUAL", "EDITOR"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			editor = value
			break
		}
	}
	if editor == "" {
		editor = "vi"
	}
	invocation := "exec "+editor
	if viFamilyEditor(editor) {
		invocation += " -c 'setlocal filetype=yaml expandtab tabstop=2 shiftwidth=2 softtabstop=2 autoindent'"
	}
	command := exec.CommandContext(ctx, "/bin/sh", "-c", invocation+` "$1"`, commandName, path)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = errorOutput
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s editor %q failed: %w", label, editor, err)
	}
	return nil
}

func viFamilyEditor(editor string) bool {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return false
	}
	name := filepath.Base(strings.Trim(fields[0], `"'`))
	return name == "vi" || name == "vim" || name == "nvim"
}
