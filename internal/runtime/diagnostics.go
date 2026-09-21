package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	diagnosticsVersion = 1
	diagnosticsLimit = 20
	diagnosticsMaximumBytes = 1024 * 1024
	diagnosticTextLimit = 4096
	diagnosticLogLimit = 16 * 1024
	diagnosticLogsPerAttemptLimit = 16 * 1024
	diagnosticJSONDepthLimit = 4
)

type DiagnosticAttempt struct {
	ID          string             `json:"id"`
	Environment string             `json:"environment"`
	Services    []string           `json:"services"`
	StartedAt   time.Time          `json:"startedAt"`
	FinishedAt  *time.Time         `json:"finishedAt,omitempty"`
	Status      string             `json:"status"`
	Stages      []DiagnosticStage  `json:"stages"`
	Routes      []DiagnosticRoute  `json:"routes"`
	Logs        []DiagnosticLog    `json:"logs,omitempty"`
	Failure     *DiagnosticFailure `json:"failure,omitempty"`
}

type DiagnosticLog struct {
	Service    string    `json:"service"`
	CapturedAt time.Time `json:"capturedAt"`
	Tail       string    `json:"tail"`
}

type DiagnosticStage struct {
	Name       string     `json:"name"`
	Service    string     `json:"service,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type DiagnosticRoute struct {
	Service    string `json:"service"`
	Dependency string `json:"dependency"`
	Binding    string `json:"binding,omitempty"`
	Mode       string `json:"mode"`
	Target     string `json:"target,omitempty"`
	Source     string `json:"source,omitempty"`
}

type DiagnosticFailure struct {
	Classification     string   `json:"classification"`
	Stage              string   `json:"stage"`
	Service            string   `json:"service,omitempty"`
	Error              string   `json:"error"`
	NextStep           string   `json:"nextStep"`
	RetrySafety        string   `json:"retrySafety"`
	LogTail            string   `json:"logTail,omitempty"`
	StartedServices    []string `json:"startedServices,omitempty"`
	PendingServices    []string `json:"pendingServices,omitempty"`
	RolledBackServices []string `json:"rolledBackServices,omitempty"`
	RemainingServices  []string `json:"remainingServices,omitempty"`
	CleanupStatus      string   `json:"cleanupStatus,omitempty"`
}

type diagnosticCleanupEvidence struct {
	started    []string
	pending    []string
	rolledBack []string
	remaining  []string
	status     string
}

type diagnosticHistory struct {
	Version  int                 `json:"version"`
	Attempts []DiagnosticAttempt `json:"attempts"`
}

type diagnosticRecorder struct {
	workspace *WorkspaceData
	attempt   DiagnosticAttempt
	secrets   []string
}

var diagnosticSecretPattern = regexp.MustCompile(`(?i)((?:"?(?:authorization|password|passwd|token|secret|api[_-]?key|credential|cookie|set-cookie|session(?:id)?)"?|--?(?:password|token|secret|api[_-]?key|credential))\s*(?::|=|\s)\s*"?)([^\s",;}]+)`)
var diagnosticAuthorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/-]+=*`)
var diagnosticURLUserPattern = regexp.MustCompile(`(https?://)[^/@\s:]+:[^/@\s]+@`)
var diagnosticJSONSecretPattern = regexp.MustCompile(`(?i)("(?:authorization|password|passwd|token|secret|api[_-]?key|credential|cookie|set-cookie|session(?:id)?)"\s*:\s*)"(?:\\.|[^"\\])*"`)
var diagnosticHeaderPattern = regexp.MustCompile(`(?im)(^|\n)([ \t]*(?:authorization|cookie|set-cookie)\s*:\s*)[^\r\n]*`)
var diagnosticLongSecretPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|credential|session(?:id)?)\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*'|[^\r\n,;]+)`)

func beginDiagnostics(workspace *WorkspaceData, environment string, services []string) (*diagnosticRecorder, error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("create startup diagnostic id: %w", err)
	}
	recorder := &diagnosticRecorder{
		workspace: workspace,
		attempt: DiagnosticAttempt{
			ID:          "diag-v1-" + hex.EncodeToString(idBytes),
			Environment: environment,
			Services:    append([]string(nil), services...),
			StartedAt:   time.Now().UTC(),
			Status:      "running",
			Stages:      []DiagnosticStage{},
			Routes:      []DiagnosticRoute{},
		},
	}
	if err := recorder.persist(); err != nil {
		return nil, fmt.Errorf("start startup diagnostics: %w", err)
	}
	return recorder, nil
}

