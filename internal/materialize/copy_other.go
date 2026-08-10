//go:build !darwin && !linux

package materialize

import (
	"context"
	"errors"
)

func copySource(ctx context.Context, source string, target string) error {
	return errors.New("safe source copying is unsupported on this platform")
}
