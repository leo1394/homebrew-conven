package runtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leo1394/homebrew-conven/assets"
	"golang.org/x/sys/unix"
)

//go:embed webassets/index.html
var webAssets embed.FS

type webInstance struct {
	Workspace string `json:"workspace"`
	URL       string `json:"url"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
}

// OpenWebDashboard starts only a detached viewer, never a business service.
func OpenWebDashboard(ctx context.Context, workspace *WorkspaceData, executable string, names []string, diagnose bool) (string, error) {
	if err := workspace.Store.ensureRoot(); err != nil {
		return "", err
	}
	knownNames := make(map[string]bool)
	for name := range workspace.Manifest.Services {
		knownNames[name] = true
	}
	if session, err := workspace.Store.Load(); err == nil && session != nil {
		for _, process := range session.Services {
			knownNames[process.Name] = true
		}
	}
	if attempts, err := ReadDiagnostics(workspace); err == nil {
		for _, attempt := range attempts {
			for _, name := range attempt.Services {
				knownNames[name] = true
			}
		}
	}
	for _, name := range names {
		if !knownNames[name] {
			return "", fmt.Errorf("unknown service %q", name)
		}
	}
	instance, err := readWebInstance(workspace)
	if err != nil || !probeWebInstance(ctx, instance, workspace.Root) {
		if executable == "" {
			return "", errors.New("cannot launch Web dashboard without the Conven executable")
		}
		command := exec.Command(executable, "-C", workspace.Root, "__web-dashboard")
		command.Dir = workspace.Root
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// No inherited terminal or pipe: the viewer outlives this command.
		if err := command.Start(); err != nil {
			return "", fmt.Errorf("launch Web dashboard: %w", err)
		}
		go command.Wait()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			instance, err = readWebInstance(workspace)
			if err == nil && probeWebInstance(ctx, instance, workspace.Root) {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-timer.C:
				return "", errors.New("Web dashboard did not become ready; services were left unchanged")
			case <-ticker.C:
			}
		}
	}
	query := url.Values{}
	if len(names) > 0 {
		query.Set("services", strings.Join(names, ","))
	}
	if diagnose {
		query.Set("view", "timeline")
	}
	return instance.URL + "/?" + query.Encode() + "#token=" + instance.Token, nil
}

func readWebInstance(workspace *WorkspaceData) (webInstance, error) {
	var instance webInstance
	fd, err := unix.Open(filepath.Join(workspace.Store.Root, "web.json"), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return instance, err
	}
	file := os.NewFile(uintptr(fd), "web.json")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return instance, errors.New("unsafe Web instance record")
	}
	err = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&instance)
	return instance, err
}

func probeWebInstance(ctx context.Context, instance webInstance, workspace string) bool {
	if instance.Workspace != workspace || len(instance.Token) != 64 {
		return false
	}
	u, err := url.Parse(instance.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, "GET", instance.URL+"/api/identity", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+instance.Token)
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var identity struct {
		Workspace string `json:"workspace"`
		PID       int    `json:"pid"`
	}
	return response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&identity) == nil && identity.Workspace == workspace && identity.PID == instance.PID
}

// StopWebDashboard authenticates the workspace viewer instead of trusting a PID
// from disk. It also works when there is no business service session.
func StopWebDashboard(ctx context.Context, workspace *WorkspaceData) error {
	instance, err := readWebInstance(workspace)
	if os.IsNotExist(err) { return nil }
	if err != nil { return fmt.Errorf("read Web dashboard identity: %w", err) }
	if !probeWebInstance(ctx, instance, workspace.Root) {
		if instance.Workspace == workspace.Root && instance.PID > 0 && !ProcessAlive(instance.PID) { return nil }
		return errors.New("cannot authenticate workspace Web dashboard; refusing to stop an unverified process")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", instance.URL+"/api/shutdown", nil)
	if err != nil { return err }
	request.Header.Set("Authorization", "Bearer "+instance.Token)
	request.Header.Set("Origin", instance.URL)
	request.Header.Set("X-Conven-Token", instance.Token)
	client := &http.Client{Transport:&http.Transport{Proxy:nil},CheckRedirect:func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil { return fmt.Errorf("stop Web dashboard: %w", err) }
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Web dashboard refused shutdown (HTTP %d); finish active dashboard actions and retry; older viewers must be closed once manually", response.StatusCode)
	}
	ticker := time.NewTicker(25*time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := readWebInstance(workspace)
		if os.IsNotExist(err) || (err == nil && current.Token != instance.Token) { return nil }
		select {
		case <-ctx.Done(): return fmt.Errorf("wait for Web dashboard shutdown: %w",ctx.Err())
		case <-ticker.C:
		}
	}
}

type webServer struct {
	workspace  *WorkspaceData
	version    string
	executable string
	instance   webInstance
	mu         sync.Mutex
	lastClient time.Time
	busy       bool
	stopping   bool
	shutdown   func()
	operation  map[string]any
	observed   map[string]string
	events     []webObservation
}

type webObservation struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Type      string    `json:"type"`
	Service   string    `json:"service,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
}