func (recorder *diagnosticRecorder) startStage(name string, service string) error {
	recorder.attempt.Stages = append(recorder.attempt.Stages, DiagnosticStage{
		Name:      name,
		Service:   service,
		Status:    "started",
		StartedAt: time.Now().UTC(),
	})
	return recorder.persist()
}

func (recorder *diagnosticRecorder) completeStage() error {
	if len(recorder.attempt.Stages) == 0 {
		return nil
	}
	stage := &recorder.attempt.Stages[len(recorder.attempt.Stages)-1]
	finished := time.Now().UTC()
	stage.FinishedAt = &finished
	stage.Status = "completed"
	return recorder.persist()
}

func (recorder *diagnosticRecorder) failStage(failure error) error {
	if len(recorder.attempt.Stages) == 0 {
		return nil
	}
	stage := &recorder.attempt.Stages[len(recorder.attempt.Stages)-1]
	finished := time.Now().UTC()
	stage.FinishedAt = &finished
	stage.Status = "failed"
	stage.Error = recorder.safeText(failure.Error(), diagnosticTextLimit)
	return recorder.persist()
}

func (recorder *diagnosticRecorder) setPlan(plan *Plan) error {
	recorder.attempt.Environment = plan.EnvironmentName
	recorder.attempt.Services = append([]string(nil), plan.Selected...)
	recorder.attempt.Routes = diagnosticRoutes(plan)
	recorder.secrets = diagnosticPlanSecrets(plan)
	return recorder.persist()
}

func (recorder *diagnosticRecorder) succeed() error {
	finished := time.Now().UTC()
	recorder.attempt.FinishedAt = &finished
	recorder.attempt.Status = "succeeded"
	return recorder.persist()
}

func (recorder *diagnosticRecorder) captureLogs(session *Session, names []string) error {
	recorder.attempt.Logs = diagnosticSessionLogs(recorder.workspace.Store, session, names, recorder.secrets)
	return recorder.persist()
}

func (recorder *diagnosticRecorder) fail(stage string, service string, failure error, logTail string) error {
	return recorder.failWithCleanup(stage, service, failure, logTail, diagnosticCleanupEvidence{})
}

func (recorder *diagnosticRecorder) failWithCleanup(stage string, service string, failure error, logTail string, cleanup diagnosticCleanupEvidence) error {
	finished := time.Now().UTC()
	recorder.attempt.FinishedAt = &finished
	recorder.attempt.Status = "failed"
	safeError := recorder.safeText(failure.Error(), diagnosticTextLimit)
	classification, nextStep := diagnosticFailureAdvice(stage, service)
	recorder.attempt.Failure = &DiagnosticFailure{
		Classification: classification,
		Stage:       stage,
		Service:     service,
		Error:       safeError,
		NextStep:    nextStep,
		RetrySafety: "unknown",
		LogTail:     recorder.safeText(logTail, diagnosticLogLimit),
		StartedServices: append([]string(nil), cleanup.started...),
		PendingServices: append([]string(nil), cleanup.pending...),
		RolledBackServices: append([]string(nil), cleanup.rolledBack...),
		RemainingServices: append([]string(nil), cleanup.remaining...),
		CleanupStatus: cleanup.status,
	}
	return recorder.persist()
}

func diagnosticSessionServiceNames(session *Session) []string {
	if session == nil {
		return nil
	}
	names := make([]string, 0, len(session.Services))
	for _, process := range session.Services {
		names = append(names, process.Name)
	}
	return names
}

func diagnosticPendingServices(order []string, started []string) []string {
	startedSet := make(map[string]bool, len(started))
	for _, name := range started {
		startedSet[name] = true
	}
	pending := make([]string, 0)
	for _, name := range order {
		if !startedSet[name] {
			pending = append(pending, name)
		}
	}
	return pending
}

func diagnosticRolledBackServices(started []string, remaining []string) []string {
	remainingSet := make(map[string]bool, len(remaining))
	for _, name := range remaining {
		remainingSet[name] = true
	}
	rolledBack := make([]string, 0)
	for _, name := range started {
		if !remainingSet[name] {
			rolledBack = append(rolledBack, name)
		}
	}
	return rolledBack
}

