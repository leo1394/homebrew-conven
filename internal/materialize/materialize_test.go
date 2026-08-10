package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestMaterializeRepositoryPatchesPrivatelyAndPreservesSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "old.txt"), []byte("old target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before := directoryHash(t, source)
	plan := testPlan(source, configRoot, target)
	plan.Patches = []Patch{
		{File: "application.yaml", Path: "server.port", Value: 18080},
		{File: "application.yaml", Path: "rpc.partner", Value: map[string]any{"target": "127.0.0.1:18081"}},
		{File: "config-local.yaml", Path: "localConfigEnable", Value: true},
		{File: "config-local.yaml", Path: "localConfigPath", Value: filepath.Join(target, "application.yaml")},
	}
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if after := directoryHash(t, source); after != before {
		t.Fatalf("source changed: before=%s after=%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(target, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old target survived publication: %v", err)
	}
	application := readYAMLMap(t, filepath.Join(target, "application.yaml"))
	server := application["server"].(map[string]any)
	if server["port"] != 18080 {
		t.Fatalf("server.port = %#v, want 18080", server["port"])
	}
	if server["name"] != "api" {
		t.Fatalf("repository application content was not preserved: %#v", server)
	}
	rpc := application["rpc"].(map[string]any)
	partner := rpc["partner"].(map[string]any)
	if partner["target"] != "127.0.0.1:18081" {
		t.Fatalf("rpc.partner patch missing: %#v", partner)
	}
	runtimeBootstrap := readYAMLMap(t, filepath.Join(target, "config-local.yaml"))
	if runtimeBootstrap["appId"] != "demo" || runtimeBootstrap["localConfigEnable"] != true {
		t.Fatalf("runtime bootstrap was not copied and patched: %#v", runtimeBootstrap)
	}
	if runtimeBootstrap["localConfigPath"] != filepath.Join(target, "application.yaml") {
		t.Fatalf("runtime localConfigPath = %#v", runtimeBootstrap["localConfigPath"])
	}
	assertPrivateTree(t, target)
}

func TestMaterializeAppliesGuardsAfterPatchesAndVerifiesScalars(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := applyYAMLPatch(filepath.Join(source, "application.yaml"), "server.registrationEnabled", true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Patches = []Patch{
		{File: "application.yaml", Path: "rpc.partner.discovType", Value: "consul"},
		{File: "application.yaml", Path: "server.registrationEnabled", Value: true},
	}
	plan.Guards = []Guard{
		{File: "application.yaml", Path: "rpc.partner.discovType", Value: ""},
		{File: "application.yaml", Path: "server.registrationEnabled", Value: false},
	}
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	application := readYAMLMap(t, filepath.Join(target, "application.yaml"))
	rpc := application["rpc"].(map[string]any)
	partner := rpc["partner"].(map[string]any)
	if partner["discovType"] != "" {
		t.Fatalf("guarded discovery type = %#v, want empty string", partner["discovType"])
	}
	server := application["server"].(map[string]any)
	if server["registrationEnabled"] != false {
		t.Fatalf("guarded registrationEnabled = %#v, want false", server["registrationEnabled"])
	}
	if err := VerifyGuards(target, plan.Guards); err != nil {
		t.Fatalf("verify published guards: %v", err)
	}
	if err := applyYAMLPatch(filepath.Join(target, "application.yaml"), "server.registrationEnabled", true); err != nil {
		t.Fatal(err)
	}
	if err := VerifyGuards(target, plan.Guards); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("VerifyGuards accepted a changed value: %v", err)
	}
}

func TestVerifyGuardsRejectsPrepareCreatedIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.WriteFile(filepath.Join(source, "nested", "application.yaml"), []byte("server:\n  registrationEnabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Guards = []Guard{{File: "nested/application.yaml", Path: "server.registrationEnabled", Value: false}}
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "prepare-output")
	if err := os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "application.yaml"), []byte("server:\n  registrationEnabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(target, "nested")
	if err := os.Rename(nested, filepath.Join(root, "original-nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, nested); err != nil {
		t.Fatal(err)
	}
	err := VerifyGuards(target, plan.Guards)
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("VerifyGuards accepted a prepare-created intermediate symlink: %v", err)
	}
}

