package materialize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostSnapshotUsesSharedGuardsAndRejectsChangedBootstrap(t *testing.T) {
	root := t.TempDir()
	source, configRoot := filepath.Join(root, "source"), filepath.Join(root, "configs")
	writeTestSource(t, source, "http://must-not-be-contacted.invalid")
	if err := os.Mkdir(configRoot, 0700); err != nil { t.Fatal(err) }
	bootstrap, err := os.ReadFile(filepath.Join(source, "config-dev.yaml"))
	if err != nil { t.Fatal(err) }
	content := []byte("server:\n  name: host\n  port: 8080\n")
	snapshot, err := NewHostSnapshot(bootstrap, content)
	if err != nil { t.Fatal(err) }
	content[0] = 'X'
	plan := testPlan(source, configRoot, filepath.Join(configRoot, "api"))
	plan.SourceDriver = SourceApollo
	plan.Patches = []Patch{{File: "application.yaml", Path: "server.port", Value: 9000}}
	plan.Guards = []Guard{{File: "application.yaml", Path: "server.port", Value: 18080}}
	before := directoryHash(t, source)
	if _, err := MaterializeFromSource(context.Background(), plan, snapshot); err != nil { t.Fatal(err) }
	server := readYAMLMap(t, filepath.Join(plan.TargetDir, "application.yaml"))["server"].(map[string]any)
	if server["name"] != "host" || server["port"] != 18080 { t.Fatalf("unexpected effective config: %#v", server) }
	if directoryHash(t, source) != before { t.Fatal("source was modified") }
	assertPrivateTree(t, plan.TargetDir)
	published := directoryHash(t, plan.TargetDir)
	if err := os.WriteFile(filepath.Join(source, "config-dev.yaml"), append(bootstrap, []byte("# changed\n")...), 0600); err != nil { t.Fatal(err) }
	if _, err := MaterializeFromSource(context.Background(), plan, snapshot); err == nil { t.Fatal("accepted stale host snapshot") }
	if directoryHash(t, plan.TargetDir) != published { t.Fatal("failed preparation changed published config") }
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	if _, err := snapshot.Application(ctx, SourceInput{Bootstrap: bootstrap}); err == nil { t.Fatal("ignored cancellation") }
}

func TestHostSnapshotRejectsInvalidInputs(t *testing.T) {
	for _, application := range [][]byte{nil, []byte("secret: [invalid"), []byte("a: 1\n---\nb: 2\n")} {
		if _, err := NewHostSnapshot([]byte("appId: demo\n"), application); err == nil { t.Fatal("accepted invalid snapshot") }
	}
	if _, err := MaterializeFromSource(context.Background(), Plan{}, nil); err == nil { t.Fatal("accepted nil source") }
}