func (recorder *diagnosticRecorder) persist() error {
	history, err := readDiagnosticHistory(recorder.workspace.Store)
	if err != nil {
		return err
	}
	found := false
	for index := range history.Attempts {
		if history.Attempts[index].ID == recorder.attempt.ID {
			history.Attempts[index] = recorder.attempt
			found = true
			break
		}
	}
	if !found {
		history.Attempts = append([]DiagnosticAttempt{recorder.attempt}, history.Attempts...)
	}
	if len(history.Attempts) > diagnosticsLimit {
		history.Attempts = history.Attempts[:diagnosticsLimit]
	}
	return writeDiagnosticHistory(recorder.workspace.Store, history)
}

func ReadDiagnostics(workspace *WorkspaceData) ([]DiagnosticAttempt, error) {
	if workspace == nil || workspace.Store == nil {
		return nil, fmt.Errorf("read startup diagnostics: workspace store is required")
	}
	history, err := readDiagnosticHistory(workspace.Store)
	if err != nil {
		return nil, err
	}
	result := make([]DiagnosticAttempt, len(history.Attempts))
	for index, attempt := range history.Attempts {
		result[index] = sanitizedDiagnosticAttempt(attempt)
	}
	return result, nil
}

func sanitizedDiagnosticAttempt(attempt DiagnosticAttempt) DiagnosticAttempt {
	attempt.Services = append([]string(nil), attempt.Services...)
	attempt.Stages = append([]DiagnosticStage(nil), attempt.Stages...)
	for index := range attempt.Stages {
		attempt.Stages[index].Error = diagnosticSafeText(attempt.Stages[index].Error, diagnosticTextLimit)
	}
	attempt.Routes = append([]DiagnosticRoute(nil), attempt.Routes...)
	for index := range attempt.Routes {
		attempt.Routes[index].Target = diagnosticRouteValue(attempt.Routes[index].Target)
	}
	attempt.Logs = append([]DiagnosticLog(nil), attempt.Logs...)
	for index := range attempt.Logs {
		attempt.Logs[index].Tail = diagnosticSafeText(attempt.Logs[index].Tail, diagnosticLogLimit)
	}
	if attempt.Failure != nil {
		failure := *attempt.Failure
		failure.Error = diagnosticSafeText(failure.Error, diagnosticTextLimit)
		failure.LogTail = diagnosticSafeText(failure.LogTail, diagnosticLogLimit)
		failure.StartedServices = append([]string(nil), failure.StartedServices...)
		failure.PendingServices = append([]string(nil), failure.PendingServices...)
		failure.RolledBackServices = append([]string(nil), failure.RolledBackServices...)
		failure.RemainingServices = append([]string(nil), failure.RemainingServices...)
		attempt.Failure = &failure
	}
	return attempt
}

func readDiagnosticHistory(store *Store) (diagnosticHistory, error) {
	history := diagnosticHistory{Version: diagnosticsVersion, Attempts: []DiagnosticAttempt{}}
	directory := filepath.Join(store.Root, "diagnostics")
	path := filepath.Join(directory, "attempts.json")
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return history, nil
	}
	if err != nil {
		return history, fmt.Errorf("inspect startup diagnostics directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return history, fmt.Errorf("startup diagnostics path %q must be a real directory", directory)
	}
	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return history, nil
	}
	if err != nil {
		return history, fmt.Errorf("inspect startup diagnostics: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return history, fmt.Errorf("startup diagnostics %q must be a regular file, not a symbolic link", path)
	}
	if info.Size() > diagnosticsMaximumBytes {
		return history, fmt.Errorf("startup diagnostics exceed the %d byte limit", diagnosticsMaximumBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return history, fmt.Errorf("read startup diagnostics: %w", err)
	}
	if err := json.Unmarshal(data, &history); err != nil {
		return history, fmt.Errorf("decode startup diagnostics: %w", err)
	}
	if history.Version != diagnosticsVersion {
		return history, fmt.Errorf("unsupported startup diagnostics version %d", history.Version)
	}
	if history.Attempts == nil {
		history.Attempts = []DiagnosticAttempt{}
	}
	return history, nil
}

func writeDiagnosticHistory(store *Store, history diagnosticHistory) error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	directory := filepath.Join(store.Root, "diagnostics")
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		if err := os.Mkdir(directory, 0700); err != nil {
			return fmt.Errorf("create startup diagnostics directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect startup diagnostics directory: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("startup diagnostics path %q must be a real directory", directory)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("protect startup diagnostics directory: %w", err)
	}
	path := filepath.Join(directory, "attempts.json")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("startup diagnostics %q must be a regular file, not a symbolic link", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect startup diagnostics: %w", err)
	}
	history.Version = diagnosticsVersion
	var data []byte
	for {
		data, err = json.MarshalIndent(history, "", "  ")
		if err != nil {
			return fmt.Errorf("encode startup diagnostics: %w", err)
		}
		data = append(data, '\n')
		if len(data) <= diagnosticsMaximumBytes {
			break
		}
		if len(history.Attempts) <= 1 {
			return fmt.Errorf("startup diagnostics exceed the %d byte limit", diagnosticsMaximumBytes)
		}
		history.Attempts = history.Attempts[:len(history.Attempts)-1]
	}
	temporary, err := os.CreateTemp(directory, ".attempts-*.json")
	if err != nil {
		return fmt.Errorf("create temporary startup diagnostics: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary startup diagnostics: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write startup diagnostics: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync startup diagnostics: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close startup diagnostics: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish startup diagnostics: %w", err)
	}
	return nil
}

func diagnosticRoutes(plan *Plan) []DiagnosticRoute {
	routes := make([]DiagnosticRoute, 0)
	for _, service := range plan.Order {
		resolutions := plan.Resolutions[service]
		dependencies := make([]string, 0, len(resolutions))
		for dependency := range resolutions {
			dependencies = append(dependencies, dependency)
		}
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			resolution := resolutions[dependency]
			binding := ""
			if planned := plan.Services[service].Config; planned != nil {
				for _, route := range planned.Routes {
					if route.Dependency == dependency {
						binding = route.Binding
						break
					}
				}
			}
			target := resolution.Target
			if resolution.Address != "" {
				target = resolution.Address
			} else if resolution.Addresses != "" {
				target = resolution.Addresses
			}
			routes = append(routes, DiagnosticRoute{
				Service:    service,
				Dependency: dependency,
				Binding:    binding,
				Mode:       resolution.Mode,
				Target:     diagnosticRouteValue(target),
				Source:     resolution.Mode,
			})
		}
		routes = append(routes, diagnosticDisabledRoutes(plan, service, routes)...)
	}
	return routes
}

