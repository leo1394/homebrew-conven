//go:build darwin

package materialize

import "golang.org/x/sys/unix"

func ensureAtomicPublicationSupported() error {
	return nil
}

func atomicSwapDirectories(staging string, target string) error {
	return unix.RenamexNp(staging, target, unix.RENAME_SWAP)
}
