package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSafeOutputSplitCredential(t *testing.T) {
	var result bytes.Buffer
	w := &safeOutput{target:&result}
	w.Write([]byte("request http://example.test/auth?app_secret=super"))
	if result.Len() != 0 { t.Fatal("incomplete line exposed") }
	w.Write([]byte("secret\n")); w.flush()
	if strings.Contains(result.String(), "supersecret") { t.Fatal("credential exposed") }
}

func TestSafeOutputBoundsUnterminatedLine(t *testing.T) {
	var result bytes.Buffer
	w := &safeOutput{target:&result}
	w.Write(bytes.Repeat([]byte("x"), 100000)); w.flush()
	if len(w.pending) != 0 || !strings.Contains(result.String(), "omitted") || result.Len() > 100 { t.Fatal("unbounded output") }
}

func TestHostStartArguments(t *testing.T) {
	valid := []string{"-C", "/workspace", "services", "--start", "--env", "test", "api"}
	args, enabled, err := hostStartArguments(valid)
	if err != nil || enabled || len(args) != len(valid) { t.Fatal("ordinary commands must not opt in") }
	args, enabled, err = hostStartArguments(append([]string{"--unprotected-start"}, valid...))
	if err != nil || !enabled || strings.Join(args, " ") != strings.Join(valid, " ") { t.Fatal("explicit host startup must opt in") }
	for _, invalid := range [][]string{
		{"--unprotected-start"},
		{"--unprotected-start", "--viewer", "/workspace"},
		{"--unprotected-start", "-C", "/workspace", "services", "--stop-all", "--env", "test", "api"},
		{"--unprotected-start", "-C", "/workspace", "services", "--start", "--env", "test"},
		{"--unprotected-start", "-C", "/workspace", "services", "--start", "--env", "test", "api", "--restart"},
	} {
		if _, enabled, err := hostStartArguments(invalid); err == nil || enabled { t.Fatalf("accepted invalid opt-in: %v", invalid) }
	}
}
