package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestWebFavicon(t *testing.T) {
	server:=&webServer{instance:webInstance{URL:"http://127.0.0.1:54321"}}
	w:=httptest.NewRecorder()
	server.ServeHTTP(w,httptest.NewRequest("GET",server.instance.URL+"/favicon.png",nil))
	if w.Code!=200 || w.Header().Get("Content-Type")!="image/png" || !strings.HasPrefix(w.Body.String(),"\x89PNG\r\n\x1a\n") {t.Fatal("favicon must serve the embedded PNG without authentication")}
	w=httptest.NewRecorder()
	server.ServeHTTP(w,httptest.NewRequest("GET","http://evil.example/favicon.png",nil))
	if w.Code!=403 {t.Fatal("favicon bypassed host validation")}
	html,err:=webAssets.ReadFile("webassets/index.html")
	if err!=nil || !strings.Contains(string(html),`href="/favicon.png"`) {t.Fatal("page does not reference favicon")}
}

func TestWebServerAuthenticationAndOrigins(t *testing.T) {
	token:=strings.Repeat("a",64)
	server:=&webServer{workspace:&WorkspaceData{Root:"/workspace"},instance:webInstance{URL:"http://127.0.0.1:54321",Token:token,PID:1}}
	for _,test:=range []struct{path,host,origin,auth,method,csrf string;want int}{
		{"/api/identity","127.0.0.1:54321","","","GET","",401},
		{"/api/identity","evil.example","","Bearer "+token,"GET","",403},
		{"/api/identity","127.0.0.1:54321","http://evil.example","Bearer "+token,"GET","",403},
		{"/api/identity","127.0.0.1:54321","","Bearer "+token,"GET","",200},
		{"/api/action","127.0.0.1:54321","","Bearer "+token,"POST",token,403},
		{"/api/action","127.0.0.1:54321","http://127.0.0.1:54321","Bearer "+token,"POST","",403},
		{"/api/action","127.0.0.1:54321","http://127.0.0.1:54321","Bearer "+token,"POST",token,400},
		{"/etc/passwd","127.0.0.1:54321","","Bearer "+token,"GET","",404},
	} {
		r:=httptest.NewRequest(test.method,"http://"+test.host+test.path,strings.NewReader(`{"action":"shell"}`))
		r.Header.Set("Origin",test.origin);r.Header.Set("Authorization",test.auth);r.Header.Set("X-Conven-Token",test.csrf)
		w:=httptest.NewRecorder();server.ServeHTTP(w,r)
		if w.Code!=test.want {t.Fatalf("%+v: got %d body %s",test,w.Code,w.Body.String())}
		if w.Header().Get("Access-Control-Allow-Origin")!="" {t.Fatal("CORS enabled")}
	}
}

func TestWebAssetsAreEmbeddedAndPrivate(t *testing.T) {
	server:=&webServer{instance:webInstance{URL:"http://127.0.0.1:54321",Token:"secret"}}
	w:=httptest.NewRecorder();server.ServeHTTP(w,httptest.NewRequest("GET","http://127.0.0.1:54321/",nil))
	if w.Code!=200 || !strings.Contains(strings.ToLower(w.Body.String()),"<!doctype html>") { t.Fatalf("asset: %d",w.Code) }
	if strings.Contains(w.Body.String(),`token=secret`) { t.Fatal("token leaked into public asset") }
	if !strings.Contains(w.Header().Get("Content-Security-Policy"),"frame-ancestors 'none'") {t.Fatal("missing framing defense")}
}

func TestWebInstanceRejectsUntrustedRecords(t *testing.T) {
	root:=t.TempDir()
	workspace:=&WorkspaceData{Root:root,Store:&Store{Root:root}}
	path:=filepath.Join(root,"web.json")
	if err:=os.WriteFile(path,[]byte(`{}`),0644);err!=nil {t.Fatal(err)}
	if _,err:=readWebInstance(workspace);err==nil {t.Fatal("accepted public token record")}
	if err:=os.Remove(path);err!=nil {t.Fatal(err)}
	if err:=os.Symlink(filepath.Join(root,"other"),path);err!=nil {t.Fatal(err)}
	if _,err:=readWebInstance(workspace);err==nil {t.Fatal("accepted symlink")}
	ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
	if probeWebInstance(ctx,webInstance{Workspace:root,URL:"http://example.com",Token:strings.Repeat("a",64)},root) {t.Fatal("accepted nonlocal URL")}
}

