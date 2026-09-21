package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
)

func TestWebDashboardKeepsTerminalDashboard(t *testing.T) {
	for _, start := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing-session", true: "start-session"}[start], func(t *testing.T) {
			root := environmentShortcutWorkspace(t)
			workspace, err := convenruntime.OpenWorkspace(convenruntime.CommonOptions{Cwd: root})
			if err != nil { t.Fatal(err) }
			if start {
				t.Cleanup(func() { _ = convenruntime.Stop(context.Background(), workspace, nil, true, false, io.Discard) })
			} else if err := workspace.Store.Save(&convenruntime.Session{Workspace: root, Environment: "test", Selected: []string{"api"}}); err != nil { t.Fatal(err) }
			var output bytes.Buffer
			var opened []string
			app := App{Cwd: root, Output: &output, Error: &output,
				TerminalDashboardAvailable: func(*os.File, io.Writer) bool { return true },
				WebDashboardOpener: func(context.Context, *convenruntime.WorkspaceData, string, []string, bool) (string, error) {
					opened = append(opened, "web"); return "http://127.0.0.1:12345/#token=test", nil
				},
				BrowserOpener: func(context.Context, string) error { return nil },
				TerminalDashboardOpener: func(_ context.Context, _ *convenruntime.WorkspaceData, session *convenruntime.Session, _ convenruntime.TailOptions, _ *os.File, _ io.Writer) error {
					if session == nil || session.Environment != "test" { t.Fatalf("wrong session: %#v", session) }
					opened = append(opened, "tui"); return nil
				},
			}
			args := []string{"services", "--dashboard", "--web", "api"}
			if start { args = []string{"services", "--start", "--test", "--skip-verify", "--dashboard", "--web", "api"} }
			if code := app.Run(args); code != 0 { t.Fatalf("code=%d: %s", code, output.String()) }
			if strings.Join(opened, ",") != "web,tui" { t.Fatalf("view order = %v", opened) }
			if session, err := workspace.Store.Load(); err != nil || session == nil { t.Fatalf("closing viewers cleared session: %v", err) }
		})
	}
}

func TestWebDashboardReusesWorkspaceWithoutStartingServices(t *testing.T) {
	workspace := environmentShortcutWorkspace(t)
	for _, args := range [][]string{{"services","--dashboard","--web","api"},{"services","--diagnose","api"}} {
		var output, stderr bytes.Buffer
		opened:=false
		app:=App{Cwd:workspace,Output:&output,Error:&stderr,
			WebDashboardOpener:func(ctx context.Context, ws *convenruntime.WorkspaceData, executable string,names []string,diagnose bool)(string,error){
				opened=true
				if ws.Root!=workspace || len(names)!=1 || names[0]!="api" || diagnose!=(args[1]=="--diagnose") { t.Fatalf("unexpected request: %s %v %v",ws.Root,names,diagnose) }
				session,err:=ws.Store.Load()
				if err!=nil || session!=nil { t.Fatalf("viewer created session: %v %v",session,err) }
				return "http://127.0.0.1:12345/#token=example",nil
			}, BrowserOpener:func(context.Context,string)error{return errors.New("no browser")},
		}
		if code:=app.Run(args);code!=0 || !opened { t.Fatalf("code=%d opened=%v stderr=%s",code,opened,stderr.String()) }
		if !strings.Contains(output.String(),"Open the URL above") { t.Fatal(output.String()) }
	}
}

func TestWebDashboardFlagConflicts(t *testing.T) {
	for _,args:=range [][]string{
		{"services","--start","--web","api"},
		{"services","--start","--dashboard","--web","--tail","api"},
		{"services","--start","--dashboard","--web","--dry-run","api"},
		{"services","--diagnose","--start","api"},
		{"services","--dashboard","--web","--test"},
	} {
		var output bytes.Buffer
		if code:=(App{Cwd:t.TempDir(),Output:&output,Error:&output}).Run(args);code==0 { t.Fatalf("accepted %v",args) }
	}
}

func TestWebDashboardHelpNeedsNoWorkspace(t *testing.T) {
	for _,args:=range [][]string{{"services","--diagnose","--help"},{"services","--dashboard","--web","--help"}} {
		var output bytes.Buffer
		if code:=(App{Cwd:t.TempDir(),Output:&output,Error:&output}).Run(args);code!=0 { t.Fatalf("%v: %s",args,output.String()) }
	}
}

func TestFailedWebStartOpensDiagnosticsAndKeepsFailureExit(t *testing.T) {
	workspace:=environmentShortcutWorkspace(t)
	var output bytes.Buffer
	opened:=false
	app:=App{Cwd:workspace,Output:&output,Error:&output,
		WebDashboardOpener:func(ctx context.Context,ws *convenruntime.WorkspaceData,executable string,names []string,diagnose bool)(string,error){
			opened=diagnose
			attempts,err:=convenruntime.ReadDiagnostics(ws)
			if err!=nil || len(attempts)!=1 || attempts[0].Status!="failed" {t.Fatalf("missing failure: %v %v",attempts,err)}
			return "http://127.0.0.1:12345/#token=test",nil
		},BrowserOpener:func(context.Context,string)error{return nil},
	}
	if code:=app.Run([]string{"services","--start","--test","--dashboard","--web","missing-service"});code==0 || !opened {
		t.Fatalf("code=%d opened=%v output=%s",code,opened,output.String())
	}
}