func diagnosticDisabledRoutes(plan *Plan, service string, existing []DiagnosticRoute) []DiagnosticRoute {
	if plan.Workspace == nil || plan.Workspace.Manifest == nil {
		return nil
	}
	disabled := make(map[string]bool, len(plan.Workspace.Manifest.Workspace.DisabledBindings))
	for _, binding := range plan.Workspace.Manifest.Workspace.DisabledBindings {
		disabled[binding] = true
	}
	seen := make(map[string]bool)
	for _, route := range existing {
		if route.Service == service {
			seen[route.Binding] = true
		}
	}
	manifestService := plan.Workspace.Manifest.Services[service]
	routes := make([]DiagnosticRoute, 0)
	for dependency, declaration := range manifestService.Dependencies {
		if declaration.Binding == "" || !disabled[declaration.Binding] || seen[declaration.Binding] {
			continue
		}
		seen[declaration.Binding] = true
		routes = append(routes, DiagnosticRoute{Service: service, Dependency: dependency, Binding: declaration.Binding, Mode: "disabled", Source: "workspace.disabledBindings"})
	}
	for _, binding := range manifestService.Discovery.EffectiveConsumerBindings() {
		if binding == "" || !disabled[binding] || seen[binding] {
			continue
		}
		seen[binding] = true
		routes = append(routes, DiagnosticRoute{Service: service, Binding: binding, Mode: "disabled", Source: "workspace.disabledBindings"})
	}
	sort.Slice(routes, func(left int, right int) bool { return routes[left].Binding < routes[right].Binding })
	return routes
}

func diagnosticFailureLog(plan *Plan, session *Session, stage string, service string) string {
	if plan == nil || service == "" {
		return ""
	}
	path := ""
	offset := int64(0)
	if session != nil {
		for _, process := range session.Services {
			if process.Name == service {
				path = process.LogPath
				offset = process.logOffset
				if offset == 0 {
					offset = process.LogOffset
				}
				break
			}
		}
	}
	if path == "" && (stage == "prepare" || stage == "build") {
		path = filepath.Join(plan.RunDir, "logs", service+"-"+stage+".log")
	}
	if path == "" || !pathWithinDirectory(filepath.Join(plan.RunDir, "logs"), path) {
		return ""
	}
	return readBoundedDiagnosticLog(filepath.Join(plan.RunDir, "logs"), path, offset)
}

