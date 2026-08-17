package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRenderDashboardFrameKeepsBannerAndLatestLogs(t *testing.T) {
	info := dashboardInfo{
		Version:     "0.2.4",
		Workspace:   "local-stack",
		Environment: "dev",
		Address:     "192.168.1.42",
		Interface:   "en0",
		Cluster:     "dev-cluster-config",
		DisabledRPCBindings: []string{"legacyRpc", "searchRpc"},
		StartedAt:   time.Date(2026, time.August, 17, 9, 8, 7, 0, time.Local),
		Color:       true,
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
	frame := renderDashboardFrame(info, 80, 13, history)
	plain := plainDashboardFrame(frame)
	for _, expected := range []string{
		"  CONVEN  0.2.4",
		"STARTED  2026-08-17 09:08:07",
		"  WORKSPACE  local-stack     ENV  dev     LAN  192.168.1.42 (en0)",
		"  CLUSTER    dev-cluster-config",
		"  DISABLED   legacyRpc, searchRpc",
		"  SERVICES   2 local",
		"    user-svc",
		"HTTP  18080  ·  METRICS  19090",
		"    order-svc",
		"HTTP  18081",
		"q / Ctrl-C: detach · FOLLOW · ↑/↓ scroll · / search",
		"[order-svc] two",
		"[user-svc] five",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("dashboard frame is missing %q: %q", expected, plain)
		}
	}
	if strings.Contains(plain, "[user-svc] one") {
		t.Fatalf("dashboard retained a log outside its viewport: %q", plain)
	}
	if strings.Contains(plain, "LOGS") {
		t.Fatalf("dashboard detach divider retained the LOGS label: %q", plain)
	}
	if lines := strings.Count(frame, "\r\n") + 1; lines != 13 {
		t.Fatalf("dashboard frame has %d rows, want 13", lines)
	}
	if dashboardHasBackgroundStyle(frame) {
		t.Fatalf("dashboard frame uses reverse or background styling: %q", frame)
	}
	borderLines := 0
	banner := dashboardBannerLines(info, 80, 13)
	for _, line := range banner {
		if strings.Contains(line, "─") {
			borderLines++
		}
	}
	if borderLines != 2 {
		t.Fatalf("dashboard banner has %d border lines, want title and detach borders: %q", borderLines, plain)
	}
	userLine := dashboardLineContaining(banner, "user-svc")
	orderLine := dashboardLineContaining(banner, "order-svc")
	if strings.Index(userLine, "HTTP") != strings.Index(orderLine, "HTTP") {
		t.Fatalf("service protocol columns are not aligned: user=%q order=%q", userLine, orderLine)
	}
}

