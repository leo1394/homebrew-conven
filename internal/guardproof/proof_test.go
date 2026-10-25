package guardproof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestEvidenceIsBoundToImportAndDenyAll(t *testing.T) {
	manifest := []byte("version: 3\n")
	hash := sha256.Sum256(manifest)
	e := Evidence{1, "deny-all", "/work", hex.EncodeToString(hash[:]), []string{"api"}}
	data, _ := json.Marshal(e)
	if err := validate(data, manifest, "/work", "api"); err != nil { t.Fatal(err) }
	for _, tc := range []struct { data, manifest []byte; workspace, service string }{
		{data, []byte("changed"), "/work", "api"},
		{data, manifest, "/other", "api"},
		{data, manifest, "/work", "unselected"},
		{append(append([]byte{}, data...), []byte("{}")...), manifest, "/work", "api"},
	} { if validate(tc.data, tc.manifest, tc.workspace, tc.service) == nil { t.Fatal("accepted mismatched evidence") } }
	e.Mode = "approved-egress"; data, _ = json.Marshal(e)
	if validate(data, manifest, "/work", "api") == nil { t.Fatal("deny-all verifier accepted egress") }
}
