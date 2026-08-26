package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/leo1394/homebrew-conven/internal/cli"
)

var version = "0.3.0"
var versionDate = "2026-08-20"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "conven: resolve executable for hot reload: %v\n", err)
		os.Exit(1)
	}
	app := cli.App{
		Input:       os.Stdin,
		Output:      os.Stdout,
		Error:       os.Stderr,
		Context:     ctx,
		Executable:  executable,
		Version:     version,
		VersionDate: versionDate,
	}
	os.Exit(app.Run(os.Args[1:]))
}
