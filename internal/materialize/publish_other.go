//go:build !darwin && !linux

package materialize

import (
	"errors"
)

func ensureAtomicPublicationSupported() error {
	return errors.New("atomic materialization publication is unsupported on this platform")
}

func atomicSwapDirectories(staging string, target string) error {
	return errors.New("atomic materialization publication is unsupported on this platform")
}
