//go:build linux

package materialize

import "golang.org/x/sys/unix"

func ensureAtomicPublicationSupported() error {
	return nil
}

func atomicSwapDirectories(staging string, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE)
}
