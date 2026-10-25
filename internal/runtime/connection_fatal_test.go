package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKtctlFatalTransportFailsBeforeReadiness(t *testing.T) {
	for _, message := range []string{"socket: too many open files", "unable to do port forwarding: socat not found"} {
		t.Run(message, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "connection.log")
			if err := os.WriteFile(logPath, []byte("WRN "+message+"\n"), 0600); err != nil { t.Fatal(err) }
			_, err, retry := waitForConnectionAttempt(context.Background(), &ConnectionProcess{PID:os.Getpid()}, nil, ConnectionConfig{Driver:"ktctl", Timeout:100*time.Millisecond}, logPath)
			if err == nil || !strings.Contains(err.Error(), message) || retry { t.Fatalf("err=%v retry=%v", err, retry) }
		})
	}
}

func TestKtctlTransientDNSFailureIsNotFatal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "connection.log")
	if err := os.WriteFile(logPath, []byte("WRN DNS lookup error=EOF\nWRN DNS lookup i/o timeout\n"), 0600); err != nil { t.Fatal(err) }
	if err := ktctlFatalTransportFailure(logPath); err != nil { t.Fatal(err) }
	if err := ktctlFatalTransportFailure(logPath + ".missing"); err != nil { t.Fatal(err) }
}