func readBoundedDiagnosticLog(logsDirectory string, path string, offset int64) string {
	if !pathWithinDirectory(logsDirectory, path) || filepath.Clean(filepath.Dir(path)) != filepath.Clean(logsDirectory) {
		return ""
	}
	info, err := os.Lstat(logsDirectory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ""
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ""
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		unix.Close(descriptor)
		return ""
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || offset < 0 || offset >= info.Size() {
		return ""
	}
	start := offset
	if info.Size()-start > diagnosticLogLimit {
		start = info.Size() - diagnosticLogLimit
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, diagnosticLogLimit))
	if err != nil {
		return ""
	}
	if start > offset {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	return string(data)
}

func diagnosticRouteValue(value string) string {
	if RedactDiagnosticText(value) != value {
		return "[REDACTED]"
	}
	return value
}

func archiveSessionDiagnosticLogs(workspace *WorkspaceData, session *Session, names []string) error {
	if session == nil || session.AttemptID == "" {
		return nil
	}
	history, err := readDiagnosticHistory(workspace.Store)
	if err != nil {
		return err
	}
	for index := range history.Attempts {
		if history.Attempts[index].ID != session.AttemptID {
			continue
		}
		captured := diagnosticSessionLogs(workspace.Store, session, names, nil)
		if len(names) == 0 {
			history.Attempts[index].Logs = captured
		} else {
			history.Attempts[index].Logs = mergeDiagnosticLogs(history.Attempts[index].Logs, captured, names)
		}
		return writeDiagnosticHistory(workspace.Store, history)
	}
	return nil
}

func mergeDiagnosticLogs(existing []DiagnosticLog, captured []DiagnosticLog, names []string) []DiagnosticLog {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	merged := make([]DiagnosticLog, 0, len(existing)+len(captured))
	for _, log := range existing {
		if !selected[log.Service] {
			merged = append(merged, log)
		}
	}
	merged = append(merged, captured...)
	sort.Slice(merged, func(left int, right int) bool { return merged[left].Service < merged[right].Service })
	return merged
}

func diagnosticSessionLogs(store *Store, session *Session, names []string, secrets []string) []DiagnosticLog {
	if session == nil {
		return nil
	}
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	logs := make([]DiagnosticLog, 0, len(session.Services))
	remaining := diagnosticLogsPerAttemptLimit
	for _, process := range session.Services {
		if remaining == 0 {
			break
		}
		if len(selected) > 0 && !selected[process.Name] {
			continue
		}
		offset := process.LogOffset
		if process.logOffset > 0 {
			offset = process.logOffset
		}
		tail := readBoundedDiagnosticLog(filepath.Join(store.CurrentDir, "logs"), process.LogPath, offset)
		if tail == "" {
			continue
		}
		maximum := diagnosticLogLimit
		if maximum > remaining {
			maximum = remaining
		}
		safeTail := diagnosticSafeTextWithSecrets(tail, maximum, secrets)
		if len(safeTail) > remaining {
			safeTail = safeTail[:remaining]
		}
		logs = append(logs, DiagnosticLog{
			Service:    process.Name,
			CapturedAt: time.Now().UTC(),
			Tail:       safeTail,
		})
		remaining -= len(safeTail)
	}
	sort.Slice(logs, func(left int, right int) bool { return logs[left].Service < logs[right].Service })
	return logs
}

func diagnosticSafeText(value string, maximum int) string {
	return diagnosticSafeTextWithSecrets(value, maximum, nil)
}

func RedactDiagnosticText(value string) string {
	return diagnosticRedactText(value)
}

func (recorder *diagnosticRecorder) safeText(value string, maximum int) string {
	return diagnosticSafeTextWithSecrets(value, maximum, recorder.secrets)
}

func diagnosticSafeTextWithSecrets(value string, maximum int, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = diagnosticRedactText(value)
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 32 {
			return character
		}
		return -1
	}, value)
	if len(value) > maximum {
		value = value[:maximum] + "…"
	}
	return strings.TrimSpace(value)
}

func diagnosticRedactText(value string) string {
	if redacted, ok := diagnosticRedactJSONDocuments(value, 0); ok {
		return redacted
	}
	if strings.Contains(value, "\n") {
		lines := strings.Split(value, "\n")
		for index, line := range lines {
			if redacted, ok := diagnosticRedactJSONDocuments(line, 0); ok {
				lines[index] = redacted
			} else {
				lines[index] = diagnosticRedactUnstructuredText(line)
			}
		}
		return strings.Join(lines, "\n")
	}
	return diagnosticRedactUnstructuredText(value)
}