func TestMaterializeStrictGuardRequiresSourcePathBeforePatches(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("previous target"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Patches = []Patch{{File: "application.yaml", Path: "server.registration.enabled", Value: false}}
	plan.Guards = []Guard{{File: "application.yaml", Path: "server.registration.enabled", Value: false}}
	err := Materialize(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), `YAML guard path "server.registration.enabled" does not exist`) {
		t.Fatalf("strict guard missing-path error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(target, "sentinel"))
	if readErr != nil || string(data) != "previous target" {
		t.Fatalf("previous target changed after guard failure: data=%q err=%v", data, readErr)
	}
}

func TestMaterializeGuardCanExplicitlyCreatePath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Guards = []Guard{{
		File:        "application.yaml",
		Path:        "server.registration.enabled",
		Value:       false,
		AllowCreate: true,
	}}
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	application := readYAMLMap(t, filepath.Join(target, "application.yaml"))
	server := application["server"].(map[string]any)
	registration := server["registration"].(map[string]any)
	if registration["enabled"] != false {
		t.Fatalf("created guard value = %#v, want false", registration["enabled"])
	}
}

func TestMaterializeGuardRejectsAmbiguousMappingKeys(t *testing.T) {
	tests := []struct {
		name        string
		application string
		want        string
	}{
		{
			name: "tagged duplicate",
			application: "server:\n  registrationEnabled: true\n" +
				"!unsafe server:\n  registrationEnabled: true\n",
			want: "unsupported mapping key",
		},
		{
			name: "merge key",
			application: "defaults: &defaults\n  registrationEnabled: true\n" +
				"server:\n  <<: *defaults\n",
			want: "unsupported merge key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			configRoot := filepath.Join(root, "configs")
			target := filepath.Join(configRoot, "api")
			writeTestSource(t, source, "http://unused.invalid")
			if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte(test.application), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(configRoot, 0700); err != nil {
				t.Fatal(err)
			}
			plan := testPlan(source, configRoot, target)
			plan.Guards = []Guard{{File: "application.yaml", Path: "server.registrationEnabled", Value: false}}
			err := Materialize(context.Background(), plan)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("guard mapping error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMaterializeRejectsNonScalarGuardValue(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Guards = []Guard{{
		File:  "application.yaml",
		Path:  "rpc.partner",
		Value: map[string]any{"discovType": ""},
	}}
	err := Materialize(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "YAML guard value must be a non-null scalar") {
		t.Fatalf("non-scalar guard error = %v", err)
	}
}

func TestMaterializeConflictingScalarGuardsFailExactlyAndKeepTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("previous target"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Guards = []Guard{
		{File: "application.yaml", Path: "server.port", Value: false},
		{File: "application.yaml", Path: "server.port", Value: "false"},
	}
	err := Materialize(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("conflicting guards error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(target, "sentinel"))
	if readErr != nil || string(data) != "previous target" {
		t.Fatalf("previous target changed after exact guard failure: data=%q err=%v", data, readErr)
	}
}

func TestMaterializeRepositoryPreservesApplicationWithoutBootstrap(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(source, "application.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.Bootstrap = ""
	plan.RuntimeBootstrap = ""
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(target, "application.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("repository application changed without patches:\n%s", after)
	}
}

func TestMaterializeApolloRetriesAndReplacesApplication(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/configs/demo/dev/application.yml" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("ip") != "127.0.0.1" {
			t.Errorf("request ip = %q", request.URL.Query().Get("ip"))
		}
		if requests.Add(1) == 1 {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(map[string]any{
			"configurations": map[string]string{
				"content": "server:\n  name: apollo\n  port: 9000\n",
			},
		})
	}))
	defer server.Close()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, server.URL)
	if err := os.Remove(filepath.Join(source, "application.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(source, configRoot, target)
	plan.SourceDriver = SourceApollo
	plan.Application = "generated/application.yaml"
	plan.Apollo = Apollo{Attempts: 2, RetryDelay: time.Millisecond, Timeout: time.Second}
	plan.Patches = []Patch{{File: "generated/application.yaml", Path: "server.port", Value: 18080}}
	if err := Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("Apollo requests = %d, want 2", requests.Load())
	}
	application := readYAMLMap(t, filepath.Join(target, "generated", "application.yaml"))
	serverConfig := application["server"].(map[string]any)
	if serverConfig["name"] != "apollo" || serverConfig["port"] != 18080 {
		t.Fatalf("Apollo application was not fetched then patched: %#v", serverConfig)
	}
}

func TestApolloRetriesRequestTimeoutAndRateLimit(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if requests.Add(1) == 1 {
					writer.Header().Set("Retry-After", "0")
					writer.WriteHeader(status)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				json.NewEncoder(writer).Encode(map[string]any{
					"configurations": map[string]string{
						"content": "server:\n  name: apollo\n",
					},
				})
			}))
			defer server.Close()
			bootstrap := fmt.Sprintf("appId: demo\ncluster: dev\nip: %s\nnamespaceName: application.yml\n", server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			application, err := (ApolloAdapter{}).Application(ctx, SourceInput{
				Bootstrap: []byte(bootstrap),
				Apollo: Apollo{Attempts: 2, RetryDelay: 10 * time.Second, Timeout: time.Second},
			})
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 {
				t.Fatalf("Apollo requests = %d, want 2", requests.Load())
			}
			if !strings.Contains(string(application), "name: apollo") {
				t.Fatalf("Apollo application = %q", application)
			}
		})
	}
}

