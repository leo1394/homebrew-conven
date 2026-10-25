// Host-only configuration preparation; this helper has no execution or egress API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/leo1394/homebrew-conven/internal/runtime"
)

func main() {
	workspace := flag.String("workspace", "", "source workspace (read only)")
	environment := flag.String("environment", "", "explicit environment")
	responses := flag.String("responses", "", "private host Apollo response directory")
	parent := flag.String("output-parent", "", "private preparation parent")
	flag.Parse()
	if *workspace == "" || *environment == "" || *responses == "" || *parent == "" || flag.NArg() == 0 { fmt.Fprintln(os.Stderr, "workspace, environment, responses, output-parent and services are required"); os.Exit(2) }
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer cancel()
	ws, err := runtime.OpenWorkspace(runtime.CommonOptions{Cwd: *workspace})
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	dir, err := runtime.PrepareGuardConfigurations(ctx, ws, *environment, flag.Args(), *responses, *parent)
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	json.NewEncoder(os.Stdout).Encode(map[string]any{"preparedDirectory": dir, "businessServicesStarted": false, "guestEgressAuthorized": false})
}
