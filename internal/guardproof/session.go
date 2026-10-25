package guardproof

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

type Source struct {
	Bootstrap []byte `json:"bootstrap"`
	Application []byte `json:"application"`
}

type Session struct {
	Version int `json:"version"`
	Environment string `json:"environment"`
	Workspace string `json:"workspace"`
	ManifestSHA256 string `json:"manifestSHA256"`
	Services map[string]Source `json:"services"`
}

func validateSession(data, manifest []byte, workspace, environment string, services []string) (*Session, error) {
	var bundle struct {
		Preparation Session `json:"preparation"`
		Network json.RawMessage `json:"network"`
		Token string `json:"token"`
	}
	d := json.NewDecoder(bytes.NewReader(data)); d.DisallowUnknownFields()
	if d.Decode(&bundle) != nil || d.Decode(new(any)) != io.EOF { return nil, fmt.Errorf("invalid Guard session evidence") }
	s := bundle.Preparation
	hash := sha256.Sum256(manifest)
	if s.Version != 1 || s.Workspace != workspace || s.Environment != environment || s.ManifestSHA256 != hex.EncodeToString(hash[:]) || len(s.Services) != len(services) || len(services) == 0 {
		return nil, fmt.Errorf("Guard session environment, selection or manifest changed; refresh the protected session")
	}
	seen := map[string]bool{}
	for _, name := range services {
		source, ok := s.Services[name]
		if !ok || seen[name] || len(source.Bootstrap) == 0 || len(source.Application) == 0 { return nil, fmt.Errorf("service outside the prepared Guard session") }
		seen[name] = true
	}
	return &s, nil
}
