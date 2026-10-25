package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run against the exact bundled adapter; never uses a business workspace.
func TestHostStartObserveAndStop(t *testing.T) {
	binary := os.Getenv("CONVEN_GUARD_HOST_TEST_BINARY")
	if binary == "" { t.Skip("set CONVEN_GUARD_HOST_TEST_BINARY to a built adapter") }
	python, err := exec.LookPath("python3"); if err != nil { t.Skip("python3 fixture unavailable") }
	listener, err := net.Listen("tcp", "127.0.0.1:0"); if err != nil { t.Fatal(err) }
	port := listener.Addr().(*net.TCPAddr).Port; listener.Close()
	root := t.TempDir()
	for _, path := range []string{".conven", "api"} { if err := os.Mkdir(filepath.Join(root, path), 0700); err != nil { t.Fatal(err) } }
	source := `import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200); self.end_headers(); self.wfile.write(b'ok')
  print(json.dumps({'level':'INFO','message':'guard-observed-request'}),flush=True)
print('consumer-switch='+os.getenv('SERVICE_KAFKA_CONSUMERS_ENABLED','missing'),flush=True)
HTTPServer(('127.0.0.1',int(os.environ['PORT'])),Handler).serve_forever()
`
	if err := os.WriteFile(filepath.Join(root, "api", "server.py"), []byte(source), 0600); err != nil { t.Fatal(err) }
	manifest := fmt.Sprintf("version: 3\nworkspace: {name: guard-fixture}\nenvironments: {test: {}}\nservices:\n  api:\n    path: api\n    ports: {http: %d}\n    runner:\n      run: [%q, -u, server.py]\n    env: {PORT: %q}\n    healthChecks:\n      - {type: tcp, address: %q}\n", port, python, fmt.Sprint(port), fmt.Sprintf("127.0.0.1:%d", port))
	if err := os.WriteFile(filepath.Join(root, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil { t.Fatal(err) }
	run := func(args ...string) []byte {
		t.Helper(); cmd := exec.Command(binary, args...); cmd.Env = append(os.Environ(), "SERVICE_KAFKA_CONSUMERS_ENABLED=true")
		data, err := cmd.CombinedOutput(); if err != nil { t.Fatalf("adapter %v: %v %s", args, err, data) }; return data
	}
	defer run("-C", root, "services", "--stop-all")
	run("-C", root, "services", "--start", "--test", "api")
	client := &http.Client{Timeout:5*time.Second, Transport:&http.Transport{Proxy:nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port)); if err != nil { t.Fatal(err) }; response.Body.Close()
	if response.StatusCode != 200 { t.Fatal("HTTP fixture failed") }
	var viewer map[string]string
	if err := json.Unmarshal(run("--viewer", root), &viewer); err != nil { t.Fatal(err) }
	address, err := url.Parse(viewer["url"]); if err != nil { t.Fatal(err) }
	token := strings.TrimPrefix(address.Fragment, "token="); address.Fragment = ""; address.RawQuery = ""
	request := func(path string, authorized bool) (int, []byte) {
		t.Helper(); endpoint := *address; endpoint.Path = path
		req, _ := http.NewRequest("GET", endpoint.String(), nil)
		if authorized { req.Header.Set("Authorization", "Bearer " + token) }
		res, err := client.Do(req); if err != nil { t.Fatal(err) }; defer res.Body.Close()
		data, _ := io.ReadAll(res.Body); return res.StatusCode, data
	}
	if code, _ := request("/api/snapshot", false); code != 401 { t.Fatal("unauthenticated snapshot accepted") }
	healthy := false
	for deadline := time.Now().Add(15*time.Second); time.Now().Before(deadline); {
		code, data := request("/api/snapshot", true)
		if code == 200 && strings.Contains(string(data), `"state":"running"`) && strings.Contains(string(data), `"status":"healthy"`) { healthy = true; break }
		time.Sleep(100*time.Millisecond)
	}
	if !healthy { t.Fatal("runtime health did not converge") }
	if code, data := request("/api/logs", true); code != 200 || !strings.Contains(string(data), "guard-observed-request") || !strings.Contains(string(data), "consumer-switch=false") { t.Fatalf("missing collected logs/consumer default: %d %s", code, data) }
	run("-C", root, "services", "--stop-all")
	if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil { connection.Close(); t.Fatal("service survived stop") }
}
