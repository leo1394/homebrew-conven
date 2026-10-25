package guardproof

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// LoadSession recognizes only PID 1's boot-bound policy installation. An
// environment variable, workspace file, or ordinary host terminal cannot opt in.
func LoadSession(workspace, manifestPath, environment string, services []string) (*Session, error) {
	const path = "/run/conven-guard/session.json"
	if _, err := os.Lstat(path); os.IsNotExist(err) { return nil, nil } else if err != nil { return nil, err }
	for _, name := range []string{"/", "/run", "/run/conven-guard", path} {
		info, err := os.Lstat(name); if err != nil { return nil, err }
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Mode()&os.ModeSymlink != 0 { return nil, fmt.Errorf("Guard session evidence must be root-owned and immutable to the guest") }
		if name == path && (!info.Mode().IsRegular() || info.Size() > 32 << 20) { return nil, fmt.Errorf("invalid Guard session file") }
	}
	interfaces, err := net.Interfaces(); if err != nil { return nil, err }
	for _, iface := range interfaces { if iface.Name != "lo" { return nil, fmt.Errorf("Guard session rejected: external NIC present") } }
	if filepath.Clean(manifestPath) != filepath.Join(workspace, ".conven", "conven.yaml") { return nil, fmt.Errorf("Guard requires the imported manifest") }
	file, err := os.Open(path); if err != nil { return nil, err }; defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (32 << 20) + 1)); if err != nil || len(data) > 32 << 20 { return nil, fmt.Errorf("Guard session exceeds limit") }
	digest := sha256.Sum256(data)
	cmdline, err := os.ReadFile("/proc/cmdline"); if err != nil { return nil, err }
	bound := false
	for _, flag := range strings.Fields(string(cmdline)) { if flag == "guard_session=" + hex.EncodeToString(digest[:]) { bound = true } }
	if !bound { return nil, fmt.Errorf("Guard session is not bound to this VM boot") }
	manifest, err := os.ReadFile(manifestPath); if err != nil { return nil, err }
	return validateSession(data, manifest, workspace, environment, services)
}