func TestDashboardBannerUsesWhiteLabelsGreenCountAndCenteredYellowHint(t *testing.T) {
	info := dashboardInfo{
		Version:     "0.2.5",
		Workspace:   "local-stack",
		Environment: "test",
		Address:     "10.0.0.8",
		Interface:   "en0",
		Cluster:     "test-cluster",
		DisabledRPCBindings: []string{"legacyRpc"},
		Color:       true,
		Services: []dashboardService{
			{Name: "api", Ports: map[string]int{"http": 18080}},
			{Name: "rpc", Ports: map[string]int{"rpc": 18081}},
			{Name: "worker"},
			{Name: "jobs"},
		},
	}
	const width = 100
	hint := "q / Ctrl-C: detach · FOLLOW · ↑/↓ scroll · / search"
	banner := dashboardBannerWithHint(info, width, 16, hint)
	rendered := make([]string, 0, len(banner))
	for _, line := range banner {
		rendered = append(rendered, renderDashboardLine(line, width, true))
	}
	joined := strings.Join(rendered, "\n")
	if !strings.Contains(joined, dashboardWhite+strings.Repeat("─", width)+dashboardReset) {
		t.Fatalf("dashboard top rule is not white: %q", joined)
	}
	topRule := strings.TrimRight(plainDashboardText(rendered[1]), " ")
	if topRule != strings.Repeat("─", width) {
		t.Fatalf("dashboard top rule is not left-aligned: %q", topRule)
	}
	for _, label := range []string{"WORKSPACE", "ENV", "LAN", "CLUSTER", "DISABLED", "SERVICES", "RPC", "HTTP"} {
		if !strings.Contains(joined, dashboardWhite+label+dashboardReset) {
			t.Fatalf("dashboard label %q is not white: %q", label, joined)
		}
	}
	if !strings.Contains(joined, dashboardGreen+"4"+dashboardReset+dashboardDim+" local"+dashboardReset) {
		t.Fatalf("dashboard local service count is not green: %q", joined)
	}
	if !strings.Contains(joined, dashboardYellow+"legacyRpc"+dashboardReset) {
		t.Fatalf("dashboard disabled RPC binding is not yellow: %q", joined)
	}
	if strings.Contains(joined, dashboardGreen+"4 local") {
		t.Fatalf("dashboard highlighted the full service count instead of only the number: %q", joined)
	}
	divider := rendered[len(rendered)-1]
	if !strings.Contains(divider, dashboardYellow+hint+dashboardReset) {
		t.Fatalf("dashboard hint is not yellow: %q", divider)
	}
	if strings.Count(divider, dashboardWhite) != 2 {
		t.Fatalf("dashboard footer rules are not white: %q", divider)
	}
	if strings.Contains(divider, "\x1b[2;36m") {
		t.Fatalf("dashboard footer retained the dim cyan rule style: %q", divider)
	}
	plain := plainDashboardText(divider)
	hintStart := strings.Index(plain, hint)
	if hintStart < 0 {
		t.Fatalf("dashboard hint is missing: %q", plain)
	}
	left := dashboardDisplayWidth(plain[:hintStart])
	right := width - left - dashboardDisplayWidth(hint)
	if left-right > 1 || right-left > 1 {
		t.Fatalf("dashboard hint is not centered: left=%d right=%d line=%q", left, right, plain)
	}
}

func TestDashboardTitleShowsEarliestSelectedServiceStartAtFarRight(t *testing.T) {
	later := time.Date(2026, time.August, 17, 11, 30, 0, 0, time.Local)
	earlier := later.Add(-45 * time.Minute)
	services := []dashboardService{
		{Name: "api", StartedAt: later},
		{Name: "rpc", StartedAt: earlier},
		{Name: "worker"},
	}
	started := dashboardServicesStartedAt(services)
	if !started.Equal(earlier) {
		t.Fatalf("dashboard start time = %s, want %s", started, earlier)
	}
	const width = 80
	title := renderDashboardLine(dashboardTitleLine(dashboardInfo{Version: "0.2.12", StartedAt: started}, width), width, false)
	plain := strings.TrimSuffix(title, dashboardReset)
	if dashboardDisplayWidth(plain) != width {
		t.Fatalf("dashboard title width = %d, want %d: %q", dashboardDisplayWidth(plain), width, plain)
	}
	if !strings.HasSuffix(plain, "STARTED  2026-08-17 10:45:00") {
		t.Fatalf("dashboard title does not right-align start time: %q", plain)
	}
}

func TestDashboardBannerCollapsesServicesAndLeavesLogSpace(t *testing.T) {
	info := dashboardInfo{
		Version:     "0.2.4",
		Workspace:   "small",
		Environment: "test",
		Address:     "unavailable",
		Cluster:     "test-cluster-config",
		Services: []dashboardService{
			{Name: "api"},
			{Name: "order"},
			{Name: "payment"},
		},
	}
	banner := dashboardBannerLines(info, 40, 14)
	if len(banner) != 11 {
		t.Fatalf("banner rows = %d, want 11: %#v", len(banner), banner)
	}
	if !strings.Contains(strings.Join(banner, "\n"), "+2 more services") {
		t.Fatalf("collapsed banner does not report hidden services: %#v", banner)
	}
	frame := renderDashboardFrame(info, 40, 14, []string{"latest"})
	if !strings.Contains(frame, "latest") {
		t.Fatalf("small dashboard left no log viewport: %q", frame)
	}
}

