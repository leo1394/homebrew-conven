package runtime

import (
	"errors"
	"fmt"
	"io"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func CleanupRuntime(store *Store, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	unlock, err := store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	session, err := store.Load()
	if err != nil {
		return err
	}
	if session != nil {
		return errors.New("workspace has a saved Conven session; run conven services --stop-all before cleanup")
	}
	if err := store.CleanupCurrentOutputs(); err != nil {
		return err
	}
	style := terminal.New(output)
	fmt.Fprintf(output, "%s %s\n", style.Success("✓ Cleared build artifacts and service logs:"), style.Identifier(store.CurrentDir))
	return nil
}
