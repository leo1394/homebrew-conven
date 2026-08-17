package cli

import (
	"errors"
	"os"

	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
)

func (app App) runHotReloadWatcher(arguments []string) int {
	if os.Getenv("CONVEN_HOT_RELOAD_PROCESS") != "1" {
		return app.fail(errors.New("__hot-reload is an internal Conven command"))
	}
	if len(arguments) != 0 {
		return app.fail(errors.New("__hot-reload does not accept arguments"))
	}
	workspace, err := convenruntime.OpenWorkspace(convenruntime.CommonOptions{Cwd: app.Cwd})
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.RunHotReloadWatcher(app.Context, workspace, convenruntime.HotReloadOptions{Output: app.Output}); err != nil {
		return app.fail(err)
	}
	return 0
}