func TestDashboardSanitizesTerminalControlSequences(t *testing.T) {
	value := "safe\x1b[2Jred\x1b[41m\x1b]0;title\a\tend\r\n"
	if sanitized := sanitizeDashboardText(value); sanitized != "safered    end" {
		t.Fatalf("sanitized line = %q", sanitized)
	}
	frame := renderDashboardFrame(dashboardInfo{Workspace: "safe", Color: true}, 60, 4, []string{value})
	if strings.Contains(frame, "\x1b[2J") || strings.Contains(frame, "\x1b[41m") || strings.Contains(frame, "title") {
		t.Fatalf("service log retained terminal control data: %q", frame)
	}
}

func TestDashboardNoColorDisablesStylingButKeepsScreenControl(t *testing.T) {
	frame := renderDashboardFrame(dashboardInfo{
		Version:     "0.2.4",
		Workspace:   "plain",
		Environment: "test",
		Address:     "127.0.0.1",
		Cluster:     "test-cluster-config",
		Services:    []dashboardService{{Name: "api", Ports: map[string]int{"http": 18080}}},
	}, 80, 10, []string{"[api] ready"})
	if dashboardHasSGR(frame) {
		t.Fatalf("color-disabled dashboard contains SGR styling: %q", frame)
	}
	if !strings.Contains(frame, "\x1b[H") || !strings.Contains(frame, "\x1b[2K") {
		t.Fatalf("color-disabled dashboard lost screen controls: %q", frame)
	}
	plain := plainDashboardFrame(frame)
	for _, expected := range []string{"CONVEN", "0.2.4", "CLUSTER", "SERVICES", "[api] ready"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("color-disabled dashboard is missing %q: %q", expected, plain)
		}
	}
}

func TestDashboardLogHighlightsOnlyPrefixAndSeverity(t *testing.T) {
	slow := renderDashboardLogLine("[api] request SLOW after 900ms", true)
	if !strings.Contains(slow, dashboardCyan+"[api]"+dashboardReset) {
		t.Fatalf("dashboard log prefix is not highlighted: %q", slow)
	}
	if !strings.Contains(slow, dashboardYellow+"SLOW"+dashboardReset) {
		t.Fatalf("dashboard slow severity is not highlighted: %q", slow)
	}
	failed := renderDashboardLogLine("[api] request ERROR after retry", true)
	if !strings.Contains(failed, dashboardRed+"ERROR"+dashboardReset) {
		t.Fatalf("dashboard error severity is not highlighted: %q", failed)
	}
	if plainDashboardText(slow) != "[api] request SLOW after 900ms" || plainDashboardText(failed) != "[api] request ERROR after retry" {
		t.Fatalf("dashboard log highlighting changed content: slow=%q failed=%q", slow, failed)
	}
}

func TestDashboardFramesFitTerminalAndKeepLogsVisible(t *testing.T) {
	info := dashboardInfo{
		Version:     "0.2.4",
		Workspace:   "项目-with-a-long-name",
		Environment: "test",
		Address:     "192.168.100.200",
		Interface:   "en0",
		Cluster:     "test-cluster-config",
		Color:       true,
		Services: []dashboardService{
			{Name: "服务-api-with-a-very-long-name", Ports: map[string]int{"http": 18080}},
			{Name: "worker", Ports: map[string]int{"rpc": 18081}},
		},
	}
	for _, size := range [][2]int{{20, 4}, {40, 8}, {80, 12}} {
		width := size[0]
		height := size[1]
		frame := renderDashboardFrame(info, width, height, []string{"old", "latest"})
		lines := strings.Split(frame, "\r\n")
		if len(lines) != height {
			t.Fatalf("dashboard %dx%d has %d rows", width, height, len(lines))
		}
		for _, line := range lines {
			if lineWidth := dashboardDisplayWidth(plainDashboardText(line)); lineWidth > width {
				t.Fatalf("dashboard %dx%d line width = %d: %q", width, height, lineWidth, plainDashboardText(line))
			}
		}
		if !strings.Contains(plainDashboardFrame(frame), "latest") {
			t.Fatalf("dashboard %dx%d left no visible log row: %q", width, height, frame)
		}
	}
}

func TestDashboardColorHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if dashboardColorEnabled() {
		t.Fatal("dashboard color remained enabled with NO_COLOR present")
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

func TestDashboardLogFragmentsPreserveContentAndCellWidth(t *testing.T) {
	for _, test := range []struct {
		value string
		width int
	}{
		{value: "exact-width", width: 11},
		{value: "服务日志详情", width: 5},
		{value: "e\u0301-combined-value", width: 6},
	} {
		fragments := dashboardLogFragments(test.value, test.width)
		var joined strings.Builder
		for _, fragment := range fragments {
			if width := dashboardDisplayWidth(fragment.Text); width > test.width {
				t.Fatalf("fragment width = %d, want <= %d: %q", width, test.width, fragment.Text)
			}
			if strings.Contains(fragment.Text, "…") {
				t.Fatalf("fragment contains viewport ellipsis: %q", fragment.Text)
			}
			joined.WriteString(fragment.Text)
		}
		if joined.String() != test.value {
			t.Fatalf("wrapped content = %q, want %q", joined.String(), test.value)
		}
	}
}

func TestDashboardFrameWrapsLongJSONAndFollowsItsTail(t *testing.T) {
	info := dashboardInfo{
		Version:     "0.2.5",
		Workspace:   "local",
		Environment: "test",
		Address:     "10.0.0.8",
		Cluster:     "test",
	}
	longJSON := `[rea-api] {"@timestamp":"2026-08-11T11:30:54.363+08","level":"slow","duration":"926.3ms","content":"[RPC] ok - slowcall - 127.0.0.1:18082","payload":"` + strings.Repeat("segment-", 40) + `","requestId":"tail-marker-9f31"}`
	frame := renderDashboardFrame(info, 52, 12, []string{longJSON})
	lines := strings.Split(frame, "\r\n")
	if len(lines) != 12 {
		t.Fatalf("wrapped dashboard has %d rows, want 12", len(lines))
	}
	var visibleLog strings.Builder
	for _, line := range lines[len(dashboardBanner(info, 52, 12)):] {
		visibleLog.WriteString(plainDashboardText(line))
	}
	if !strings.Contains(visibleLog.String(), "tail-marker-9f31") {
		t.Fatalf("dashboard follow view does not show the wrapped JSON tail: %q", plainDashboardFrame(frame))
	}
	if strings.Contains(visibleLog.String(), "@timestamp") {
		t.Fatalf("dashboard follow view started at the head of an over-height JSON line: %q", visibleLog.String())
	}
	if strings.Contains(visibleLog.String(), "…") {
		t.Fatalf("dashboard added a viewport ellipsis to wrapped JSON: %q", visibleLog.String())
	}
	for _, line := range lines {
		if width := dashboardDisplayWidth(plainDashboardText(line)); width > 52 {
			t.Fatalf("wrapped dashboard row width = %d, want <= 52: %q", width, plainDashboardText(line))
		}
	}
}

func TestChooseLocalIPv4PrefersPhysicalPrivateInterfaceOverTunnelRoute(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		primary    string
		candidates []localIPv4Candidate
		want       string
	}{
		{
			name:    "darwin en0 over utun",
			goos:    "darwin",
			primary: "198.19.0.1",
			candidates: []localIPv4Candidate{
				{Interface: "utun4", Index: 8, Flags: net.FlagUp | net.FlagPointToPoint, IP: net.ParseIP("198.19.0.1").To4()},
				{Interface: "en0", Index: 4, Flags: net.FlagUp, IP: net.ParseIP("10.198.199.110").To4()},
			},
			want: "en0",
		},
		{
			name:    "linux eth0 over tun",
			goos:    "linux",
			primary: "10.20.0.2",
			candidates: []localIPv4Candidate{
				{Interface: "tun0", Index: 8, Flags: net.FlagUp | net.FlagPointToPoint, IP: net.ParseIP("10.20.0.2").To4()},
				{Interface: "eth0", Index: 2, Flags: net.FlagUp, IP: net.ParseIP("192.168.10.20").To4()},
			},
			want: "eth0",
		},
		{
			name:    "private fallback without preferred name",
			goos:    "darwin",
			primary: "198.19.0.1",
			candidates: []localIPv4Candidate{
				{Interface: "utun4", Index: 8, Flags: net.FlagUp | net.FlagPointToPoint, IP: net.ParseIP("198.19.0.1").To4()},
				{Interface: "en5", Index: 5, Flags: net.FlagUp, IP: net.ParseIP("192.168.1.42").To4()},
			},
			want: "en5",
		},
		{
			name:    "tunnel is final fallback",
			goos:    "darwin",
			primary: "198.19.0.1",
			candidates: []localIPv4Candidate{
				{Interface: "utun4", Index: 8, Flags: net.FlagUp | net.FlagPointToPoint, IP: net.ParseIP("198.19.0.1").To4()},
			},
			want: "utun4",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected := chooseLocalIPv4ForOS(test.goos, net.ParseIP(test.primary), test.candidates)
			if selected.Interface != test.want {
				t.Fatalf("selected interface = %q, want %q", selected.Interface, test.want)
			}
		})
	}
	if selected := chooseLocalIPv4ForOS("darwin", nil, nil); selected.IP != nil {
		t.Fatalf("empty candidate selection = %#v", selected)
	}
}

