package guardproof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestControlledSessionBindsEnvironmentManifestAndExactSelection(t *testing.T) {
	manifest := []byte("manifest")
	hash := sha256.Sum256(manifest)
	p := Session{Version: 1, Environment: "test", Workspace: "/work", ManifestSHA256: hex.EncodeToString(hash[:]), Services: map[string]Source{"api": {Bootstrap: []byte("bootstrap"), Application: []byte("application")}}}
	data, err := json.Marshal(map[string]any{"preparation": p, "network": map[string]any{}, "token": "not-runtime-authority"})
	if err != nil { t.Fatal(err) }
	if _, err := validateSession(data, manifest, "/work", "test", []string{"api"}); err != nil { t.Fatal(err) }
	for _, tc := range []struct { manifest []byte; workspace, environment string; selected []string }{
		{[]byte("changed"), "/work", "test", []string{"api"}},
		{manifest, "/other", "test", []string{"api"}},
		{manifest, "/work", "dev", []string{"api"}},
		{manifest, "/work", "test", []string{"other"}},
		{manifest, "/work", "test", nil},
		{manifest, "/work", "test", []string{"api", "api"}},
	} {
		if _, err := validateSession(data, tc.manifest, tc.workspace, tc.environment, tc.selected); err == nil { t.Fatal("accepted mismatched session") }
	}
}
