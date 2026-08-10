package materialize

import (
	"fmt"
	"os"
)

var removePublishedDirectory = os.RemoveAll

type PublishedCleanupError struct {
	Path string
	Err  error
}

func (err *PublishedCleanupError) Error() string {
	return fmt.Sprintf("materialized directory was published, but previous target cleanup at %q failed: %v", err.Path, err.Err)
}

func (err *PublishedCleanupError) Unwrap() error {
	return err.Err
}

func publishDirectory(staging string, target string) error {
	if err := ensureAtomicPublicationSupported(); err != nil {
		return err
	}
	_, err := os.Lstat(target)
	if os.IsNotExist(err) {
		if err := os.Rename(staging, target); err != nil {
			return fmt.Errorf("publish materialized directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect publication target: %w", err)
	}
	if err := atomicSwapDirectories(staging, target); err != nil {
		return fmt.Errorf("atomically replace materialized directory: %w", err)
	}
	if err := removePublishedDirectory(staging); err != nil {
		return &PublishedCleanupError{Path: staging, Err: err}
	}
	return nil
}