func diagnosticRedactUnstructuredText(value string) string {
	value = diagnosticURLUserPattern.ReplaceAllString(value, "$1[REDACTED]@")
	value = diagnosticJSONSecretPattern.ReplaceAllString(value, `$1"[REDACTED]"`)
	value = diagnosticHeaderPattern.ReplaceAllString(value, "$1$2[REDACTED]")
	value = diagnosticAuthorizationPattern.ReplaceAllString(value, "$1 [REDACTED]")
	value = diagnosticLongSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = diagnosticSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	return value
}

func diagnosticRedactJSONDocuments(value string, depth int) (string, bool) {
	if depth >= diagnosticJSONDepthLimit {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	documents := make([]string, 0, 1)
	for {
		var decoded interface{}
		err := decoder.Decode(&decoded)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		decoded = diagnosticRedactJSONValue(decoded, depth+1)
		redacted, err := json.Marshal(decoded)
		if err != nil {
			return "", false
		}
		documents = append(documents, string(redacted))
	}
	if len(documents) == 0 {
		return "", false
	}
	return strings.Join(documents, "\n"), true
}

func diagnosticRedactJSONValue(value interface{}, depth int) interface{} {
	if depth >= diagnosticJSONDepthLimit {
		switch value.(type) {
		case map[string]interface{}, []interface{}:
			return "[REDACTED]"
		case string:
			return "[REDACTED]"
		default:
			return value
		}
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, nested := range typed {
			if diagnosticSensitiveKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			typed[key] = diagnosticRedactJSONValue(nested, depth+1)
		}
	case []interface{}:
		for index, nested := range typed {
			typed[index] = diagnosticRedactJSONValue(nested, depth+1)
		}
	case string:
		if redacted, ok := diagnosticRedactJSONDocuments(typed, depth); ok {
			return redacted
		}
		return diagnosticRedactUnstructuredText(typed)
	}
	return value
}

func appendStopDiagnosticStage(workspace *WorkspaceData, attemptID string, service string, startedAt time.Time, failure error) error {
	if attemptID == "" {
		return nil
	}
	history, err := readDiagnosticHistory(workspace.Store)
	if err != nil {
		return err
	}
	for index := range history.Attempts {
		if history.Attempts[index].ID != attemptID {
			continue
		}
		finished := time.Now().UTC()
		stage := DiagnosticStage{
			Name:       "stop",
			Service:    service,
			Status:     "completed",
			StartedAt:  startedAt,
			FinishedAt: &finished,
		}
		if failure != nil {
			stage.Status = "failed"
			stage.Error = diagnosticSafeText(failure.Error(), diagnosticTextLimit)
		}
		history.Attempts[index].Stages = append(history.Attempts[index].Stages, stage)
		return writeDiagnosticHistory(workspace.Store, history)
	}
	return nil
}

func diagnosticPlanSecrets(plan *Plan) []string {
	secrets := make([]string, 0)
	for key, value := range plan.Environment.Env {
		if diagnosticSensitiveKey(key) && value != "" {
			secrets = append(secrets, value)
		}
	}
	for _, service := range plan.Services {
		for _, entry := range service.Environment {
			key, value, found := strings.Cut(entry, "=")
			if found && diagnosticSensitiveKey(key) && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	return secrets
}

func diagnosticSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range []string{"authorization", "password", "passwd", "token", "secret", "api_key", "apikey", "credential", "cookie", "session"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func diagnosticFailureAdvice(stage string, service string) (string, string) {
	suffix := ""
	if service != "" {
		suffix = " for service " + service
	}
	switch stage {
	case "plan", "validation", "workspace", "session-transition":
		return "configuration", "Correct the workspace manifest or selected environment reported by the failure, then retry startup."
	case "endpoints", "connection":
		return "dependency", "Restore the reported external endpoint or connection prerequisite, then retry startup."
	case "materialize", "preflight", "runtime-preflight", "service-preflight", "run-workdir":
		return "runtime-configuration", "Correct the reported runtime configuration or isolation check" + suffix + ", then retry startup."
	case "prepare", "build":
		return "build", "Inspect the saved " + stage + " log" + suffix + ", correct the command failure, then retry startup."
	case "start", "verify", "finalize", "restart":
		return "service-startup", "Inspect the saved service log and reported runtime check" + suffix + ", correct the failure, then retry."
	case "stop", "cleanup":
		return "cleanup", "Inspect the saved session before retrying; manually stop only the specifically reported remaining process if it is still running."
	default:
		return "unknown", "Inspect the reported stage" + suffix + ", correct the cause, then retry."
	}
}