func TestParseRetryAfterCapsServerDelay(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "seconds", value: "12", want: 12 * time.Second, ok: true},
		{name: "seconds capped", value: "120", want: maxApolloRetryAfter, ok: true},
		{name: "date", value: now.Add(7 * time.Second).Format(http.TimeFormat), want: 7 * time.Second, ok: true},
		{name: "date capped", value: now.Add(time.Minute).Format(http.TimeFormat), want: maxApolloRetryAfter, ok: true},
		{name: "past date", value: now.Add(-time.Minute).Format(http.TimeFormat), want: 0, ok: true},
		{name: "invalid", value: "later", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseRetryAfter(test.value, now)
			if ok != test.ok || got != test.want {
				t.Fatalf("parseRetryAfter(%q) = (%s, %t), want (%s, %t)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestApolloRejectsResponseLargerThanLimit(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(io.LimitReader(repeatingByteReader{}, maxApolloResponseBytes+1)),
			Request:    request,
		}, nil
	})}
	_, err := (ApolloAdapter{Client: client}).Application(context.Background(), SourceInput{
		Bootstrap: []byte("appId: demo\ncluster: dev\nip: http://apollo.invalid\nnamespaceName: application.yml\n"),
		Apollo: Apollo{Timeout: time.Second},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized Apollo response error = %v", err)
	}
}

func TestApolloRejectsAmbiguousBootstrapBeforeNetwork(t *testing.T) {
	tests := []struct {
		name      string
		bootstrap string
	}{
		{
			name:      "duplicate key",
			bootstrap: "appId: demo\nappId: other\ncluster: dev\nip: http://apollo.invalid\nnamespaceName: application.yml\n",
		},
		{
			name:      "multiple documents",
			bootstrap: "appId: demo\ncluster: dev\nip: http://apollo.invalid\nnamespaceName: application.yml\n---\nappId: other\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"configurations":{"content":"server: {}\n"}}`)),
					Request:    request,
				}, nil
			})}
			_, err := (ApolloAdapter{Client: client}).Application(context.Background(), SourceInput{
				Bootstrap: []byte(test.bootstrap),
				Apollo:    Apollo{Timeout: time.Second},
			})
			if err == nil {
				t.Fatal("ambiguous Apollo bootstrap unexpectedly succeeded")
			}
			if requests.Load() != 0 {
				t.Fatalf("Apollo made %d request(s) before rejecting bootstrap", requests.Load())
			}
		})
	}
}

