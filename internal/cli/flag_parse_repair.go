package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/config"
	"golang.org/x/term"
)

func (app App) confirmFlagParseRepair(ctx context.Context, repair *config.FlagParseRepair) (bool, error) {
	if app.Input == nil || !term.IsTerminal(int(app.Input.Fd())) { return false, nil }
	if ctx == nil { ctx = context.Background() }
	if err := ctx.Err(); err != nil { return false, err }
	if _, err := fmt.Fprintf(app.Output, "Service %s needs flag.Parse() before reading -f.\nFile: %s:%d\nProposed source edit: add flag.Parse() before loading configuration.\nModify this source file? [y/N]: ", repair.Service, repair.Path, repair.Line); err != nil { return false, err }
	reader := bufio.NewReader(app.Input)
	for {
		if err := ctx.Err(); err != nil { return false, err }
		ready, err := waitForConfirmationInput(reader, int(app.Input.Fd()), 100*time.Millisecond)
		if err != nil { return false, err }
		if !ready { continue }
		answer, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) { return false, nil }
		if err != nil { return false, err }
		if err := ctx.Err(); err != nil { return false, err }
		return flagParseRepairApproved(answer), nil
	}
}

func flagParseRepairApproved(answer string) bool { return strings.EqualFold(strings.TrimSpace(answer), "y") }
