//go:build !linux

package guardproof

func LoadSession(workspace, manifestPath, environment string, services []string) (*Session, error) { return nil, nil }
