package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRegistryRetryClassifiesTransientErrors(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED, &registryHTTPStatusError{503}, &registryHTTPStatusError{504}} {
		if !transientRegistryError(fmt.Errorf("wrapped: %w", err)) { t.Fatalf("not retryable: %v", err) }
	}
	for _, err := range []error{context.Canceled, errors.New("invalid JSON"), &registryHTTPStatusError{401}, &registryHTTPStatusError{403}, &registryHTTPStatusError{404}} {
		if transientRegistryError(err) { t.Fatalf("unexpected retry: %v", err) }
	}
}

func TestRegistryRetryBoundedAndCancellable(t *testing.T) {
	calls := 0
	_, recovered, err := retryRegistrySnapshot(context.Background(), func(context.Context) (*RegistrySnapshot, error) {
		calls++; return nil, io.EOF
	})
	if err == nil || !recovered || calls != 4 || !strings.Contains(err.Error(), "after 4 attempts") { t.Fatalf("calls=%d recovered=%v err=%v", calls, recovered, err) }
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	_, _, err = retryRegistrySnapshot(ctx, func(context.Context) (*RegistrySnapshot, error) {
		calls++; cancel(); return nil, io.EOF
	})
	if !errors.Is(err, context.Canceled) || calls != 1 { t.Fatalf("cancellation: calls=%d err=%v", calls, err) }
	deadline, done := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer done()
	calls = 0
	_, _, err = retryRegistrySnapshot(deadline, func(context.Context) (*RegistrySnapshot, error) {
		calls++; return nil, io.EOF
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 { t.Fatalf("backoff ignored deadline: calls=%d err=%v", calls, err) }
}

func TestRegistryHTTPTimeoutRetainsRetryableCause(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 { time.Sleep(80 * time.Millisecond) }
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	client := &http.Client{Timeout: 20 * time.Millisecond}
	_, recovered, err := retryRegistrySnapshot(context.Background(), func(ctx context.Context) (*RegistrySnapshot, error) {
		var payload []interface{}
		err := registryGET(ctx, client, server.URL+"?token=private-value", nil, &payload)
		if err != nil && strings.Contains(err.Error(), "private-value") { t.Fatal("request URL leaked into diagnostic") }
		return &RegistrySnapshot{}, err
	})
	if err != nil || !recovered || calls.Load() != 2 { t.Fatalf("timeout recovery: calls=%d recovered=%v err=%v", calls.Load(), recovered, err) }
}

func TestRegistryBaselineRetriesTemporaryFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 { w.WriteHeader(503); return }
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	service := PlannedService{Name: "api", RegistryRef: "consul", RegistryIdentity: "api.rpc", Registry: &model.Registry{Driver: "consul", Address: server.URL}}
	baseline, err := snapshotServiceRegistry(context.Background(), t.TempDir(), service)
	if err != nil || baseline == nil || calls.Load() != 2 { t.Fatalf("baseline: %v %v, calls=%d", baseline, err, calls.Load()) }
}

func TestRegistryObservationRestartsWindowAfterRecovery(t *testing.T) {
	var calls atomic.Int32
	var recoveredAt atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 2 { w.WriteHeader(503); return }
		if call == 3 { recoveredAt.Store(time.Now().UnixNano()) }
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	service := PlannedService{Name: "api", RegistryRef: "consul", RegistryIdentity: "api.rpc", Registry: &model.Registry{Driver: "consul", Address: server.URL, ObserveFor: "30ms"}}
	baseline := &RegistrySnapshot{Driver: "consul", Registry: "consul", Identity: "api.rpc", Instances: map[string]RegistryInstance{}}
	evidence, err := verifyServiceRegistry(context.Background(), t.TempDir(), service, baseline)
	if err != nil { t.Fatal(err) }
	if evidence == nil || recoveredAt.Load() == 0 || time.Since(time.Unix(0, recoveredAt.Load())) < 30*time.Millisecond || calls.Load() < 4 {
		t.Fatalf("observation did not restart after recovery: calls=%d evidence=%v", calls.Load(), evidence)
	}
}

func TestRegistryObservationNeverAcceptsFailureOrNewInstance(t *testing.T) {
	for _, scenario := range []string{"unauthorized", "malformed", "unavailable", "new-instance-after-recovery"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				switch scenario {
				case "unauthorized": w.WriteHeader(403)
				case "unavailable": w.WriteHeader(503)
				case "malformed": fmt.Fprint(w, `{broken`)
				default:
					if call == 1 { w.WriteHeader(503); return }
					fmt.Fprint(w, `[{"Service":{"ID":"local","Address":"127.0.0.1","Port":18090}}]`)
				}
			}))
			defer server.Close()
			service := PlannedService{Name: "api", RegistryRef: "consul", RegistryIdentity: "api.rpc", Registry: &model.Registry{Driver: "consul", Address: server.URL, ObserveFor: "1ms"}}
			evidence, err := verifyServiceRegistry(context.Background(), t.TempDir(), service, &RegistrySnapshot{Driver: "consul", Instances: map[string]RegistryInstance{}})
			if err == nil || evidence != nil { t.Fatalf("unsafe success: %v %v", evidence, err) }
			if (scenario == "unauthorized" || scenario == "malformed") && calls.Load() != 1 { t.Fatal("non-transient error retried") }
			if scenario == "unavailable" && calls.Load() != 4 { t.Fatalf("unbounded retries: %d", calls.Load()) }
			if scenario == "new-instance-after-recovery" && !strings.Contains(err.Error(), "new instance") { t.Fatal(err) }
		})
	}
}
