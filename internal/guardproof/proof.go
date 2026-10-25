// Package guardproof recognizes the root-owned, deny-all Guard VM boundary.
// It does not accept environment flags as isolation evidence.
package guardproof

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

type Evidence struct {
	Version int `json:"version"`
	Mode string `json:"mode"`
	Workspace string `json:"workspace"`
	ManifestSHA256 string `json:"manifestSHA256"`
	Services []string `json:"services"`
}

func validate(data, manifest []byte, workspace, service string) error {
	var e Evidence
	d := json.NewDecoder(bytes.NewReader(data)); d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil { return fmt.Errorf("invalid Guard evidence") }
	if d.Decode(new(any)) != io.EOF { return fmt.Errorf("invalid trailing Guard evidence") }
	hash := sha256.Sum256(manifest)
	if e.Version != 1 || e.Mode != "deny-all" || e.Workspace != workspace || e.ManifestSHA256 != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("Guard evidence does not match this workspace or deny-all policy; refresh the protected snapshot")
	}
	for _, allowed := range e.Services { if allowed == service { return nil } }
	return fmt.Errorf("service %q is outside the protected import selection", service)
}