func TestKubeconfigClusterNameKeepsOnlyBasename(t *testing.T) {
	if name := kubeconfigClusterName("/Users/example/k8s/cls-9s1ammj5-config"); name != "cls-9s1ammj5-config" {
		t.Fatalf("cluster name = %q", name)
	}
	if name := kubeconfigClusterName("  "); name != "" {
		t.Fatalf("empty cluster name = %q", name)
	}
	legacy := &Session{Connection: &ConnectionProcess{
		Driver:  "ktctl",
		Command: []string{"ktctl", "--kubeconfig", "/Users/example/k8s/legacy-cluster-config", "connect"},
	}}
	if name := dashboardSessionCluster(legacy); name != "legacy-cluster-config" {
		t.Fatalf("legacy session cluster name = %q", name)
	}
	legacy.Cluster = "captured-cluster-config"
	if name := dashboardSessionCluster(legacy); name != "captured-cluster-config" {
		t.Fatalf("captured session cluster name = %q", name)
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

func TestTailLogsRejectsNonTerminalDashboard(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if DashboardAvailable(nil, &output) {
		t.Fatal("dashboard unexpectedly available without terminal files")
	}
	err := TailLogs(context.Background(), &WorkspaceData{Manifest: &model.Manifest{
		Workspace: model.Workspace{Name: "plain"},
	}}, &Session{
		Environment: "dev",
		Services:    []ServiceProcess{{Name: "api", LogPath: logPath}},
	}, TailOptions{Version: "test-version"}, nil, &output)
	if err == nil || !strings.Contains(err.Error(), "dashboard requires an interactive terminal") || !strings.Contains(err.Error(), "services --logs --tail") {
		t.Fatalf("non-terminal dashboard error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("non-terminal dashboard output = %q", output.String())
	}
}

func TestDashboardHistoryRetainsLatestTenThousandLines(t *testing.T) {
	history := newDashboardHistory(dashboardHistoryLines)
	for index := 0; index < dashboardHistoryLines+5; index++ {
		history.Append("line-" + strconv.Itoa(index))
	}
	if history.Len() != dashboardHistoryLines {
		t.Fatalf("history length = %d, want %d", history.Len(), dashboardHistoryLines)
	}
	if first := history.At(0); first != "line-5" {
		t.Fatalf("oldest retained line = %q", first)
	}
	if last := history.At(history.Len()-1); last != "line-"+strconv.Itoa(dashboardHistoryLines+4) {
		t.Fatalf("latest retained line = %q", last)
	}
}

func TestDashboardHistoryEnforcesByteAndLineLimits(t *testing.T) {
	history := newDashboardHistory(10)
	history.maximumBytes = 9
	history.Append("12345")
	if evicted := history.Append("67890"); evicted != 1 {
		t.Fatalf("byte-limited append evicted %d lines", evicted)
	}
	if history.Len() != 1 || history.At(0) != "67890" || history.bytes != 5 {
		t.Fatalf("byte-limited history = len %d bytes %d value %q", history.Len(), history.bytes, history.At(0))
	}

	long := strings.Repeat("界", dashboardLineBytes/3+10)
	truncated := truncateDashboardHistoryLine(long)
	if len(truncated) > dashboardLineBytes || !strings.HasSuffix(truncated, "…[truncated]") {
		t.Fatalf("truncated line bytes = %d suffix=%t", len(truncated), strings.HasSuffix(truncated, "…[truncated]"))
	}
}

func TestDashboardViewScrollSearchAndResumeFollow(t *testing.T) {
	history := newDashboardHistory(10)
	for _, line := range []string{"zero", "target one", "two", "target three"} {
		history.Append(line)
	}
	view := dashboardView{Follow: true, SearchMatch: -1}
	view.Clamp(history, 80, 2)
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputPageUp}, history, &view, 80, 2)
	if view.Follow || view.Top != 0 {
		t.Fatalf("page-up view = %#v", view)
	}
	history.Append("four")
	view.RecordAppend(history, 0)
	view.Clamp(history, 80, 2)
	if view.Top != 0 || view.NewLines != 1 {
		t.Fatalf("paused append view = %#v", view)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputText, Text: "/"}, history, &view, 80, 2)
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputText, Text: "target"}, history, &view, 80, 2)
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputEnter}, history, &view, 80, 2)
	if view.SearchQuery != "target" || view.SearchMatch != 1 || view.Follow {
		t.Fatalf("initial search view = %#v", view)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputText, Text: "n"}, history, &view, 80, 2)
	if view.SearchMatch != 3 {
		t.Fatalf("next search match = %d", view.SearchMatch)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputText, Text: "N"}, history, &view, 80, 2)
	if view.SearchMatch != 1 {
		t.Fatalf("previous search match = %d", view.SearchMatch)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputEscape}, history, &view, 80, 2)
	if view.SearchQuery != "" || view.SearchMatch != -1 {
		t.Fatalf("cleared search view = %#v", view)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputEnd}, history, &view, 80, 2)
	if !view.Follow || view.Top != 3 || view.NewLines != 0 {
		t.Fatalf("resumed view = %#v", view)
	}
}

