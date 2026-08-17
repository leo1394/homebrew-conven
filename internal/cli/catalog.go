package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runCatalog(arguments []string) int {
	if len(arguments) == 0 {
		app.printCatalogUsage(app.Error)
		return 2
	}
	action := arguments[0]
	remaining := arguments[1:]
	switch action {
	case "-h", "--help", "help":
		app.printCatalogUsage(app.Output)
		return 0
	case "--edit":
		return app.runCatalogEdit(remaining)
	case "--validate":
		return app.runCatalogValidate(remaining)
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown catalog action %q", action)))
		app.printCatalogUsage(app.Error)
		return 2
	}
}

func (app App) runCatalogEdit(arguments []string) int {
	flags := flag.NewFlagSet("catalog --edit", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven catalog --edit")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("catalog --edit does not accept arguments"))
	}
	result, err := config.EditWorkspaceCatalog(app.Cwd, func(path string) error {
		return app.CatalogEditor(app.Context, path)
	})
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	if result.Changed {
		fmt.Fprintln(app.Output, style.Stage("Updated Conven catalog"))
	} else {
		fmt.Fprintln(app.Output, style.Stage("Conven catalog unchanged"))
	}
	fmt.Fprintln(app.Output, style.Detail("Catalog: "+style.Identifier(result.Path)))
	return 0
}

func (app App) runCatalogValidate(arguments []string) int {
	flags := flag.NewFlagSet("catalog --validate", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven catalog --validate")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("catalog --validate does not accept arguments"))
	}
	catalog, path, err := config.LoadWorkspaceCatalog(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	fmt.Fprintln(app.Output, style.Success("✓ Conven catalog is valid."))
	fmt.Fprintln(app.Output, style.Detail("Catalog: "+style.Identifier(path)))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Services: %d", len(catalog.Services))))
	fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("Disabled RPC bindings: %d", len(catalog.DisabledRPCBindings))))
	return 0
}

func (app App) printCatalogUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven catalog --edit
  conven catalog --validate

Manage the declarative service catalog at .conven/catalog.yaml.

--edit opens a temporary catalog copy and publishes it only after strict
validation. --validate checks the current catalog without changing it.
`)
}
