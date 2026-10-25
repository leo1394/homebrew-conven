//go:build !linux

package guardproof

func DeniesAll(workspace, manifestPath, service string) (bool, error) { return false, nil }
