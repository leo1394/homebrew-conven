package dependency

import (
	"context"
	"net"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestCheckEndpointsUsesDeclaredAddressByDefault(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	declaration := model.Environment{Endpoints: map[string]model.EnvironmentEndpoint{
		"payment": {Address: listener.Addr().String()},
	}}
	resolutions := map[string]map[string]Resolution{
		"api": {"payment": {Mode: "endpoint", Target: "payment"}},
	}
	if err := CheckEndpoints(context.Background(), t.TempDir(), nil, declaration, resolutions); err != nil {
		t.Fatal(err)
	}
	if names := EndpointNames(resolutions); len(names) != 1 || names[0] != "payment" {
		t.Fatalf("endpoint names = %v", names)
	}
}
