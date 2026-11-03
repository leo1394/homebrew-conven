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
	planOnly := flag.Bool("plan-reads", false, "resolve configuration reads without network access")
	collectDependencies := flag.Bool("collect-dependencies", false, "collect dependency inventory and independent configuration issues without launch authority")
	modules := flag.String("prepare-modules", "", "prepare selected Linux Go build dependencies in this host cache")
	flag.Parse()
	if *workspace == "" || *environment == "" || (*modules == "" && !*planOnly && (*responses == "" || *parent == "")) || flag.NArg() == 0 { fmt.Fprintln(os.Stderr, "workspace, environment, responses, output-parent and services are required"); os.Exit(2) }
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer cancel()
	ws, err := runtime.OpenWorkspace(runtime.CommonOptions{Cwd: *workspace})
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	if *modules!="" { if err:=runtime.PrepareGuardModules(ctx,ws,*environment,flag.Args(),*modules); err!=nil { fmt.Fprintln(os.Stderr,err); os.Exit(1) }; return }
	if *planOnly {
		requests, err := runtime.PlanGuardConfigurationReads(ws, *environment, flag.Args())
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		json.NewEncoder(os.Stdout).Encode(requests); return
	}
	prepare := runtime.PrepareGuardConfigurations
	if *collectDependencies { prepare = runtime.PrepareGuardDependencyReport }
	dir, err := prepare(ctx, ws, *environment, flag.Args(), *responses, *parent)
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	json.NewEncoder(os.Stdout).Encode(map[string]any{"preparedDirectory": dir, "businessServicesStarted": false, "guestEgressAuthorized": false})
}
