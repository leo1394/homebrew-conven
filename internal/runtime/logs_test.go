package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShowLogsPrefixesSelectedServices(t *testing.T) {
	directory := t.TempDir()
	userLog := filepath.Join(directory, "user.log")
	orderLog := filepath.Join(directory, "order.log")
	if err := os.WriteFile(userLog, []byte("user line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orderLog, []byte("order line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session := &Session{Services: []ServiceProcess{
		{Name: "user-svc", LogPath: userLog},
		{Name: "order-svc", LogPath: orderLog},
	}}
	var output strings.Builder
	if err := ShowLogs(context.Background(), session, []string{"order-svc"}, false, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "[order-svc] order line\n" {
		t.Fatalf("unexpected output %q", output.String())
	}
}

func TestShowLogsRejectsServiceOutsideSession(t *testing.T) {
	err := ShowLogs(context.Background(), &Session{}, []string{"missing"}, false, &strings.Builder{})
	if err == nil {
		t.Fatal("missing service unexpectedly succeeded")
	}
}

func TestReadLastLinesAtUsesFixedSnapshotBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	appender, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appender.WriteString("during\n"); err != nil {
		appender.Close()
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	lines, err := readLastLinesAt(file, info.Size(), logTailLines)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, "\n") != "before" {
		t.Fatalf("snapshot tail = %#v, want only pre-snapshot data", lines)
	}
	data, nextOffset, err := readFrom(path, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "during\n" {
		t.Fatalf("follow data = %q, want appended data exactly once", data)
	}
	if nextOffset != info.Size()+int64(len(data)) {
		t.Fatalf("next offset = %d, want %d", nextOffset, info.Size()+int64(len(data)))
	}
}

func TestDistributeInitialLogLinesKeepsTotalBudget(t *testing.T) {
	budgets := distributeInitialLogLines(3, logFollowLines)
	if len(budgets) != 3 || budgets[0] != 3334 || budgets[1] != 3333 || budgets[2] != 3333 {
		t.Fatalf("distributed budgets = %#v", budgets)
	}
	total := 0
	for _, budget := range budgets {
		total += budget
	}
	if total != logFollowLines {
		t.Fatalf("distributed total = %d, want %d", total, logFollowLines)
	}
}

func TestDistributeInitialLogBytesKeepsGlobalBudget(t *testing.T) {
	budgets := distributeInitialLogBytes(3, int64(logFollowSnapshotBytes))
	if len(budgets) != 3 {
		t.Fatalf("byte budgets = %#v", budgets)
	}
	total := int64(0)
	for _, budget := range budgets {
		total += budget
	}
	if total != int64(logFollowSnapshotBytes) {
		t.Fatalf("distributed bytes = %d, want %d", total, logFollowSnapshotBytes)
	}
	if budgets[0]-budgets[2] > 1 {
		t.Fatalf("byte budgets are not balanced: %#v", budgets)
	}
}

func TestConsumeLogDataBoundsUnterminatedLines(t *testing.T) {
	data := []byte(strings.Repeat("x", logLineBytes+5))
	lines, pending, discarding := consumeLogData("", false, data)
	if len(lines) != 1 || len(lines[0]) > logLineBytes || !strings.HasSuffix(lines[0], "…[truncated]") {
		t.Fatalf("bounded lines = %#v", lines)
	}
	if pending != "" || !discarding {
		t.Fatalf("bounded state = pending %q discarding %t", pending, discarding)
	}
	lines, pending, discarding = consumeLogData(pending, discarding, []byte("discarded\nnext\n"))
	if len(lines) != 1 || lines[0] != "next" || pending != "" || discarding {
		t.Fatalf("resumed state = lines %#v pending %q discarding %t", lines, pending, discarding)
	}
}

func TestFollowSnapshotTruncatesOversizedLineWithoutFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.log")
	line := strings.Repeat("x", logLineBytes+100)
	if err := os.WriteFile(path, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lines, _, err := readLastLinesSnapshotWithLimit(path, 1, int64(len(line)+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || len(lines[0]) > logLineBytes || !strings.HasSuffix(lines[0], "…[truncated]") {
		t.Fatalf("oversized snapshot count = %d", len(lines))
	}
}

func TestFollowSnapshotCanReadLatestTenThousandLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.log")
	var content strings.Builder
	for index := 0; index < logFollowLines+2; index++ {
		fmt.Fprintf(&content, "line-%05d\n", index)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
		t.Fatal(err)
	}
	lines, _, err := readLastLinesSnapshotWithLimit(path, logFollowLines, int64(logFollowSnapshotBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != logFollowLines {
		t.Fatalf("follow snapshot lines = %d", len(lines))
	}
	if lines[0] != "line-00002" || lines[len(lines)-1] != "line-10001" {
		t.Fatalf("follow snapshot boundaries = %q ... %q", lines[0], lines[len(lines)-1])
	}
}

func TestStreamLogsTailsLast80LinesThenAppendsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	var initial strings.Builder
	for index := 1; index <= 82; index++ {
		fmt.Fprintf(&initial, "line-%03d\n", index)
	}
	if err := os.WriteFile(path, []byte(initial.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lines := make([]string, 0, 81)
	err := streamLogsWithLimits(ctx, []namedLog{{Name: "api", Path: path}}, logTailLines, int64(logSnapshotBytes), func(entry logEntry) error {
		lines = append(lines, entry.Line)
		if entry.Line == "line-082" {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			if _, err := file.WriteString("appended\n"); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
		if entry.Line == "appended" {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 81 {
		t.Fatalf("streamed %d lines, want 81: %#v", len(lines), lines)
	}
	if lines[0] != "line-003" || lines[len(lines)-1] != "appended" {
		t.Fatalf("stream boundaries = %q ... %q", lines[0], lines[len(lines)-1])
	}
	appended := 0
	for _, line := range lines {
		if line == "appended" {
			appended++
		}
	}
	if appended != 1 {
		t.Fatalf("appended line appeared %d times", appended)
	}
}
