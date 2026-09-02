package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestCoreModulesDoNotBranchOnConcreteFrameworks(t *testing.T) {
	files := []string{"plan.go", "orchestrator.go", "state.go", "dashboard.go"}
	frameworks := []string{"go-zero", "spring-boot", "fastapi", "nestjs", "express", "fastify", "quarkus", "micronaut", "kratos", "hertz", "kitex", "bun-http"}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil { t.Fatal(err) }
		lower := strings.ToLower(string(data))
		for _, framework := range frameworks {
			if strings.Contains(lower, framework) {
				t.Fatalf("core module %s contains concrete framework %q; add an Analyzer, Certifier, or Adapter implementation instead", file, framework)
			}
		}
	}
}

func TestListenerVerificationModes(t *testing.T) {
	tests := []struct { address string; mode string; valid bool }{
		{"127.0.0.1", "loopback", true},
		{"::1", "loopback", true},
		{"0.0.0.0", "loopback", false},
		{"0.0.0.0", "all-interfaces", true},
		{"::", "all-interfaces", true},
		{"192.0.2.1", "all-interfaces", false},
	}
	for _, test := range tests {
		err := verifyListenerMode(test.address, test.mode)
		if (err == nil) != test.valid {
			t.Fatalf("verifyListenerMode(%q, %q) error=%v, valid=%t", test.address, test.mode, err, test.valid)
		}
	}
}

func TestRegistryDeltaIsDeterministic(t *testing.T) {
	before := map[string]RegistryInstance{"existing": {ID: "existing", Address: "10.0.0.1", Port: 8080}}
	after := map[string]RegistryInstance{
		"existing": {ID: "existing", Address: "10.0.0.1", Port: 8080},
		"z": {ID: "z", Address: "127.0.0.1", Port: 18081},
		"a": {ID: "a", Address: "127.0.0.1", Port: 18080},
	}
	added := addedRegistryInstances(before, after)
	if strings.Join(added, ",") != "a(127.0.0.1:18080),z(127.0.0.1:18081)" {
		t.Fatalf("added registry instances = %#v", added)
	}
}

func TestObservedAddressParsing(t *testing.T) {
	address, port, found := splitObservedAddress("[::1]:18080 (LISTEN)")
	if !found || address != "::1" || port != 18080 {
		t.Fatalf("observed address = %q:%d found=%t", address, port, found)
	}
}