func (server *webServer) observe(snapshot WebSnapshotData) []webObservation {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.observed == nil {
		server.observed = make(map[string]string)
	}
	record := func(kind, name, fingerprint, status, message string) {
		key := kind + ":" + name
		if server.observed[key] == fingerprint {
			return
		}
		server.observed[key] = fingerprint
		now := time.Now().UTC()
		event := webObservation{ID: fmt.Sprintf("observed-%d", now.UnixNano()), Time: now, Type: kind, Service: name, Status: status, Message: diagnosticSafeText(message, 4096)}
		server.events = append([]webObservation{event}, server.events...)
		if len(server.events) > 200 {
			server.events = server.events[:200]
		}
	}
	for _, service := range snapshot.Services {
		record("process", service.Name, fmt.Sprintf("%d:%s", service.PID, service.State), service.State, "Observed process state; not a health or isolation guarantee.")
		if !service.Health.CheckedAt.IsZero() {
			record("health", service.Name, fmt.Sprintf("%d:%s", service.PID, service.Health.Status), service.Health.Status, service.Health.Message)
		}
	}
	if snapshot.Connection != nil {
		connection := snapshot.Connection
		record("connection", "", fmt.Sprintf("%d:%s", connection.PID, connection.State), connection.State, "Observed connection process state; endpoint readiness is not continuously verified.")
	}
	return append([]webObservation{}, server.events...)
}

