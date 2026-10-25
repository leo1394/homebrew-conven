package materialize

import (
	"context"
	"crypto/sha256"
	"errors"
)

// HostSnapshot binds previously fetched YAML to the exact bootstrap reviewed by
// the host. It is an in-process preparation input, not a manifest trust flag.
type HostSnapshot struct {
	bootstrapDigest [32]byte
	application []byte
}

func NewHostSnapshot(bootstrap, application []byte) (*HostSnapshot, error) {
	if len(bootstrap) == 0 || len(application) == 0 || int64(len(application)) > maxApolloResponseBytes {
		return nil, errors.New("host snapshot requires bootstrap and bounded application content")
	}
	if _, err := decodeStrictSingleYAML(bootstrap, "host bootstrap"); err != nil {
		return nil, errors.New("host snapshot bootstrap is not valid YAML")
	}
	if _, err := decodeStrictSingleYAML(application, "host application"); err != nil {
		return nil, errors.New("host snapshot application is not valid YAML")
	}
	return &HostSnapshot{bootstrapDigest: sha256.Sum256(bootstrap), application: append([]byte(nil), application...)}, nil
}

func (snapshot *HostSnapshot) Application(ctx context.Context, input SourceInput) ([]byte, error) {
	if err := ctx.Err(); err != nil { return nil, err }
	if snapshot == nil || sha256.Sum256(input.Bootstrap) != snapshot.bootstrapDigest {
		return nil, errors.New("host configuration snapshot does not match the planned bootstrap; prepare again")
	}
	return append([]byte(nil), snapshot.application...), nil
}