func TestWebOperationRejectsOversizedAndConcurrentRequests(t *testing.T) {
	token:=strings.Repeat("b",64)
	server:=&webServer{instance:webInstance{URL:"http://127.0.0.1:54321",Token:token},busy:true}
	r:=httptest.NewRequest(http.MethodPost,server.instance.URL+"/api/action",strings.NewReader(`{"action":"refresh"}`))
	r.Header.Set("Origin",server.instance.URL);r.Header.Set("Authorization","Bearer "+token);r.Header.Set("X-Conven-Token",token)
	w:=httptest.NewRecorder();server.ServeHTTP(w,r)
	if w.Code!=409 {t.Fatalf("busy = %d",w.Code)}
}

func TestWebServerLifecycleDoesNotCreateOrClearServiceSession(t *testing.T) {
	workspace:=testWorkspace(t,t.TempDir(),&model.Manifest{Version:3})
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	result:=make(chan error,1)
	go func(){result<-ServeWebDashboard(ctx,workspace,"test","")}()
	var instance webInstance
	deadline:=time.Now().Add(5*time.Second)
	for time.Now().Before(deadline) {
		instance,_=readWebInstance(workspace)
		if probeWebInstance(ctx,instance,workspace.Root) {break}
		time.Sleep(10*time.Millisecond)
	}
	if !probeWebInstance(ctx,instance,workspace.Root) {t.Fatal("viewer not ready")}
	if err:=ServeWebDashboard(ctx,workspace,"test","");err==nil {t.Fatal("duplicate viewer accepted")}
	session:=&Session{Workspace:workspace.Root,Environment:"test",CreatedAt:time.Now()}
	if err:=workspace.Store.Save(session);err!=nil {t.Fatal(err)}
	cancel()
	select {
	case err:=<-result:if err!=nil {t.Fatal(err)}
	case <-time.After(5*time.Second):t.Fatal("viewer did not exit")
	}
	saved,err:=workspace.Store.Load()
	if err!=nil || saved==nil || saved.Environment!="test" {t.Fatalf("viewer changed session: %v %v",saved,err)}
	if _,err:=os.Stat(filepath.Join(workspace.Store.Root,"web.json"));!os.IsNotExist(err) {t.Fatalf("instance record retained: %v",err)}
}

func TestStopAllClosesWebDashboardWithoutSession(t *testing.T) {
	workspace:=testWorkspace(t,t.TempDir(),&model.Manifest{Version:3})
	other:=testWorkspace(t,t.TempDir(),&model.Manifest{Version:3})
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	result:=make(chan error,1)
	go func(){result<-ServeWebDashboard(ctx,workspace,"test","")}()
	var instance webInstance
	deadline:=time.Now().Add(5*time.Second)
	for time.Now().Before(deadline) {
		instance,_=readWebInstance(workspace)
		if probeWebInstance(ctx,instance,workspace.Root) {break}
		time.Sleep(10*time.Millisecond)
	}
	if !probeWebInstance(ctx,instance,workspace.Root) {t.Fatal("viewer not ready")}
	if err:=Stop(ctx,other,nil,true,false,nil);err!=nil {t.Fatal(err)}
	if !probeWebInstance(ctx,instance,workspace.Root) {t.Fatal("other workspace stopped viewer")}
	if err:=Stop(ctx,workspace,nil,true,false,nil);err!=nil {t.Fatal(err)}
	select {
	case err:=<-result:if err!=nil {t.Fatal(err)}
	case <-time.After(5*time.Second):t.Fatal("viewer did not exit")
	}
	if probeWebInstance(ctx,instance,workspace.Root) {t.Fatal("viewer still listening")}
	if _,err:=os.Stat(filepath.Join(workspace.Store.Root,"web.json"));!os.IsNotExist(err) {t.Fatalf("instance record retained: %v",err)}
	if err:=Stop(ctx,workspace,nil,true,false,nil);err!=nil {t.Fatal(err)}
}