// ServeWebDashboard is the private child-process entry point. The separate lock
// is held for its lifetime; it never holds the workspace orchestration lock.
func ServeWebDashboard(ctx context.Context, workspace *WorkspaceData, version, executable string) error {
	if err := workspace.Store.ensureRoot(); err != nil { return err }
	fd, err := unix.Open(filepath.Join(workspace.Store.Root,"web.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW,0600)
	if err != nil { return err }
	lock := os.NewFile(uintptr(fd), "web.lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() { return errors.New("Web dashboard lock must be a regular file") }
	if err := lock.Chmod(0600); err != nil { return err }
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil { return err }
	defer unix.Flock(fd,unix.LOCK_UN)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil { return err }
	defer listener.Close()
	secret := make([]byte,32)
	if _, err := rand.Read(secret); err != nil { return err }
	instance := webInstance{Workspace:workspace.Root, URL:"http://"+listener.Addr().String(), Token:hex.EncodeToString(secret),PID:os.Getpid()}
	data, _ := json.Marshal(instance)
	file, err := os.CreateTemp(workspace.Store.Root,".web-*.json")
	if err != nil { return err }
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil { file.Close(); return err }
	if err = file.Close(); err != nil { return err }
	path := filepath.Join(workspace.Store.Root,"web.json")
	if err = os.Rename(file.Name(),path); err != nil { return err }
	defer os.Remove(path)
	viewer := &webServer{workspace:workspace,version:version,executable:executable,instance:instance,lastClient:time.Now()}
	server := &http.Server{Handler:viewer,ReadHeaderTimeout:3*time.Second,ReadTimeout:10*time.Second,WriteTimeout:15*time.Second,IdleTimeout:30*time.Second,MaxHeaderBytes:8192}
	serverContext, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownDone := make(chan struct{})
	viewer.shutdown = func() {
		defer close(shutdownDone)
		shutdownContext, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		server.Shutdown(shutdownContext)
	}
	go func() {
		ticker := time.NewTicker(10*time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-serverContext.Done(): return
			case <-ticker.C:
				viewer.mu.Lock()
				active := !viewer.busy && time.Since(viewer.lastClient)<time.Minute
				viewer.mu.Unlock()
				if active {
					probeContext,done:=context.WithTimeout(serverContext,8*time.Second)
					_,_ = RefreshWebHealth(probeContext,workspace)
					done()
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-serverContext.Done(): server.Close(); return
			case <-ticker.C:
				viewer.mu.Lock()
				idle := !viewer.busy && time.Since(viewer.lastClient)>30*time.Minute
				viewer.mu.Unlock()
				if idle { server.Close(); return }
			}
		}
	}()
	err = server.Serve(listener)
	viewer.mu.Lock()
	stopping := viewer.stopping
	viewer.mu.Unlock()
	if stopping { <-shutdownDone }
	if errors.Is(err,http.ErrServerClosed) { return nil }
	return err
}

func (server *webServer) ServeHTTP(w http.ResponseWriter,r *http.Request) {
	w.Header().Set("Cache-Control","no-store")
	w.Header().Set("X-Content-Type-Options","nosniff")
	w.Header().Set("Referrer-Policy","no-referrer")
	w.Header().Set("Content-Security-Policy","default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if r.Host != strings.TrimPrefix(server.instance.URL,"http://") || (r.Header.Get("Origin")!="" && r.Header.Get("Origin")!=server.instance.URL) {
		http.Error(w,"untrusted origin",http.StatusForbidden); return
	}
	if r.URL.Path=="/favicon.png" && r.Method=="GET" {
		w.Header().Set("Content-Type","image/png")
		w.Write(assets.Mark)
		return
	}
	if r.URL.Path=="/" && r.Method=="GET" {
		data,err := webAssets.ReadFile("webassets/index.html")
		if err != nil { http.Error(w,"dashboard asset unavailable",500);return }
		w.Header().Set("Content-Type","text/html; charset=utf-8");w.Write(data);return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")),[]byte("Bearer "+server.instance.Token))!=1 {
		http.Error(w,"authentication required",http.StatusUnauthorized);return
	}
	server.mu.Lock();server.lastClient=time.Now();server.mu.Unlock()
	w.Header().Set("Content-Type","application/json")
	if r.Method=="GET" {
		switch r.URL.Path {
		case "/api/identity": json.NewEncoder(w).Encode(map[string]any{"workspace":server.workspace.Root,"pid":server.instance.PID});return
		case "/api/snapshot":
			workspace,err := OpenWebWorkspace(CommonOptions{Cwd:server.workspace.Root})
			if err != nil { http.Error(w,"cannot locate workspace",500);return }
			snapshot,err := WebSnapshot(r.Context(),workspace,server.version)
			if err != nil { http.Error(w,diagnosticSafeText(err.Error(),4096),500);return }
			json.NewEncoder(w).Encode(struct {
				WebSnapshotData
				Events []webObservation `json:"events"`
			}{snapshot, server.observe(snapshot)});return
		case "/api/logs":
			q:=r.URL.Query()
			page,err := WebLogs(server.workspace,WebLogQuery{Services:splitWebNames(q.Get("services")),Cursor:q.Get("cursor"),Query:q.Get("query"),Level:q.Get("level"),RequestID:q.Get("requestId"),TraceID:q.Get("traceId"),Attempt:q.Get("attempt"),Since:q.Get("since"),Until:q.Get("until")})
			if err!=nil { http.Error(w,diagnosticSafeText(err.Error(),4096),400);return }
			json.NewEncoder(w).Encode(page);return
		case "/api/operation":
			server.mu.Lock();defer server.mu.Unlock();json.NewEncoder(w).Encode(server.operation);return
		}
	}
	if r.URL.Path=="/api/shutdown" && r.Method=="POST" {
		if r.Header.Get("Origin")!=server.instance.URL || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Conven-Token")),[]byte(server.instance.Token))!=1 {
			http.Error(w,"action confirmation token required",403);return
		}
		server.mu.Lock()
		if server.busy || server.stopping || server.shutdown == nil { server.mu.Unlock();http.Error(w,"dashboard is busy or stopping",409);return }
		server.stopping=true
		server.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		go server.shutdown()
		return
	}
	if r.URL.Path=="/api/action" && r.Method=="POST" { server.action(w,r);return }
	http.Error(w,"not found",404)
}

func splitWebNames(value string) []string {
	if value=="" { return nil };return strings.Split(value,",")
}

func (server *webServer) action(w http.ResponseWriter,r *http.Request) {
	if r.Header.Get("Origin")!=server.instance.URL || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Conven-Token")),[]byte(server.instance.Token))!=1 {
		http.Error(w,"action confirmation token required",403);return
	}
	var action struct { Action string `json:"action"`; Services []string `json:"services"`; SessionToken string `json:"sessionToken"` }
	decoder:=json.NewDecoder(http.MaxBytesReader(w,r.Body,8192));decoder.DisallowUnknownFields()
	if decoder.Decode(&action)!=nil { http.Error(w,"invalid action",400);return }
	if decoder.Decode(new(any))!=io.EOF { http.Error(w,"invalid trailing action data",400);return }
	if action.Action!="stop" && action.Action!="restart" && action.Action!="refresh" { http.Error(w,"unsupported action",400);return }
	if action.Action!="refresh" && (len(action.Services)==0 || action.SessionToken=="") { http.Error(w,"select current session services and confirm",400);return }
	server.mu.Lock()
	if server.busy || server.stopping { server.mu.Unlock();http.Error(w,"another dashboard action is active or dashboard is stopping",409);return }
	operationID := fmt.Sprintf("web-%d",time.Now().UnixNano())
	server.busy=true;server.operation=map[string]any{"id":operationID,"action":action.Action,"status":"running","startedAt":time.Now()};server.mu.Unlock()
	go func() {
		ctx,cancel:=context.WithTimeout(context.Background(),15*time.Minute);defer cancel()
		workspace,err:=OpenWorkspace(CommonOptions{Cwd:server.workspace.Root})
		if err==nil {
			switch action.Action {
			case "stop": err=StopWithSessionToken(ctx,workspace,action.Services,action.SessionToken,io.Discard)
			case "restart": _,err=Restart(ctx,workspace,RestartOptions{Services:action.Services,ExpectedSessionToken:action.SessionToken,HotReloadExecutable:server.executable,Output:io.Discard})
			case "refresh": _,err=RefreshWebHealth(ctx,workspace,true)
			}
		}
		server.mu.Lock();defer server.mu.Unlock();server.busy=false;server.operation["finishedAt"]=time.Now()
		if err!=nil { server.operation["status"]="failed";server.operation["error"]=diagnosticSafeText(err.Error(),4096) } else { server.operation["status"]="completed" }
	}()
	w.WriteHeader(http.StatusAccepted);json.NewEncoder(w).Encode(map[string]string{"id":operationID,"status":"accepted"})
}
