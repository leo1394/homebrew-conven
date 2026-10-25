package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuardConnectionRequestRequiresPinnedConfig(t *testing.T) {
	for _, argv := range [][]string{nil, {"ktctl", "connect"}, {"ktctl", "--kubeconfig"}, {"ktctl", "--kubeconfig", "one", "--kubeconfig", "two"}} {
		if _, err := guardConnectionRequest(argv); err == nil { t.Fatalf("accepted %v", argv) }
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0600); err != nil { t.Fatal(err) }
	binary := filepath.Join(t.TempDir(), "ktctl")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil { t.Fatal(err) }
	request, err := guardConnectionRequest([]string{binary, "--kubeconfig", path, "--context", "test", "--namespace", "test", "connect"})
	if err != nil { t.Fatal(err) }
	if request["operation"] != "start" || len(request["id"].(string)) != 36 { t.Fatal("missing ownership identity") }
	for _, arg := range request["arguments"].([]string) { if arg == path || arg == "--kubeconfig" { t.Fatal("host path sent to root helper") } }
	if _, ok := request["kubeconfig"].(map[string]any); !ok { t.Fatal("config must be transferred as data") }
	if request["sourceDigest"] != "f16d05ec6b29248d2c61adb1e9263f78e4f7bace1b955014a2d17872cfe4064d" { t.Fatalf("workspace executable was not hashed") }
}
