package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/guardproof"
	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func TestGuardDependencyPreflightDoesNotProbeOptionalConsulBindings(t *testing.T) {
	var reads atomic.Int32
	registry:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { reads.Add(1); fmt.Fprint(w,`[]`) })); defer registry.Close()
	host,port:=externalDependencyTestServerAddress(t,registry)
	service:=externalDependencyTestService(t,fmt.Sprintf("deviceRpc:\n  discovType: consul\n  consul: {host: %s, port: %d, key: device.rpc}\n",host,port),nil)
	plan:=&Plan{Order:[]string{"api"},Services:map[string]PlannedService{"api":service},GuardSession:&guardproof.Session{Version:1}}
	for _,announce:=range []bool{true,false} {
		var output strings.Builder
		if err:=runRuntimePreflight(context.Background(),plan,&output,announce);err!=nil { t.Fatal(err) }
		if reads.Load()!=0 || strings.Contains(output.String(),"External Consul") { t.Fatal("Guard repeated host-reviewed availability checks or fabricated health") }
	}
	// An environment variable alone cannot select the protected behavior.
	t.Setenv("GUARD_SESSION","1"); plan.GuardSession=nil
	var output strings.Builder
	if err:=runRuntimePreflight(context.Background(),plan,&output,true);err==nil || reads.Load()!=1 { t.Fatal("ordinary host Consul preflight was bypassed") }
}

func TestGuardDependencyPreflightStillRejectsConfigAndIsolationChanges(t *testing.T) {
	for _,isolation:=range []bool{false,true} {
		service:=externalDependencyTestService(t,"host: 0.0.0.0\n",nil)
		if isolation {
			service.Config.Isolation.RegistrationMode="config"
			service.Config.Plan.Guards=[]materialize.Guard{{File:"application.yaml",Path:"host",Value:"127.0.0.1"}}
		} else {
			if err:=os.WriteFile(filepath.Join(service.Config.Plan.TargetDir,"application.yaml"),[]byte("deviceRpc: [invalid\n"),0600);err!=nil { t.Fatal(err) }
		}
		plan:=&Plan{Order:[]string{"api"},Services:map[string]PlannedService{"api":service},GuardSession:&guardproof.Session{Version:1}}
		var output strings.Builder
		if err:=runRuntimePreflight(context.Background(),plan,&output,true);err==nil { t.Fatal("Guard skipped final config or isolation validation") }
	}
}
