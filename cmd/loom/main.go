package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/leo1394/homebrew-loom/internal/cli"
)

var version = "0.1.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := cli.App{
		Input:   os.Stdin,
		Output:  os.Stdout,
		Error:   os.Stderr,
		Context: ctx,
		Version: version,
	}
	os.Exit(app.Run(os.Args[1:]))
}
