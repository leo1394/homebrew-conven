package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuardConfigurationPlanUsesOnlySelectedIdentities(t *testing.T) {
	ws, _, _ := guardPreparationFixture(t)
	path := filepath.Join(ws.Root, "beta", "bootstrap.yaml")
	data := []byte("appId: new-api\ncluster: test\nnamespaceName: application.yml\nip: apollo.test:8080\n")
	if err := os.WriteFile(path, data, 0600); err != nil { t.Fatal(err) }
	plan, err := PlanGuardConfigurationReads(ws, "test", []string{"beta"})
	if err != nil || len(plan) != 1 || plan[0].Service != "beta" || plan[0].Path != "/configs/new-api/test/application.yml?ip=127.0.0.1" || plan[0].Authority != "apollo.test:8080" { t.Fatalf("plan: %+v %v", plan, err) }
	actual, _ := os.ReadFile(path); if string(actual) != string(data) { t.Fatal("modified source") }
	if _, err := PlanGuardConfigurationReads(ws, "prod", []string{"beta"}); err == nil { t.Fatal("unknown environment") }
	if err := os.WriteFile(path, []byte("appId: ../secret\ncluster: test\nnamespaceName: application.yml\nip: apollo.test:8080\n"), 0600); err != nil { t.Fatal(err) }
	if _, err := PlanGuardConfigurationReads(ws, "test", []string{"beta"}); err == nil { t.Fatal("accepted path traversal") }
}
