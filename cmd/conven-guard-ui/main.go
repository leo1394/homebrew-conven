// Conven Guard's host adapter. Uses the same transactions and authenticated
// dashboard as the CLI; never executes a shell or disables startup validation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/leo1394/homebrew-conven/internal/cli"
	"github.com/leo1394/homebrew-conven/internal/config"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
)

// Buffer complete lines before redaction so split writes cannot expose secrets.
type safeOutput struct { mu sync.Mutex; pending []byte; target io.Writer; dropping bool }
func (w *safeOutput) Write(p []byte) (int, error) {
	w.mu.Lock(); defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			if w.dropping { fmt.Fprintln(w.target, "[oversized diagnostic line omitted]") } else { fmt.Fprintln(w.target, convenruntime.RedactDiagnosticText(string(w.pending))) }
			w.pending = nil; w.dropping = false
		} else if !w.dropping {
			if len(w.pending) >= 65536 { w.pending = nil; w.dropping = true } else { w.pending = append(w.pending, b) }
		}
	}
	return len(p), nil
}
func (w *safeOutput) flush() { w.Write([]byte("\n")) }

func main() { os.Exit(run()) }
// Only the explicit GUI host-start transaction may opt into shared consumers.
func hostStartArguments(args []string) ([]string, bool, error) {
	if len(args) == 0 || args[0] != "--unprotected-start" { return args, false, nil }
	args = args[1:]
	if len(args) < 7 || args[0] != "-C" || args[1] == "" || args[2] != "services" || args[3] != "--start" || args[4] != "--env" || args[5] == "" || strings.HasPrefix(args[5], "-") {
		return nil, false, fmt.Errorf("unprotected start requires a workspace, environment and explicit services")
	}
	for _, name := range args[6:] {
		if name == "" || strings.HasPrefix(name, "-") { return nil, false, fmt.Errorf("unprotected start requires explicit service names, not additional options") }
	}
	return args, true, nil
}
func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer cancel()
	executable, err := os.Executable(); if err != nil { return 1 }
	helper := filepath.Join(filepath.Dir(executable), "guard-connection-client")
	if info, err := os.Stat(helper); err == nil && info.Mode().IsRegular() { convenruntime.ConfigureGuardConnectionHelper(helper) }
	args, unprotected, err := hostStartArguments(os.Args[1:])
	if err != nil { fmt.Fprintln(os.Stderr, err); return 1 }
	if len(args) == 2 && (args[0] == "--catalog" || args[0] == "--viewer") {
		root, err := filepath.EvalSymlinks(args[1])
		if err != nil { fmt.Fprintln(os.Stderr, "Cannot resolve workspace directory"); return 1 }
		workspace, err := convenruntime.OpenWebWorkspace(convenruntime.CommonOptions{Cwd: root})
		if err != nil { fmt.Fprintln(os.Stderr, convenruntime.RedactDiagnosticText(err.Error())); return 1 }
		if args[0] == "--viewer" {
			address, err := convenruntime.OpenWebDashboard(ctx, workspace, executable, nil, false)
			if err != nil { fmt.Fprintln(os.Stderr, convenruntime.RedactDiagnosticText(err.Error())); return 1 }
			json.NewEncoder(os.Stdout).Encode(map[string]string{"url":address}); return 0
		}
		environments := []string{}
		for name := range workspace.Manifest.Environments { environments = append(environments, name) }; sort.Strings(environments)
		session, err := workspace.Store.Load()
		if err != nil { fmt.Fprintln(os.Stderr, "Cannot inspect workspace session; resolve it before changing mode"); return 1 }
		lanAddress, lanInterface := convenruntime.LocalLANAddress()
		json.NewEncoder(os.Stdout).Encode(map[string]any{"services":config.ServiceNames(workspace.Manifest), "environments":environments, "hasSession":session != nil && len(session.Services) > 0, "lanAddress":lanAddress, "lanInterface":lanInterface})
		return 0
	}
	// Do not inherit an opt-in to shared Kafka from the GUI launch environment.
	os.Setenv(config.KafkaConsumersEnabledEnv, "false")
	if unprotected { os.Setenv(config.KafkaConsumersEnabledEnv, "true") }
	output := &safeOutput{target:os.Stdout}; defer output.flush()
	app := cli.App{Context:ctx, Input:os.Stdin, Output:output, Error:output, Executable:executable, Version:"guard-host"}
	return app.Run(args)
}