func TestWebShutdownRequiresConfirmationAndRejectsBusy(t *testing.T) {
	token:=strings.Repeat("a",64)
	server:=&webServer{workspace:&WorkspaceData{Root:"/workspace"},instance:webInstance{URL:"http://127.0.0.1:54321",Token:token},busy:true,shutdown:func(){t.Error("busy viewer shut down")}}
	for _,csrf:=range []string{"",token} {
		r:=httptest.NewRequest("POST",server.instance.URL+"/api/shutdown",nil)
		r.Header.Set("Authorization","Bearer "+token)
		r.Header.Set("Origin",server.instance.URL)
		r.Header.Set("X-Conven-Token",csrf)
		w:=httptest.NewRecorder();server.ServeHTTP(w,r)
		want:=403
		if csrf!="" {want=409}
		if w.Code!=want {t.Fatalf("status=%d want=%d",w.Code,want)}
	}
}

func TestWebChildHelper(t *testing.T) {
	if os.Getenv("CONVEN_WEB_TEST_CHILD") != "1" {
		return
	}
	root := os.Getenv("CONVEN_WEB_TEST_WORKSPACE")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := &WorkspaceData{Root: root, Store: store, Manifest: &model.Manifest{Version: 3}}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	if err := ServeWebDashboard(ctx, workspace, "test", ""); err != nil {
		t.Fatal(err)
	}
}

func TestOpenWebDashboardDetachesAndReusesAuthenticatedViewer(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 3})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_WEB_TEST_CHILD", "1")
	t.Setenv("CONVEN_WEB_TEST_WORKSPACE", workspace.Root)
	t.Setenv("CONVEN_WEB_TEST_BINARY", executable)
	wrapper := filepath.Join(t.TempDir(), "viewer-test")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$CONVEN_WEB_TEST_BINARY\" -test.run '^TestWebChildHelper$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	address, err := OpenWebDashboard(ctx, workspace, wrapper, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := readWebInstance(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		process, _ := os.FindProcess(instance.PID)
		if process != nil {
			_ = process.Signal(syscall.SIGTERM)
		}
		deadline := time.Now().Add(3 * time.Second)
		for ProcessAlive(instance.PID) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if ProcessAlive(instance.PID) && process != nil {
			_ = process.Kill()
			t.Error("test viewer did not terminate")
		}
	}()
	second, err := OpenWebDashboard(ctx, workspace, "/not/an/executable", nil, false)
	if err != nil || second != address {
		t.Fatalf("existing viewer not reused: %q %v", second, err)
	}
	if session, err := workspace.Store.Load(); err != nil || session != nil {
		t.Fatalf("viewer changed service session: %v %v", session, err)
	}
}

func TestWebObservationsKeepCurrentHealthSeparateAndBounded(t *testing.T) {
	server := &webServer{}
	snapshot := WebSnapshotData{Services: []WebService{{Name: "api", PID: 1, State: "running", Verification: "verified", Health: WebHealth{Status: "unknown"}}}}
	events := server.observe(snapshot)
	if len(events) != 1 || events[0].Type != "process" {
		t.Fatalf("startup evidence was presented as current health: %#v", events)
	}
	if len(server.observe(snapshot)) != 1 {
		t.Fatal("unchanged snapshot produced a transition")
	}
	snapshot.Services[0].Health = WebHealth{Status: "unhealthy", CheckedAt: time.Now(), Message: "probe unavailable"}
	events = server.observe(snapshot)
	if len(events) != 2 || events[0].Type != "health" || events[0].Status != "unhealthy" {
		t.Fatalf("missing health transition: %#v", events)
	}
	for index := 0; index < 250; index++ {
		snapshot.Services[0].PID = index + 2
		server.observe(snapshot)
	}
	if len(server.events) != 200 {
		t.Fatal("observation history is not bounded")
	}
}