func TestMaterializeRejectsSymlinksAndTargetEscape(t *testing.T) {
	t.Run("source root symlink", func(t *testing.T) {
		root := t.TempDir()
		realSource := filepath.Join(root, "real-source")
		writeTestSource(t, realSource, "http://unused.invalid")
		source := filepath.Join(root, "source")
		if err := os.Symlink(realSource, source); err != nil {
			t.Fatal(err)
		}
		configRoot := filepath.Join(root, "configs")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		err := Materialize(context.Background(), testPlan(source, configRoot, filepath.Join(configRoot, "api")))
		if err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("source symlink error = %v", err)
		}
	})

	t.Run("source child symlink", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		writeTestSource(t, source, "http://unused.invalid")
		outside := filepath.Join(root, "outside-secret.txt")
		if err := os.WriteFile(outside, []byte("must not be copied\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(source, "linked.yaml")); err != nil {
			t.Fatal(err)
		}
		configRoot := filepath.Join(root, "configs")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(configRoot, "api")
		err := Materialize(context.Background(), testPlan(source, configRoot, target))
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("child symlink error = %v", err)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("target exists after rejecting outside symlink: %v", statErr)
		}
	})

	t.Run("source ancestor symlink is canonicalized", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real-parent")
		source := filepath.Join(realParent, "source")
		writeTestSource(t, source, "http://unused.invalid")
		aliasParent := filepath.Join(root, "alias-parent")
		if err := os.Symlink(realParent, aliasParent); err != nil {
			t.Fatal(err)
		}
		configRoot := filepath.Join(root, "configs")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(configRoot, "api")
		plan := testPlan(filepath.Join(aliasParent, "source"), configRoot, target)
		if err := Materialize(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(target, "application.yaml")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("target is not direct child", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		writeTestSource(t, source, "http://unused.invalid")
		configRoot := filepath.Join(root, "configs")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(configRoot, "nested", "api")
		err := Materialize(context.Background(), testPlan(source, configRoot, target))
		if err == nil || !strings.Contains(err.Error(), "direct child") {
			t.Fatalf("nested target error = %v", err)
		}
	})

	t.Run("target symlink", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		writeTestSource(t, source, "http://unused.invalid")
		configRoot := filepath.Join(root, "configs")
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outside, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(configRoot, "api")
		if err := os.Symlink(outside, target); err != nil {
			t.Fatal(err)
		}
		err := Materialize(context.Background(), testPlan(source, configRoot, target))
		if err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("target symlink error = %v", err)
		}
	})

	t.Run("source and target overlap", func(t *testing.T) {
		root := t.TempDir()
		configRoot := filepath.Join(root, "configs")
		target := filepath.Join(configRoot, "api")
		if err := os.Mkdir(configRoot, 0700); err != nil {
			t.Fatal(err)
		}
		writeTestSource(t, target, "http://unused.invalid")
		err := Materialize(context.Background(), testPlan(target, configRoot, target))
		if err == nil || !strings.Contains(err.Error(), "must not overlap") {
			t.Fatalf("overlapping source/target error = %v", err)
		}
	})
}

