package runtime

import (
	"bufio"
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

	"github.com/leo1394/homebrew-loom/internal/terminal"
)

const logTailLines = 80

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
	return streamLogs(ctx, logs, func(entry logEntry) error {
		_, err := fmt.Fprintf(output, "[%s] %s\n", style.Identifier(entry.Name), entry.Line)
		return err
	})
}

func streamLogs(ctx context.Context, logs []namedLog, emit func(logEntry) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	entries, errorsChannel := startLogStream(ctx, logs)
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

func startLogStream(ctx context.Context, logs []namedLog) (<-chan logEntry, <-chan error) {
	entries := make(chan logEntry, 128)
	errorsChannel := make(chan error, len(logs))
	var followers sync.WaitGroup
	for _, log := range logs {
		followers.Add(1)
		go func(log namedLog) {
			defer followers.Done()
			if err := followLog(ctx, log, entries); err != nil && !errors.Is(err, context.Canceled) {
				errorsChannel <- err
			}
		}(log)
	}
	go func() {
		followers.Wait()
		close(entries)
		close(errorsChannel)
	}()
	return entries, errorsChannel
}

func selectLogs(session *Session, names []string) ([]namedLog, error) {
	if session == nil {
		return nil, errors.New("no loom session found")
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

func followLog(ctx context.Context, log namedLog, entries chan<- logEntry) error {
	initial, offset, err := readLastLinesSnapshot(log.Path, logTailLines)
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
			pending += string(data)
			parts := strings.Split(pending, "\n")
			pending = parts[len(parts)-1]
			for _, line := range parts[:len(parts)-1] {
				line = strings.TrimSuffix(line, "\r")
				select {
				case entries <- logEntry{Name: log.Name, Line: line}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
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
	data, err := io.ReadAll(file)
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
	lines, err := readLastLinesAt(file, boundary, count)
	return lines, boundary, err
}

func readLastLinesAt(file *os.File, boundary int64, count int) ([]string, error) {
	if boundary < 0 {
		return nil, fmt.Errorf("log snapshot boundary must not be negative")
	}
	const maxTailBytes int64 = 256 * 1024
	start := int64(0)
	if boundary > maxTailBytes {
		start = boundary - maxTailBytes
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
	lines := make([]string, 0)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return lines, nil
}
