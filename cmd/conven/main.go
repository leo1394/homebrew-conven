package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/leo1394/homebrew-conven/internal/cli"
)

var version = "0.2.9"
var versionDate = "2026-08-12"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := cli.App{
		Input:       os.Stdin,
		Output:      os.Stdout,
		Error:       os.Stderr,
		Context:     ctx,
		Version:     version,
		VersionDate: versionDate,
	}
	os.Exit(app.Run(os.Args[1:]))
}
