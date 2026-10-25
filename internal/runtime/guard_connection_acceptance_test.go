//go:build guard_connection_acceptance

package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Manual connection-only acceptance. Never builds, materializes or starts services.
// Excluded from ordinary CI; requires explicit workspace, helper and service selection.
func TestGuardConnectionAcceptance(t *testing.T) {
	root, helper, selected := os.Getenv("GUARD_CHECK_WORKSPACE"), os.Getenv("GUARD_CHECK_HELPER"), os.Getenv("GUARD_CHECK_SERVICES")
	if root == "" || helper == "" || selected == "" { t.Skip("explicit connection acceptance inputs required") }
	options := CommonOptions{Cwd:root, Environment:os.Getenv("GUARD_CHECK_ENV")}
	if options.Environment == "" { t.Fatal("explicit environment required") }
	workspace, err := OpenWorkspace(options); if err != nil { t.Fatal(err) }
	// Match the unprotected GUI plan only. This test has no service launch path.
	t.Setenv("SERVICE_KAFKA_CONSUMERS_ENABLED", "true")
	plan, err := BuildPlan(workspace, options, strings.Split(selected, ",")); if err != nil { t.Fatal(err) }
	t.Logf("workspace=%s environment=%s executable=%s kubeconfig=%s namespace=%s", workspace.Root, plan.EnvironmentName, plan.Connection.Command, plan.Connection.Kubeconfig, plan.Connection.Namespace)
	for _, endpoint := range plan.Connection.Readiness { t.Logf("readiness %s: %s", endpoint.Name, endpoint.Address) }
	old := guardConnectionHelper; ConfigureGuardConnectionHelper(helper)
	defer ConfigureGuardConnectionHelper(old)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute); defer cancel()
	lease := t.TempDir()
	process, err := EnsureConnection(ctx, plan.Connection, filepath.Join(lease, "connection.log"), lease, io.Discard)
	if process != nil {
		defer func() {
			cleanup, done := context.WithTimeout(context.Background(), time.Minute); defer done()
			if err := releaseConnection(cleanup, process, lease, false, io.Discard); err != nil { t.Errorf("connection cleanup: %v", err) } else { t.Log("acceptance lease released") }
		}()
	}
	if err != nil {
		if lines, readErr := readLastLines(filepath.Join(lease, "connection.log"), 12); readErr == nil {
			for _, line := range lines { t.Log(RedactDiagnosticText(sanitizeDashboardText(line))) }
		}
		t.Fatal(err)
	}
	if process == nil || process.GuardConnectionID == "" { t.Fatal("no owned helper connection; cannot validate direct execution") }
	if len(process.Command) == 0 || process.Command[0] != plan.Connection.Command { t.Fatal("actual executable does not match workspace ktctl.path") }
	t.Logf("direct helper connection ready: pid=%d executable=%s", process.PID, process.Command[0])
}