func TestDashboardViewScrollsWithinWrappedLogicalLine(t *testing.T) {
	history := newDashboardHistory(10)
	history.Append("AAAAABBBBBCCCCCDDDDD")
	view := dashboardView{Follow: true, SearchMatch: -1}
	view.Clamp(history, 5, 2)
	if view.Top != 0 || view.TopOffset != 10 {
		t.Fatalf("follow cursor = %#v, want logical line 0 offset 10", view)
	}

	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputUp}, history, &view, 5, 2)
	if view.Follow || view.Top != 0 || view.TopOffset != 5 {
		t.Fatalf("wrapped line up view = %#v", view)
	}
	visible := dashboardVisibleLogRows(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, 5, 2)
	if len(visible) != 2 || visible[0].Text != "BBBBB" || visible[1].Text != "CCCCC" {
		t.Fatalf("wrapped line up rows = %#v", visible)
	}

	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputPageUp}, history, &view, 5, 2)
	if view.Top != 0 || view.TopOffset != 0 {
		t.Fatalf("wrapped line page-up view = %#v", view)
	}
	handleDashboardInput(dashboardInputEvent{Kind: dashboardInputPageDown}, history, &view, 5, 2)
	if !view.Follow || view.TopOffset != 10 {
		t.Fatalf("wrapped line page-down did not resume follow: %#v", view)
	}
}

