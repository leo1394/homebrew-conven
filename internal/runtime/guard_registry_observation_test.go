package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/guardproof"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestGuardRuntimeObservationSkipsOnlyRemoteRegistration(t *testing.T) {
	var registryReads, healthReads atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { registryReads.Add(1); fmt.Fprint(w, `[]`) }))
	defer registry.Close()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { healthReads.Add(1); w.WriteHeader(200) }))
	defer health.Close()
	rpc, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer rpc.Close()
	_, httpPort, _ := net.SplitHostPort(health.Listener.Addr().String())
	port, _ := strconv.Atoi(httpPort)
	service := PlannedService{Name:"synthetic", RegistryRef:"consul", RegistryIdentity:"synthetic.rpc", Registry:&model.Registry{Driver:"consul", Address:registry.URL}, Kinds:[]string{"http", "rpc"}, Ports:map[string]int{"http":port, "rpc":rpc.Addr().(*net.TCPAddr).Port}}
	plan := &Plan{Workspace:&WorkspaceData{Manifest:&model.Manifest{Version:3}}, GuardSession:&guardproof.Session{Version:1}}
	observeRuntime, observeRegistry := runtimeObservationModes(plan, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel()
	var baseline *RegistrySnapshot
	if observeRegistry { baseline, err = snapshotServiceRegistry(ctx, t.TempDir(), service); if err != nil { t.Fatal(err) } }
	if registryReads.Load() != 0 || baseline != nil { t.Fatal("protected startup attempted a remote registration read") }
	if !observeRuntime { t.Fatal("Guard disabled listener/runtime verification") }
	pgid, err := syscall.Getpgid(os.Getpid()); if err != nil { t.Fatal(err) }
	process := ServiceProcess{Name:"synthetic", PID:os.Getpid(), PGID:pgid}
	if err := WaitHealthyChecks(ctx, process, []HealthCheck{{Type:"process"}, {Type:"http", URL:health.URL}, {Type:"tcp", Address:rpc.Addr().String()}}); err != nil { t.Fatal(err) }
	listeners, err := verifyServiceListeners(ctx, service, process)
	if err != nil { t.Fatal(err) }
	if healthReads.Load() != 1 || len(listeners) != 2 || listeners["http"].OwnerPID != os.Getpid() || listeners["rpc"].OwnerPID != os.Getpid() { t.Fatal("process/HTTP/RPC listener health evidence missing") }
	evidence, err := verifyServiceRegistry(ctx, t.TempDir(), service, baseline)
	if err != nil || evidence != nil || registryReads.Load() != 0 { t.Fatal("protected startup fabricated registry evidence or read remote registry") }
}

func TestUnprotectedRuntimeRegistryObservationUnchanged(t *testing.T) {
	t.Setenv("GUARD_SESSION", "1")
	var reads atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); fmt.Fprint(w, `[]`) }))
	defer registry.Close()
	plan := &Plan{Workspace:&WorkspaceData{Manifest:&model.Manifest{Version:3}}}
	observeRuntime, observeRegistry := runtimeObservationModes(plan, false)
	if !observeRuntime || !observeRegistry { t.Fatal("ordinary CLI registry observation disabled or environment opted into Guard") }
	service := PlannedService{Name:"synthetic", RegistryRef:"consul", RegistryIdentity:"synthetic.rpc", Registry:&model.Registry{Driver:"consul", Address:registry.URL}}
	baseline, err := snapshotServiceRegistry(context.Background(), t.TempDir(), service)
	if err != nil || baseline == nil || reads.Load() != 1 { t.Fatal("ordinary CLI baseline read changed") }
	for _, guard := range []*guardproof.Session{nil, {Version:1}} {
		plan.GuardSession = guard
		for _, skip := range []bool{false, true} {
			for _, version := range []int{2, 3} {
				plan.Workspace.Manifest.Version = version
				runtime, registration := runtimeObservationModes(plan, skip)
				if runtime != (!skip && version >= 3) || registration != (!skip && version >= 3 && guard == nil) { t.Fatal("observation policy changed skip/version semantics") }
			}
		}
	}
}
