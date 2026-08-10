package runtime

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRenderDashboardFrameKeepsBannerAndLatestLogs(t *testing.T) {
	info := dashboardInfo{
		Workspace:   "local-stack",
		Environment: "dev",
		Address:     "192.168.1.42",
		Interface:   "en0",
		Services: []dashboardService{
			{Name: "user-svc", Ports: map[string]int{"metrics": 19090, "http": 18080}},
			{Name: "order-svc", Ports: map[string]int{"http": 18081}},
		},
	}
	history := []string{
		"[user-svc] one",
		"[order-svc] two",
		"[user-svc] three",
		"[order-svc] four",
		"[user-svc] five",
	}
	frame := renderDashboardFrame(info, 80, 8, history)
	for _, expected := range []string{
		"CONVEN | local-stack | env=dev | LAN=192.168.1.42 (en0)",
		"user-svc | ports http=18080, metrics=19090",
		"order-svc | ports http=18081",
		"LOGS | q/Ctrl-C detach; services keep running",
		"[order-svc] two",
		"[user-svc] five",
	} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("dashboard frame is missing %q: %q", expected, frame)
		}
	}
	if strings.Contains(frame, "[user-svc] one") {
		t.Fatalf("dashboard retained a log outside its viewport: %q", frame)
	}
	if lines := strings.Count(frame, "\r\n") + 1; lines != 8 {
		t.Fatalf("dashboard frame has %d rows, want 8", lines)
	}
}

func TestDashboardBannerCollapsesServicesAndLeavesLogSpace(t *testing.T) {
	info := dashboardInfo{
		Workspace:   "small",
		Environment: "test",
		Address:     "unavailable",
		Services: []dashboardService{
			{Name: "api"},
			{Name: "order"},
			{Name: "payment"},
		},
	}
	banner := dashboardBannerLines(info, 5)
	if len(banner) != 4 {
		t.Fatalf("banner rows = %d, want 4: %#v", len(banner), banner)
	}
	if !strings.Contains(strings.Join(banner, "\n"), "+2 more services") {
		t.Fatalf("collapsed banner does not report hidden services: %#v", banner)
	}
	frame := renderDashboardFrame(info, 40, 5, []string{"latest"})
	if !strings.Contains(frame, "latest") {
		t.Fatalf("small dashboard left no log viewport: %q", frame)
	}
}

func TestDashboardSanitizesTerminalControlSequences(t *testing.T) {
	value := "safe\x1b[2Jred\x1b]0;title\a\tend\r\n"
	if sanitized := sanitizeDashboardText(value); sanitized != "safered    end" {
		t.Fatalf("sanitized line = %q", sanitized)
	}
	frame := renderDashboardFrame(dashboardInfo{Workspace: "safe"}, 60, 4, []string{value})
	if strings.Contains(frame, "\x1b[2J") || strings.Contains(frame, "title") {
		t.Fatalf("service log retained terminal control data: %q", frame)
	}
}

func TestDashboardFitsWideCharactersByTerminalCellWidth(t *testing.T) {
	if fitted := fitDashboardLine("服务A", 6, true); fitted != "服务A " {
		t.Fatalf("padded wide line = %q", fitted)
	}
	if fitted := fitDashboardLine("服务日志", 5, false); fitted != "服务…" {
		t.Fatalf("truncated wide line = %q", fitted)
	}
	if width := dashboardDisplayWidth("e\u0301服务"); width != 5 {
		t.Fatalf("combined display width = %d, want 5", width)
	}
}

func TestChooseLocalIPv4PrefersDefaultRouteThenPrivateInterface(t *testing.T) {
	candidates := []localIPv4Candidate{
		{Interface: "en0", Index: 4, Flags: net.FlagUp, IP: net.ParseIP("192.168.1.42").To4()},
		{Interface: "utun3", Index: 8, Flags: net.FlagUp | net.FlagPointToPoint, IP: net.ParseIP("10.20.0.2").To4()},
		{Interface: "eth0", Index: 2, Flags: net.FlagUp, IP: net.ParseIP("203.0.113.4").To4()},
	}
	selected := chooseLocalIPv4(net.ParseIP("10.20.0.2"), candidates)
	if selected.Interface != "utun3" {
		t.Fatalf("default route selected %q, want utun3", selected.Interface)
	}
	selected = chooseLocalIPv4(nil, candidates)
	if selected.Interface != "en0" {
		t.Fatalf("fallback selected %q, want private non-point-to-point en0", selected.Interface)
	}
	if selected := chooseLocalIPv4(nil, nil); selected.IP != nil {
		t.Fatalf("empty candidate selection = %#v", selected)
	}
}

func TestDashboardServicesUseSessionPortSnapshotWithManifestFallback(t *testing.T) {
	workspace := &WorkspaceData{Manifest: &model.Manifest{
		Services: map[string]model.Service{
			"api":   {Ports: map[string]int{"http": 28080}},
			"order": {Ports: map[string]int{"http": 18081}},
		},
	}}
	session := &Session{
		Selected: []string{"order", "api"},
		Services: []ServiceProcess{
			{Name: "api", Ports: map[string]int{"http": 18080}},
			{Name: "order"},
		},
	}
	services := dashboardServices(workspace, session, nil)
	if len(services) != 2 || services[0].Name != "order" || services[1].Name != "api" {
		t.Fatalf("dashboard service order = %#v", services)
	}
	if services[0].Ports["http"] != 18081 {
		t.Fatalf("old session manifest fallback port = %d", services[0].Ports["http"])
	}
	if services[1].Ports["http"] != 18080 {
		t.Fatalf("session port snapshot = %d, want 18080", services[1].Ports["http"])
	}
}

func TestDashboardServicesKeepCapturedEmptyPortSnapshot(t *testing.T) {
	workspace := &WorkspaceData{Manifest: &model.Manifest{
		Services: map[string]model.Service{
			"api": {Ports: map[string]int{"http": 28080}},
		},
	}}
	session := &Session{Services: []ServiceProcess{{
		Name:  "api",
		Ports: map[string]int{},
	}}}
	services := dashboardServices(workspace, session, nil)
	if len(services) != 1 || services[0].Ports == nil || len(services[0].Ports) != 0 {
		t.Fatalf("captured empty port snapshot = %#v", services)
	}
}

func TestTailLogsFallsBackToPlainStreamWithoutTTY(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- TailLogs(ctx, &WorkspaceData{Manifest: &model.Manifest{
			Workspace: model.Workspace{Name: "plain"},
		}}, &Session{
			Environment: "dev",
			Services:    []ServiceProcess{{Name: "api", LogPath: logPath}},
		}, nil, nil, writer)
		writer.Close()
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if line != "[api] ready\n" {
		cancel()
		t.Fatalf("plain tail output = %q", line)
	}
	if strings.Contains(line, "\x1b") {
		cancel()
		t.Fatalf("plain tail output contains terminal control data: %q", line)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plain tail did not stop after context cancellation")
	}
}
