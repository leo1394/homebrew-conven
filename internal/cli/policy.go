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
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runPolicy(arguments []string) int {
	if len(arguments) == 0 {
		app.printPolicyUsage(app.Error)
		return 2
	}
	action := arguments[0]
	remaining := arguments[1:]
	switch action {
	case "-h", "--help", "help":
		app.printPolicyUsage(app.Output)
		return 0
	case "--edit":
		return app.runPolicyEdit(remaining)
	case "--import":
		return app.runPolicyImport(remaining)
	case "--reset":
		return app.runPolicyReset(remaining)
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown policy action %q", action)))
		app.printPolicyUsage(app.Error)
		return 2
	}
}

func (app App) runPolicyImport(arguments []string) int {
	flags := flag.NewFlagSet("policy --import", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	edit := flags.Bool("edit", false, "edit a private import draft before validation and publication")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven policy --import [yaml-file] [--edit]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nImports the YAML file as the entire .conven/conven.yaml for the workspace resolved from cwd.")
		fmt.Fprintln(flags.Output(), "When yaml-file is omitted, imports <workspace>/application.yaml and reports the selected default.")
		fmt.Fprintln(flags.Output(), "This is a whole-file replacement, not a merge with repository scan results.")
		fmt.Fprintln(flags.Output(), "An existing manifest is backed up before replacement; --edit opens a private import-seeded draft before publication.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) > 1 {
		return app.fail(errors.New("policy --import accepts at most one YAML file"))
	}
	importPath := ""
	if len(flags.Args()) == 1 {
		importPath = flags.Args()[0]
	} else {
		workspace, err := config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(err)
		}
		importPath = filepath.Join(workspace, "application.yaml")
		fmt.Fprintf(app.Output, "No import file specified; using workspace default: %s\n", importPath)
		if _, err := os.Lstat(importPath); err != nil {
			if os.IsNotExist(err) {
				return app.fail(fmt.Errorf("default policy import file %q does not exist; specify a YAML filename or generate application.yaml first", importPath))
			}
			return app.fail(fmt.Errorf("inspect default policy import file %q: %w", importPath, err))
		}
	}
	var editImport func(string) error
	if *edit {
		editImport = func(path string) error {
			return app.PolicyEditor(app.Context, path)
		}
	}
	result, err := config.ImportWorkspacePolicy(app.Cwd, importPath, editImport)
	if err != nil {
		return app.fail(err)
	}
	if !result.Changed {
		fmt.Fprintf(app.Output, "Conven policy manifest already matches imported file %s: %s\n", result.SourcePath, result.Path)
	} else if result.Created {
		fmt.Fprintf(app.Output, "Imported Conven policy manifest from %s: %s\n", result.SourcePath, result.Path)
	} else {
		fmt.Fprintf(app.Output, "Replaced Conven policy manifest from imported file %s: %s\n", result.SourcePath, result.Path)
	}
	if result.BackupPath != "" {
		fmt.Fprintf(app.Output, "Pre-import manifest backup: %s\n", result.BackupPath)
	}
	fmt.Fprintln(app.Output, "Imported the entire manifest without merging repository scan results. Review it, then run conven doctor and conven services --start --dry-run before starting services.")
	return 0
}

func (app App) runPolicyEdit(arguments []string) int {
	flags := flag.NewFlagSet("policy --edit", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven policy --edit")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("policy --edit does not accept arguments"))
	}
	result, err := config.EditWorkspacePolicy(app.Cwd, func(path string) error {
		return app.PolicyEditor(app.Context, path)
	})
	if err != nil {
		return app.fail(err)
	}
	if result.Changed {
		fmt.Fprintf(app.Output, "Updated Conven policy manifest: %s\n", result.Path)
	} else {
		fmt.Fprintf(app.Output, "Conven policy manifest unchanged: %s\n", result.Path)
	}
	return 0
}

func (app App) runPolicyReset(arguments []string) int {
	flags := flag.NewFlagSet("policy --reset", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven policy --reset")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nDestructive: rebuilds the entire conven.yaml from read-only repository analysis.")
		fmt.Fprintln(flags.Output(), "Company policies, environments, ports, dependencies, patches, manual runner changes, and comments cannot be recovered by scanning.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("policy --reset does not accept arguments"))
	}
	result, err := config.ResetWorkspacePolicyFromScan(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	if len(result.Discovered) > 0 {
		fmt.Fprintf(app.Output, "%s: %s\n", style.Label("Discovered supported services"), style.Identifiers(result.Discovered, ", "))
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(app.Output, "%s: %s\n", style.Warning("Skipped by the built-in repository analyzers"), style.Identifiers(result.Skipped, ", "))
	}
	if !result.Changed {
		fmt.Fprintf(app.Output, "Conven policy manifest already matches the scan baseline: %s\n", result.Path)
		return 0
	}
	if result.Created {
		fmt.Fprintf(app.Output, "%s: %s\n", style.Label("Created Conven policy manifest from scan baseline"), style.Identifier(result.Path))
	} else {
		fmt.Fprintf(app.Output, "%s: %s\n", style.Label("Reset Conven policy manifest to scan baseline"), style.Identifier(result.Path))
	}
	if result.BackupPath != "" {
		fmt.Fprintf(app.Output, "Pre-reset manifest backup: %s\n", result.BackupPath)
	}
	fmt.Fprintln(app.Output, "Review and re-declare policies, environments, ports, dependencies, health checks, patches, and manual runner changes before starting services.")
	return 0
}

func (app App) printPolicyUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven policy --edit
  conven policy --import [yaml-file] [--edit]
  conven policy --reset

--edit opens a temporary copy of the workspace's sole .conven/conven.yaml and
publishes it only after strict validation. --import installs a local YAML file,
or <workspace>/application.yaml when omitted, as that entire manifest without
merging repository scan results;
schema validation does not prove that its service paths or infrastructure work.
--reset destructively rebuilds the manifest from repository facts that analyzers
can prove.
`)
}

func launchPolicyEditor(ctx context.Context, input *os.File, output io.Writer, errorOutput io.Writer, path string) error {
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
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+editor+` "$1"`, "conven-policy-editor", path)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = errorOutput
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("policy editor %q failed: %w", editor, err)
	}
	return nil
}