func TestDashboardSearchRevealsMatchInWrappedContinuation(t *testing.T) {
	history := newDashboardHistory(10)
	history.Append("0000000000abcdefghijNEEDLE-tail-value")
	view := dashboardView{Follow: true, SearchMatch: -1, SearchQuery: "needle"}
	selectDashboardMatch(history, &view, 10, 2, -1, 1)
	if view.SearchMatch != 0 || view.Follow {
		t.Fatalf("wrapped search view = %#v", view)
	}
	visible := dashboardVisibleLogRows(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, 10, 2)
	var text strings.Builder
	for _, row := range visible {
		text.WriteString(row.Text)
	}
	if !strings.Contains(text.String(), "NEEDLE") {
		t.Fatalf("wrapped search match is outside viewport: rows=%#v view=%#v", visible, view)
	}
}

func TestDashboardResizeRewrapsPausedOffsetAndFollowTail(t *testing.T) {
	history := newDashboardHistory(10)
	history.Append("abcdefghijklmnopqrstuvwx")
	paused := dashboardView{Top: 0, TopOffset: 10, SearchMatch: -1}
	paused.Clamp(history, 8, 2)
	if paused.Top != 0 || paused.TopOffset != 8 {
		t.Fatalf("resized paused cursor = %#v, want offset 8", paused)
	}
	visible := dashboardVisibleLogRows(history, dashboardCursor{Line: paused.Top, Offset: paused.TopOffset}, 8, 2)
	if len(visible) != 2 || !strings.Contains(visible[0].Text, "k") {
		t.Fatalf("resized paused anchor is not visible: %#v", visible)
	}

	follow := dashboardView{Follow: true, SearchMatch: -1}
	follow.Clamp(history, 5, 2)
	follow.Clamp(history, 8, 2)
	visible = dashboardVisibleLogRows(history, dashboardCursor{Line: follow.Top, Offset: follow.TopOffset}, 8, 2)
	if len(visible) != 2 || !strings.HasSuffix(visible[len(visible)-1].Text, "uvwx") {
		t.Fatalf("resized follow view lost latest tail: view=%#v rows=%#v", follow, visible)
	}
}

func TestDashboardWrappedCursorHandlesHistoryEviction(t *testing.T) {
	history := newDashboardHistory(2)
	history.Append("AAAAABBBBB")
	history.Append("CCCCCDDDDD")
	view := dashboardView{Top: 1, TopOffset: 5, SearchMatch: 1, SearchQuery: "DDDDD"}
	evicted := history.Append("EEEEFFFFFF")
	view.RecordAppend(history, evicted)
	view.Clamp(history, 5, 1)
	if view.Top != 0 || view.TopOffset != 5 || view.SearchMatch != 0 {
		t.Fatalf("retained wrapped cursor after eviction = %#v", view)
	}

	view.Top = 0
	view.TopOffset = 5
	view.SearchMatch = 0
	evicted = history.Append("GGGGGHHHHH")
	view.RecordAppend(history, evicted)
	view.Clamp(history, 5, 1)
	if view.Top != 0 || view.TopOffset != 0 || view.SearchMatch != -1 || view.SearchMessage != "current match expired" {
		t.Fatalf("expired wrapped cursor after eviction = %#v", view)
	}
}

