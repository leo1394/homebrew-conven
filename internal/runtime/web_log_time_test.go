package runtime

import (
	"testing"
	"time"
)

func TestWebLogTimestampOffsets(t *testing.T) {
	for _, test := range []struct { name, line, want string }{
		{"go-zero", `{"@timestamp":"2026-10-13T22:50:29.363+08","level":"info","content":"resolver update"}`, "2026-10-13T14:50:29.363Z"},
		{"negative offset", `{"timestamp":"2026-10-13T06:50:29-08","message":"ready"}`, "2026-10-13T14:50:29Z"},
		{"plain hour offset", "2026-10-13T22:50:29.363+08 INFO ready", "2026-10-13T14:50:29.363Z"},
		{"minute offset", "2026-10-13T20:20:29+05:30 INFO ready", "2026-10-13T14:50:29Z"},
		{"UTC", "2026-10-13T14:50:29Z INFO ready", "2026-10-13T14:50:29Z"},
		{"missing", "ready without timestamp", ""},
		{"invalid", `{"time":"2026-99-99T22:50:29+08","message":"ready"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, _ := parseWebLogLine(test.line, false)
			if test.want == "" {
				if entry.time != nil { t.Fatalf("invented timestamp: %v", entry.time) }
				return
			}
			if entry.time == nil || entry.time.Format(time.RFC3339Nano) != test.want { t.Fatalf("timestamp = %v, want %s", entry.time, test.want) }
			since := entry.time.Add(-time.Second)
			until := entry.time.Add(time.Second)
			if !webLogMatches(entry, WebLogQuery{}, &since, &until) { t.Fatal("timestamp lost in time filtering") }
			if webLogMatches(entry, WebLogQuery{}, &until, nil) { t.Fatal("time filter accepted older entry") }
		})
	}
}
