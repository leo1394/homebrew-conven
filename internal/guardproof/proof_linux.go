package guardproof

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// DeniesAll is intentionally limited to a VM with no dependency egress. A future
// approved-egress backend needs its own authenticated policy; it cannot reuse this proof.
func DeniesAll(workspace, manifestPath, service string) (bool, error) {
	const path = "/run/conven-guard/deny-all.json"
	if _, err := os.Lstat(path); os.IsNotExist(err) { return false, nil } else if err != nil { return false, err }
	for _, name := range []string{"/", "/run", "/run/conven-guard", path} {
		info, err := os.Lstat(name); if err != nil { return false, err }
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("Guard evidence must be owned by root and not writable by the guest")
		}
		if name == path && (!info.Mode().IsRegular() || info.Size() > 65536) { return false, fmt.Errorf("invalid Guard evidence file") }
	}
	interfaces, err := net.Interfaces(); if err != nil { return false, err }
	for _, iface := range interfaces { if iface.Name != "lo" { return false, fmt.Errorf("Guard deny-all proof rejected: external network interface present") } }
	if filepath.Clean(manifestPath) != filepath.Join(workspace, ".conven", "conven.yaml") { return false, fmt.Errorf("Guard requires the imported workspace manifest") }
	data, err := os.ReadFile(path); if err != nil { return false, err }
	manifest, err := os.ReadFile(manifestPath); if err != nil { return false, err }
	if err := validate(data, manifest, workspace, service); err != nil { return false, err }
	return true, nil
}