func TestMaterializeFailurePreservesExistingTarget(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, source string, plan *Plan)
	}{
		{
			name: "invalid patch",
			mutate: func(t *testing.T, source string, plan *Plan) {
				plan.Patches = []Patch{{File: "application.yaml", Path: "server.port.value", Value: 1}}
			},
		},
		{
			name: "invalid copied YAML",
			mutate: func(t *testing.T, source string, plan *Plan) {
				if err := os.WriteFile(filepath.Join(source, "broken.yaml"), []byte("broken: [\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multi document patch",
			mutate: func(t *testing.T, source string, plan *Plan) {
				content := "server:\n  port: 8080\n---\nsecond:\n  value: preserved\n"
				if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				plan.Patches = []Patch{{File: "application.yaml", Path: "server.port", Value: 18080}}
			},
		},
		{
			name: "duplicate YAML key",
			mutate: func(t *testing.T, source string, plan *Plan) {
				content := "server:\n  port: 8080\n  port: 18080\n"
				if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "semantically duplicate YAML key",
			mutate: func(t *testing.T, source string, plan *Plan) {
				content := "flags:\n  true: first\n  True: second\n"
				if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "semantically duplicate numeric YAML key",
			mutate: func(t *testing.T, source string, plan *Plan) {
				content := "numbers:\n  0xB: first\n  11: second\n"
				if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			configRoot := filepath.Join(root, "configs")
			target := filepath.Join(configRoot, "api")
			writeTestSource(t, source, "http://unused.invalid")
			if err := os.Mkdir(configRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte("keep exactly\n"), 0600); err != nil {
				t.Fatal(err)
			}
			plan := testPlan(source, configRoot, target)
			test.mutate(t, source, &plan)
			before := directoryHash(t, target)
			if err := Materialize(context.Background(), plan); err == nil {
				t.Fatal("materialization unexpectedly succeeded")
			}
			if after := directoryHash(t, target); after != before {
				t.Fatalf("target changed after failure: before=%s after=%s", before, after)
			}
		})
	}
}

func TestMaterializeReportsCommittedPublicationWhenOldTargetCleanupFails(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	configRoot := filepath.Join(root, "configs")
	target := filepath.Join(configRoot, "api")
	writeTestSource(t, source, "http://unused.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte("old target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalRemove := removePublishedDirectory
	removePublishedDirectory = func(path string) error {
		return errors.New("injected cleanup failure")
	}
	defer func() {
		removePublishedDirectory = originalRemove
	}()
	err := Materialize(context.Background(), testPlan(source, configRoot, target))
	cleanupErr := &PublishedCleanupError{}
	if !errors.As(err, &cleanupErr) {
		t.Fatalf("cleanup error = %v, want PublishedCleanupError", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "sentinel.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("old target remained after committed publication: %v", statErr)
	}
	application := readYAMLMap(t, filepath.Join(target, "application.yaml"))
	if application["server"].(map[string]any)["name"] != "api" {
		t.Fatalf("new target was not kept after cleanup failure: %#v", application)
	}
}

func testPlan(source string, configRoot string, target string) Plan {
	return Plan{
		Service:          "api",
		Driver:           DriverYAMLOverlay,
		SourceDriver:     SourceRepository,
		SourceDir:        source,
		ConfigRoot:       configRoot,
		TargetDir:        target,
		Application:      "application.yaml",
		Bootstrap:        "config-dev.yaml",
		RuntimeBootstrap: "config-local.yaml",
	}
}

func writeTestSource(t *testing.T, source string, apolloEndpoint string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"application.yaml": "server:\n  name: api\n  port: 8080\nrpc:\n  partner:\n    discovType: consul\n",
		"config-dev.yaml": fmt.Sprintf("appId: demo\ncluster: dev\nip: %s\nnamespaceName: application.yml\n", apolloEndpoint),
		"config-local.yaml": "localConfigEnable: false\nlocalConfigPath: source/application.yaml\n",
		"nested/data.txt": "resource\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{}
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertPrivateTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0700 {
				return fmt.Errorf("directory %s mode = %o", path, info.Mode().Perm())
			}
			return nil
		}
		if info.Mode().Perm() != 0600 {
			return fmt.Errorf("file %s mode = %o", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func directoryHash(t *testing.T, root string) string {
	t.Helper()
	entries := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s|%s|%o", relative, info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, file); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			line += "|" + hex.EncodeToString(hash.Sum(nil))
		}
		entries = append(entries, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	hash := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(hash[:])
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type repeatingByteReader struct{}

func (repeatingByteReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}
