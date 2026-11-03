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

func TestControlledSessionAcceptsGuardIngressSchema(t *testing.T) {
	manifest := []byte("manifest"); hash := sha256.Sum256(manifest)
	preparation := map[string]any{"version":1,"environment":"test","workspace":"/work","manifestSHA256":hex.EncodeToString(hash[:]),"services":map[string]Source{"api":{Bootstrap:[]byte("bootstrap"),Application:[]byte("application")}},"ingress":[]map[string]any{{"service":"api","kind":"http","address":"0.0.0.0","port":18084}}}
	data, _ := json.Marshal(map[string]any{"preparation":preparation,"network":map[string]any{},"token":"not-runtime-authority"})
	if _, err := validateSession(data, manifest, "/work", "test", []string{"api"}); err != nil { t.Fatal(err) }
}

func TestControlledSessionRejectsInvalidIngress(t *testing.T) {
	manifest := []byte("manifest"); hash := sha256.Sum256(manifest)
	for _, ingress := range []SessionIngress{{Service:"other",Kind:"http",Address:"0.0.0.0",Port:18084},{Service:"api",Kind:"http",Address:"10.0.0.1",Port:18084},{Service:"api",Kind:"http",Address:"0.0.0.0",Port:19092}} {
		p := Session{Version:1,Environment:"test",Workspace:"/work",ManifestSHA256:hex.EncodeToString(hash[:]),Services:map[string]Source{"api":{Bootstrap:[]byte("b"),Application:[]byte("a")}},Ingress:[]SessionIngress{ingress}}
		data, _ := json.Marshal(map[string]any{"preparation":p,"network":map[string]any{},"token":"not-runtime-authority"})
		if _, err := validateSession(data, manifest, "/work", "test", []string{"api"}); err == nil { t.Fatalf("invalid ingress accepted: %+v", ingress) }
	}
}