func TestParseDashboardInputEvents(t *testing.T) {
	tests := []struct {
		input      string
		wantKind   dashboardInputKind
		wantText   string
		incomplete bool
	}{
		{input: "\x1b[A", wantKind: dashboardInputUp},
		{input: "\x1b[5~", wantKind: dashboardInputPageUp},
		{input: "\x1b[6~", wantKind: dashboardInputPageDown},
		{input: "\x1b[H", wantKind: dashboardInputHome},
		{input: "\x1b[F", wantKind: dashboardInputEnd},
		{input: "\x1b[<64;20;8M", wantKind: dashboardInputUp},
		{input: "\x1b[<65;20;8M", wantKind: dashboardInputDown},
		{input: "服", wantKind: dashboardInputText, wantText: "服"},
		{input: "\x1b", incomplete: true},
		{input: string([]byte{0xe6}), incomplete: true},
	}
	for _, test := range tests {
		event, consumed, incomplete := parseDashboardInputEvent([]byte(test.input))
		if incomplete != test.incomplete {
			t.Fatalf("parse %q incomplete = %t", test.input, incomplete)
		}
		if incomplete {
			if consumed != 0 {
				t.Fatalf("parse %q consumed = %d while incomplete", test.input, consumed)
			}
			continue
		}
		if event.Kind != test.wantKind || event.Text != test.wantText || consumed != len(test.input) {
			t.Fatalf("parse %q = %#v consumed=%d", test.input, event, consumed)
		}
	}
}

func TestDashboardInputReaderStopsBeforeTerminalRestore(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	keys, inputErrors, done := readDashboardInput(ctx, reader)
	if _, err := writer.WriteString("q"); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case event := <-keys:
		if event.Kind != dashboardInputText || event.Text != "q" {
			t.Fatalf("input event = %#v", event)
		}
	case err := <-inputErrors:
		cancel()
		t.Fatalf("input error = %v", err)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("dashboard input was not read")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dashboard input reader did not stop after cancellation")
	}
}

func renderDashboardFrame(info dashboardInfo, width int, height int, lines []string) string {
	history := newDashboardHistory(dashboardHistoryLines)
	for _, line := range lines {
		history.Append(line)
	}
	view := dashboardView{Follow: true, SearchMatch: -1}
	return renderDashboardViewFrame(info, width, height, history, &view)
}

func plainDashboardFrame(frame string) string {
	lines := strings.Split(frame, "\r\n")
	for index, line := range lines {
		lines[index] = plainDashboardText(line)
	}
	return strings.Join(lines, "\n")
}

func dashboardLineContaining(lines []string, value string) string {
	for _, line := range lines {
		if strings.Contains(line, value) {
			return line
		}
	}
	return ""
}

func plainDashboardText(value string) string {
	return sanitizeDashboardText(value)
}

func dashboardHasSGR(value string) bool {
	for index := 0; index+2 < len(value); index++ {
		if value[index] != 0x1b || value[index+1] != '[' {
			continue
		}
		for end := index + 2; end < len(value); end++ {
			if value[end] < 0x40 || value[end] > 0x7e {
				continue
			}
			if value[end] == 'm' {
				return true
			}
			index = end
			break
		}
	}
	return false
}

func dashboardHasBackgroundStyle(value string) bool {
	for index := 0; index+2 < len(value); index++ {
		if value[index] != 0x1b || value[index+1] != '[' {
			continue
		}
		for end := index + 2; end < len(value); end++ {
			if value[end] < 0x40 || value[end] > 0x7e {
				continue
			}
			if value[end] != 'm' {
				index = end
				break
			}
			for _, parameter := range strings.Split(value[index+2:end], ";") {
				code, err := strconv.Atoi(parameter)
				if err != nil {
					continue
				}
				if code == 7 || code >= 40 && code <= 49 || code == 48 || code >= 100 && code <= 107 {
					return true
				}
			}
			index = end
			break
		}
	}
	return false
}
