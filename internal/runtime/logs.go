package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

const (
	logTailLines           = 80
	logFollowLines         = 10000
	logSnapshotBytes       = 256 * 1024
	logFollowSnapshotBytes = 64 * 1024 * 1024
	logReadChunkBytes      = 4 * 1024 * 1024
	logLineBytes           = 1024 * 1024
)

type namedLog struct {
	Name string
	Path string
}

type logEntry struct {
	Name string
	Line string
}

func ShowLogs(ctx context.Context, session *Session, names []string, tail bool, output io.Writer) error {
	style := terminal.New(output)
	logs, err := selectLogs(session, names)
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return errors.New("no logs are available for the current session")
	}
	if !tail {
		for _, log := range logs {
			lines, err := readLastLines(log.Path, logTailLines)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			for _, line := range lines {
				fmt.Fprintf(output, "[%s] %s\n", style.Identifier(log.Name), line)
			}
		}
		return nil
	}
	return streamLogsWithLimits(ctx, logs, logFollowLines, int64(logFollowSnapshotBytes), func(entry logEntry) error {
		_, err := fmt.Fprintf(output, "[%s] %s\n", style.Identifier(entry.Name), entry.Line)
		return err
	})
}

func streamLogsWithLimits(ctx context.Context, logs []namedLog, initialLines int, maximumBytes int64, emit func(logEntry) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	entries, errorsChannel := startLogStreamWithLimits(ctx, logs, initialLines, maximumBytes)
	for entries != nil || errorsChannel != nil {
		select {
		case entry, open := <-entries:
			if !open {
				entries = nil
				continue
			}
			if err := emit(entry); err != nil {
				return err
			}
		case err, open := <-errorsChannel:
			if !open {
				errorsChannel = nil
				continue
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

func startLogStreamWithBudget(ctx context.Context, logs []namedLog, initialLines int) (<-chan logEntry, <-chan error) {
	return startLogStreamWithLimits(ctx, logs, initialLines, int64(logFollowSnapshotBytes))
}

func startLogStreamWithLimits(ctx context.Context, logs []namedLog, initialLines int, maximumBytes int64) (<-chan logEntry, <-chan error) {
	entries := make(chan logEntry, 128)
	errorsChannel := make(chan error, len(logs))
	budgets := distributeInitialLogLines(len(logs), initialLines)
	byteBudgets := distributeInitialLogBytes(len(logs), maximumBytes)
	var followers sync.WaitGroup
	for index, log := range logs {
		budget := budgets[index]
		byteBudget := byteBudgets[index]
		followers.Add(1)
		go func(log namedLog, initialLines int, maximumBytes int64) {
			defer followers.Done()
			if err := followLogWithInitialLimits(ctx, log, initialLines, maximumBytes, entries); err != nil && !errors.Is(err, context.Canceled) {
				errorsChannel <- err
			}
		}(log, budget, byteBudget)
	}
	go func() {
		followers.Wait()
		close(entries)
		close(errorsChannel)
	}()
	return entries, errorsChannel
}

func distributeInitialLogLines(sources int, total int) []int {
	if sources <= 0 {
		return nil
	}
	if total < 0 {
		total = 0
	}
	budgets := make([]int, sources)
	base := total / sources
	remainder := total % sources
	for index := range budgets {
		budgets[index] = base
		if index < remainder {
			budgets[index]++
		}
	}
	return budgets
}

func distributeInitialLogBytes(sources int, total int64) []int64 {
	if sources <= 0 {
		return nil
	}
	if total < 0 {
		total = 0
	}
	budgets := make([]int64, sources)
	base := total / int64(sources)
	remainder := total % int64(sources)
	for index := range budgets {
		budgets[index] = base
		if int64(index) < remainder {
			budgets[index]++
		}
	}
	return budgets
}

func selectLogs(session *Session, names []string) ([]namedLog, error) {
	if session == nil {
		return nil, errors.New("no Conven session found")
	}
	available := make(map[string]string)
	for _, service := range session.Services {
		available[service.Name] = service.LogPath
	}
	if len(names) == 0 {
		names = make([]string, 0, len(available))
		for name := range available {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	logs := make([]namedLog, 0, len(names)+1)
	for _, name := range names {
		path, found := available[name]
		if !found {
			return nil, fmt.Errorf("service %q is not part of the current session", name)
		}
		logs = append(logs, namedLog{Name: name, Path: path})
	}
	if len(names) == len(available) && session.Connection != nil && session.Connection.LogPath != "" {
		logs = append(logs, namedLog{Name: "connection/" + session.Connection.Driver, Path: session.Connection.LogPath})
	}
	return logs, nil
}

func followLogWithInitialLimits(ctx context.Context, log namedLog, initialLines int, maximumBytes int64, entries chan<- logEntry) error {
	initial, offset, err := readLastLinesSnapshotWithLimit(log.Path, initialLines, maximumBytes)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range initial {
		select {
		case entries <- logEntry{Name: log.Name, Line: line}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var pending string
	discarding := false
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			data, nextOffset, err := readFrom(log.Path, offset)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			offset = nextOffset
			if len(data) == 0 {
				continue
			}
			lines, nextPending, nextDiscarding := consumeLogData(pending, discarding, data)
			pending = nextPending
			discarding = nextDiscarding
			for _, line := range lines {
				select {
				case entries <- logEntry{Name: log.Name, Line: line}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func consumeLogData(pending string, discarding bool, data []byte) ([]string, string, bool) {
	chunk := string(data)
	lines := make([]string, 0)
	for len(chunk) > 0 {
		if discarding {
			newline := strings.IndexByte(chunk, '\n')
			if newline < 0 {
				return lines, "", true
			}
			chunk = chunk[newline+1:]
			discarding = false
			continue
		}
		newline := strings.IndexByte(chunk, '\n')
		if newline >= 0 {
			line := pending + chunk[:newline]
			pending = ""
			chunk = chunk[newline+1:]
			lines = append(lines, truncateLogLine(strings.TrimSuffix(line, "\r")))
			continue
		}
		if len(pending)+len(chunk) <= logLineBytes {
			return lines, pending + chunk, false
		}
		available := logLineBytes - len(pending)
		if available > 0 {
			pending += chunk[:available]
			chunk = chunk[available:]
		}
		lines = append(lines, truncateLogLine(pending+chunk))
		pending = ""
		discarding = true
	}
	return lines, pending, discarding
}

func truncateLogLine(line string) string {
	suffix := "…[truncated]"
	if len(line) <= logLineBytes {
		return line
	}
	limit := logLineBytes - len(suffix)
	return line[:limit] + suffix
}

func readFrom(path string, offset int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(logReadChunkBytes)))
	if err != nil {
		return nil, offset, err
	}
	return data, offset + int64(len(data)), nil
}

func readLastLines(path string, count int) ([]string, error) {
	lines, _, err := readLastLinesSnapshot(path, count)
	return lines, err
}

func readLastLinesSnapshot(path string, count int) ([]string, int64, error) {
	return readLastLinesSnapshotWithLimit(path, count, int64(logSnapshotBytes))
}

func readLastLinesSnapshotWithLimit(path string, count int, maximumBytes int64) ([]string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	boundary := info.Size()
	lines, err := readLastLinesAtLimit(file, boundary, count, maximumBytes)
	return lines, boundary, err
}

func readLastLinesAt(file *os.File, boundary int64, count int) ([]string, error) {
	return readLastLinesAtLimit(file, boundary, count, int64(logSnapshotBytes))
}

func readLastLinesAtLimit(file *os.File, boundary int64, count int, maximumBytes int64) ([]string, error) {
	if boundary < 0 {
		return nil, fmt.Errorf("log snapshot boundary must not be negative")
	}
	if count <= 0 {
		return nil, nil
	}
	if maximumBytes <= 0 {
		maximumBytes = int64(logSnapshotBytes)
	}
	start := int64(0)
	if boundary > maximumBytes {
		start = boundary - maximumBytes
	}
	data, err := io.ReadAll(io.NewSectionReader(file, start, boundary-start))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	reversed := make([]string, 0, count)
	for len(reversed) < count {
		separator := bytes.LastIndexByte(data[:end], '\n')
		part := data[separator+1 : end]
		if len(part) > 0 && part[len(part)-1] == '\r' {
			part = part[:len(part)-1]
		}
		reversed = append(reversed, logLineFromBytes(part))
		if separator < 0 {
			break
		}
		end = separator
	}
	lines := make([]string, len(reversed))
	for index := range reversed {
		lines[len(reversed)-index-1] = reversed[index]
	}
	return lines, nil
}

func logLineFromBytes(line []byte) string {
	suffix := "…[truncated]"
	if len(line) <= logLineBytes {
		return string(line)
	}
	limit := logLineBytes - len(suffix)
	return string(line[:limit]) + suffix
}
