package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const startupFailureLogBytes = 256 * 1024

type startupFailureDiagnostic struct {
	cause   error
	details []string
}

func (err *startupFailureDiagnostic) Error() string {
	return strings.Join(err.details, "; ")
}

func (err *startupFailureDiagnostic) Unwrap() error {
	return err.cause
}

func diagnoseStartupFailure(failure error, process ServiceProcess, service PlannedService) error {
	var initialization *processInitializationError
	if !errors.As(failure, &initialization) {
		return failure
	}
	details := []string{initialization.Error()}
	fatalLog, firstFatal, truncated := startupFatalLogEvidence(process.LogPath, process.logOffset)
	if fatalLog != "" {
		if firstFatal {
			details = append(details, fmt.Sprintf("first fatal log: %q", fatalLog))
		} else {
			details = append(details, fmt.Sprintf("fatal log (tail fallback after first 256 KiB): %q", fatalLog))
		}
	} else if truncated {
		details = append(details, "fatal log scan truncated: no proven fatal line in first or last 256 KiB")
	}
	if configPath := plannedServiceConfigPath(service); configPath != "" {
		details = append(details, "config path: "+configPath)
	}
	if binding, dependency := fatalLogBinding(fatalLog, service.Config); binding != "" {
		details = append(details, fmt.Sprintf("binding: %s (dependency %s)", binding, dependency))
	}
	return &startupFailureDiagnostic{cause: failure, details: details}
}

func startupFatalLogEvidence(path string, offset int64) (string, bool, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, false
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	remaining := info.Size() - offset
	if fatalLog := scanFatalLog(file, offset, startupFailureLogBytes, false); fatalLog != "" {
		return fatalLog, true, remaining > startupFailureLogBytes
	}
	if remaining <= startupFailureLogBytes {
		return "", false, false
	}
	tailOffset := info.Size() - startupFailureLogBytes
	if fatalLog := scanFatalLog(file, tailOffset, startupFailureLogBytes, true); fatalLog != "" {
		return fatalLog, false, true
	}
	return "", false, true
}

func scanFatalLog(file *os.File, offset int64, maximumBytes int64, discardPartialLine bool) string {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maximumBytes))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	if discardPartialLine && scanner.Scan() {
		// The bounded tail commonly begins in the middle of a verbose line.
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if usefulFatalLogLine(line) {
			return line
		}
	}
	return ""
}

func usefulFatalLogLine(line string) bool {
	if line == "" || strings.HasPrefix(line, "[conven]") || strings.HasPrefix(line, "--- conven services --restart ") {
		return false
	}
	message := line
	if datedMessage, found := datedLogMessage(line); found {
		message = datedMessage
	}
	if fatal, found := structuredFatalSeverity(message); found {
		return fatal
	}
	return anchoredFatalMessage(message) || message == "empty etcd hosts"
}

func structuredFatalSeverity(line string) (bool, bool) {
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(line), &entry) == nil {
			for _, key := range []string{"level", "severity", "log.level"} {
				value, found := entry[key]
				if !found {
					continue
				}
				severity, ok := value.(string)
				return fatalSeverity(severity), ok
			}
		}
	}
	for _, field := range strings.Fields(line) {
		key, value, found := strings.Cut(field, "=")
		if !found || key != "level" && key != "severity" {
			continue
		}
		value = strings.Trim(value, "\"',")
		return fatalSeverity(value), true
	}
	return false, false
}

func fatalSeverity(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fatal", "panic":
		return true
	default:
		return false
	}
}

func datedLogMessage(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	if len(fields) >= 3 {
		timestamp := fields[0] + " " + fields[1]
		for _, layout := range []string{"2006/01/02 15:04:05", "2006/01/02 15:04:05.999999999"} {
			if _, err := time.Parse(layout, timestamp); err == nil {
				return strings.Join(fields[2:], " "), true
			}
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil {
		return strings.Join(fields[1:], " "), true
	}
	return "", false
}

func anchoredFatalMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return lower == "fatal" || strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "fatal ") ||
		lower == "panic" || strings.HasPrefix(lower, "panic:") || strings.HasPrefix(lower, "[fatal]")
}

func plannedServiceConfigPath(service PlannedService) string {
	if service.Config == nil || service.Config.Plan.TargetDir == "" || service.Config.Plan.Application == "" {
		return ""
	}
	return filepath.Join(service.Config.Plan.TargetDir, service.Config.Plan.Application)
}

func fatalLogBinding(line string, config *PlannedConfig) (string, string) {
	if line == "" || config == nil {
		return "", ""
	}
	var binding string
	var dependency string
	for _, route := range config.Routes {
		if route.Binding == "" || !logMentionsBinding(line, route.Binding) {
			continue
		}
		if binding != "" && (binding != route.Binding || dependency != route.Dependency) {
			return "", ""
		}
		binding = route.Binding
		dependency = route.Dependency
	}
	return binding, dependency
}

func logMentionsBinding(line string, binding string) bool {
	for start := 0; start < len(line); {
		index := strings.Index(line[start:], binding)
		if index < 0 {
			return false
		}
		index += start
		before := index == 0 || !bindingIdentifierByte(line[index-1])
		afterIndex := index + len(binding)
		after := afterIndex == len(line) || !bindingIdentifierByte(line[afterIndex])
		if before && after {
			return true
		}
		start = index + 1
	}
	return false
}

func bindingIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}
