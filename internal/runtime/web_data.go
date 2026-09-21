package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/model"
	"golang.org/x/sys/unix"
)

const (
	webHealthProbeTimeout = 2 * time.Second
	webHealthStaleAfter = 30 * time.Second
	webHealthFailureThreshold = 2
	webHealthProbeConcurrency = 4
	webLogPageLines = 200
	webLogScanBytes = 4 * 1024 * 1024 + 64
	webLogLineBytes = 64 * 1024
	webLogCursorBytes = 16 * 1024
	webLogFilterBytes = 1024
)

type WebSnapshotData struct {
	Workspace    string              `json:"workspace"`
	Version      string              `json:"version"`
	Environment  string              `json:"environment"`
	LANAddress   string              `json:"lanAddress"`
	LANInterface string              `json:"lanInterface"`
	DisabledBindings []string        `json:"disabledBindings"`
	SessionToken string              `json:"sessionToken"`
	ConfigurationError string        `json:"configurationError,omitempty"`
	Services     []WebService        `json:"services"`
	Routes       []WebRoute          `json:"routes"`
	Attempts     []DiagnosticAttempt `json:"attempts"`
	Connection   *WebConnection      `json:"connection"`
}

type WebService struct {
	Name         string            `json:"name"`
	PID          int               `json:"pid"`
	State        string            `json:"state"`
	Ports        map[string]int    `json:"ports"`
	Listeners    []WebListener     `json:"listeners"`
	Verification string            `json:"verification"`
	Health       WebHealth         `json:"health"`
}

type WebListener struct {
	Name       string    `json:"name"`
	Address    string    `json:"address"`
	Port       int       `json:"port"`
	Mode       string    `json:"mode"`
	OwnerPID   int       `json:"ownerPid"`
	VerifiedAt time.Time `json:"verifiedAt"`
}

type WebRoute struct {
	Consumer string `json:"consumer"`
	Provider string `json:"provider"`
	Binding  string `json:"binding,omitempty"`
	Target   string `json:"target,omitempty"`
	Mode     string `json:"mode"`
	Source   string `json:"source,omitempty"`
}

type WebConnection struct {
	Driver   string `json:"driver"`
	PID      int    `json:"pid"`
	State    string `json:"state"`
	Owned    bool   `json:"owned"`
	Managed  bool   `json:"managed"`
	Elevated bool   `json:"elevated"`
}

type WebHealth struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checkedAt"`
	Message   string    `json:"message"`
}

func (health WebHealth) MarshalJSON() ([]byte, error) {
	var checkedAt *time.Time
	if !health.CheckedAt.IsZero() {
		value := health.CheckedAt
		checkedAt = &value
	}
	return json.Marshal(struct {
		Status    string     `json:"status"`
		CheckedAt *time.Time `json:"checkedAt"`
		Message   string     `json:"message"`
	}{Status: health.Status, CheckedAt: checkedAt, Message: health.Message})
}

type WebLogQuery struct {
	Services  []string
	Cursor    string
	Query     string
	Level     string
	RequestID string
	TraceID   string
	Attempt   string
	Since     string
	Until     string
}

type WebLogPage struct {
	Entries []WebLogEntry `json:"entries"`
	Cursor  string        `json:"cursor"`
	HasMore bool          `json:"hasMore"`
	Reset   bool          `json:"reset"`
}

type WebLogEntry struct {
	ID        string     `json:"id"`
	Service   string     `json:"service"`
	Text      string     `json:"text"`
	Time      *time.Time `json:"time,omitempty"`
	Level     string     `json:"level,omitempty"`
	RequestID string     `json:"requestId,omitempty"`
	TraceID   string     `json:"traceId,omitempty"`
}

type webHealthCacheEntry struct {
	key      string
	health   WebHealth
	failures int
	nextProbeAt time.Time
}

var webHealthCache = struct {
	sync.Mutex
	workspaces map[string]map[string]webHealthCacheEntry
}{workspaces: make(map[string]map[string]webHealthCacheEntry)}

var webConfigurationErrors = struct {
	sync.Mutex
	values map[string]string
}{values: make(map[string]string)}

type webLogPosition struct {
	Generation string `json:"generation,omitempty"`
	Offset     int64  `json:"offset"`
	Pending    bool   `json:"pending,omitempty"`
	Discarding bool   `json:"discarding,omitempty"`
}

type webLogCursor struct {
	Version  int                       `json:"version"`
	Services []string                  `json:"services"`
	Sources  map[string]webLogPosition `json:"sources"`
}

type webLogSource struct {
	name string
	path string
	root string
	minimumOffset int64
	data []byte
	generation string
}

type webParsedLog struct {
	text      string
	time      *time.Time
	level     string
	requestID string
	traceID   string
	attempt   string
}

var webLogSecretPattern = regexp.MustCompile(`(?is)\b(authorization|password|passwd|token|secret|api[_-]?key|credential|cookie|set-cookie)(\s*[=:]\s*|\s+)([^\s,;]+)`)
var webLogAuthorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/-]+=*`)
var webLogURLUserPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
var webLogURLSecretPattern = regexp.MustCompile(`(?i)([?&](?:authorization|password|passwd|token|secret|api[_-]?key|credential|cookie)=)[^&#\s]+`)
var webLogSecretOnlyPattern = regexp.MustCompile(`(?i)^\s*(authorization|password|passwd|token|secret|api[_-]?key|credential|cookie|set-cookie)\s*[=:]?\s*$`)
var webLogANSIPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var webLogOSCPattern = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
var webLogTimePattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b`)
var webLogLevelPattern = regexp.MustCompile(`(?i)(?:^|[\s\[])\b(trace|debug|info|warn|warning|error|fatal)\b`)

// OpenWebWorkspace tolerates a malformed manifest so the read-only diagnostic
// viewer remains available. Mutating actions reopen the workspace strictly.
func OpenWebWorkspace(options CommonOptions) (*WorkspaceData, error) {
	configPath, root, err := config.ResolvePath(options.Cwd)
	if err != nil {
		return nil, err
	}
	manifest, manifestErr := config.Load(configPath)
	settings, settingsErr := config.EffectiveSettings(root, "")
	if settings == nil {
		settings = map[string]string{}
	}
	store, err := NewStore(root)
	if err != nil {
		return nil, err
	}
	configurationError := manifestErr
	if configurationError == nil && manifest.Version < 3 {
		configurationError = fmt.Errorf("Conven manifest %q uses version %d; run conven workspace --migrate before using workspace commands", configPath, manifest.Version)
	}
	if configurationError == nil {
		configurationError = settingsErr
	}
	if manifest == nil {
		manifest = &model.Manifest{Workspace: model.Workspace{Name: filepath.Base(root)}, Services: map[string]model.Service{}, Environments: map[string]model.Environment{}, Policies: map[string]model.Policy{}}
	}
	workspace := &WorkspaceData{Root: root, ConfigPath: configPath, Manifest: manifest, Settings: settings, Store: store}
	webConfigurationErrors.Lock()
	if configurationError != nil {
		webConfigurationErrors.values[root] = RedactDiagnosticText(configurationError.Error())
	} else {
		delete(webConfigurationErrors.values, root)
	}
	webConfigurationErrors.Unlock()
	return workspace, nil
}

func WebSnapshot(ctx context.Context, workspace *WorkspaceData, version string) (WebSnapshotData, error) {
	if workspace == nil || workspace.Store == nil || workspace.Manifest == nil {
		return WebSnapshotData{}, errors.New("read Web snapshot: workspace data is required")
	}
	if err := ctx.Err(); err != nil {
		return WebSnapshotData{}, err
	}
	workspaceName, configurationError := webReloadConfigurationStatus(workspace)
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		return WebSnapshotData{}, err
	}
	for attemptIndex := range attempts {
		attempts[attemptIndex].Logs = nil
		for routeIndex := range attempts[attemptIndex].Routes {
			attempts[attemptIndex].Routes[routeIndex].Target = RedactDiagnosticText(attempts[attemptIndex].Routes[routeIndex].Target)
		}
	}
	snapshot := WebSnapshotData{
		Workspace:  workspaceName,
		Version:    strings.TrimSpace(version),
		Services:   []WebService{},
		Routes:     []WebRoute{},
		Attempts:   attempts,
		Connection: nil,
	}
	snapshot.ConfigurationError = configurationError
	snapshot.LANAddress, snapshot.LANInterface = discoverLocalIPv4()
	snapshot.DisabledBindings = append([]string{}, workspace.Manifest.Workspace.DisabledBindings...)
	sort.Strings(snapshot.DisabledBindings)
	if snapshot.Workspace == "" {
		snapshot.Workspace = filepath.Base(workspace.Root)
	}
	if snapshot.Version == "" {
		snapshot.Version = "dev"
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return WebSnapshotData{}, err
	}
	if session == nil {
		if len(attempts) > 0 {
			snapshot.Environment = attempts[0].Environment
			seen := make(map[string]bool, len(attempts[0].Services))
			for _, name := range attempts[0].Services {
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				snapshot.Services = append(snapshot.Services, WebService{Name: name, State: "stopped", Ports: map[string]int{}, Listeners: []WebListener{}, Verification: "historical", Health: WebHealth{Status: "unknown", Message: "No running session health evidence is available."}})
			}
		}
		return snapshot, nil
	}
	snapshot.Environment = session.Environment
	snapshot.SessionToken, err = replacementSessionToken(session)
	if err != nil {
		return WebSnapshotData{}, err
	}
	for _, process := range session.Services {
		state := webProcessState(process)
		service := WebService{
			Name:         process.Name,
			PID:          process.PID,
			State:        state,
			Ports:        webCopyPorts(process.Ports),
			Listeners:    webListeners(process.Listeners),
			Verification: process.Verification,
			Health:       webCachedHealth(workspace.Root, snapshot.SessionToken, process, state),
		}
		snapshot.Services = append(snapshot.Services, service)
	}
	if len(session.RuntimeRoutes) > 0 {
		for _, route := range session.RuntimeRoutes {
			snapshot.Routes = append(snapshot.Routes, WebRoute{Consumer: route.Service, Provider: route.Dependency, Binding: route.Binding, Target: RedactDiagnosticText(route.Target), Mode: route.Mode, Source: route.Source})
		}
	} else if session.AttemptID != "" {
		for _, attempt := range attempts {
			if attempt.ID != session.AttemptID {
				continue
			}
			for _, route := range attempt.Routes {
				snapshot.Routes = append(snapshot.Routes, WebRoute{Consumer: route.Service, Provider: route.Dependency, Binding: route.Binding, Target: RedactDiagnosticText(route.Target), Mode: route.Mode, Source: route.Source})
			}
			break
		}
	}
	if session.Connection != nil {
		connection := session.Connection
		managed := ServiceProcess{Name: "connection/" + connection.Driver, PID: connection.PID, PGID: connection.PGID, Command: connection.Command, Identity: connection.Identity}
		snapshot.Connection = &WebConnection{Driver: connection.Driver, PID: connection.PID, State: webProcessState(managed), Owned: connection.Owned, Managed: connection.Managed, Elevated: connection.Elevated}
	}
	return snapshot, nil
}

func webReloadConfigurationStatus(workspace *WorkspaceData) (string, string) {
	workspaceName := strings.TrimSpace(workspace.Manifest.Workspace.Name)
	manifest, manifestErr := config.Load(workspace.ConfigPath)
	if manifestErr == nil {
		workspaceName = strings.TrimSpace(manifest.Workspace.Name)
		if manifest.Version < 3 {
			manifestErr = fmt.Errorf("Conven manifest %q uses version %d; run conven workspace --migrate before using workspace commands", workspace.ConfigPath, manifest.Version)
		}
	}
	_, settingsErr := config.EffectiveSettings(workspace.Root, "")
	configurationError := manifestErr
	if configurationError == nil {
		configurationError = settingsErr
	}
	message := ""
	if configurationError != nil {
		message = RedactDiagnosticText(configurationError.Error())
	}
	webConfigurationErrors.Lock()
	if message == "" {
		delete(webConfigurationErrors.values, workspace.Root)
	} else {
		webConfigurationErrors.values[workspace.Root] = message
	}
	webConfigurationErrors.Unlock()
	return workspaceName, message
}

func RefreshWebHealth(ctx context.Context, workspace *WorkspaceData, force ...bool) (map[string]WebHealth, error) {
	if workspace == nil || workspace.Store == nil || workspace.Manifest == nil {
		return nil, errors.New("refresh Web health: workspace data is required")
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return nil, err
	}
	if session == nil {
		webHealthCache.Lock()
		delete(webHealthCache.workspaces, workspace.Root)
		webHealthCache.Unlock()
		return map[string]WebHealth{}, nil
	}
	sessionToken, err := replacementSessionToken(session)
	if err != nil {
		return nil, err
	}
	forceProbe := len(force) > 0 && force[0]
	type healthResult struct {
		process ServiceProcess
		health  WebHealth
		failed  bool
		skipped bool
	}
	jobs := make(chan ServiceProcess)
	results := make(chan healthResult, len(session.Services))
	workers := webHealthProbeConcurrency
	if workers > len(session.Services) {
		workers = len(session.Services)
	}
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for process := range jobs {
				key := webHealthKey(sessionToken, process)
				webHealthCache.Lock()
				cached, found := webHealthCache.workspaces[workspace.Root][process.Name]
				webHealthCache.Unlock()
				if !forceProbe && found && cached.key == key && !cached.nextProbeAt.IsZero() && time.Now().Before(cached.nextProbeAt) {
					results <- healthResult{process: process, health: cached.health, skipped: true}
					continue
				}
				health, failed := webProbeServiceHealth(ctx, session.HealthChecks, process)
				results <- healthResult{process: process, health: health, failed: failed}
			}
		}()
	}
	go func() {
		for _, process := range session.Services {
			jobs <- process
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	collected := make([]healthResult, 0, len(session.Services))
	for result := range results {
		collected = append(collected, result)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := workspace.Store.Load()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("Web health session ended during refresh")
	}
	currentToken, err := replacementSessionToken(current)
	if err != nil {
		return nil, err
	}
	if currentToken != sessionToken {
		return nil, errors.New("Web health session changed during refresh")
	}
	now := time.Now().UTC()
	webHealthCache.Lock()
	cache := webHealthCache.workspaces[workspace.Root]
	if cache == nil {
		cache = make(map[string]webHealthCacheEntry)
		webHealthCache.workspaces[workspace.Root] = cache
	}
	refreshed := make(map[string]WebHealth, len(collected))
	active := make(map[string]bool, len(collected))
	for _, result := range collected {
		key := webHealthKey(sessionToken, result.process)
		previous := cache[result.process.Name]
		if result.skipped && previous.key == key {
			refreshed[result.process.Name] = previous.health
			active[result.process.Name] = true
			continue
		}
		entry := webHealthCacheEntry{key: key, health: result.health}
		if result.failed {
			if previous.key == key {
				entry.failures = previous.failures + 1
			} else {
				entry.failures = 1
			}
			entry.health.CheckedAt = now
			if entry.failures >= webHealthFailureThreshold {
				entry.health.Status = "unhealthy"
				entry.health.Message = "Network health check failed repeatedly."
			} else if previous.key == key && (previous.health.Status == "healthy" || previous.health.Status == "stale") {
				entry.health.Status = "stale"
				entry.health.Message = "Last healthy evidence is stale after a failed probe."
			} else {
				entry.health.Status = "unknown"
				entry.health.Message = "A single failed probe is not sufficient health evidence."
			}
			delay := 10 * time.Second
			for count := 1; count < entry.failures && delay < 60*time.Second; count++ {
				delay *= 2
			}
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			entry.nextProbeAt = now.Add(delay)
		}
		cache[result.process.Name] = entry
		refreshed[result.process.Name] = entry.health
		active[result.process.Name] = true
	}
	for name := range cache {
		if !active[name] {
			delete(cache, name)
		}
	}
	webHealthCache.Unlock()
	return refreshed, nil
}

func WebLogs(workspace *WorkspaceData, query WebLogQuery) (WebLogPage, error) {
	if workspace == nil || workspace.Store == nil {
		return WebLogPage{}, errors.New("read Web logs: workspace store is required")
	}
	if err := validateWebLogQuery(query); err != nil {
		return WebLogPage{}, err
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return WebLogPage{}, err
	}
	sources, err := webLogSources(workspace, session, query)
	if err != nil {
		return WebLogPage{}, err
	}
	names := make([]string, len(sources))
	for index, source := range sources {
		names[index] = source.name
	}
	cursor, reset, err := decodeWebLogCursor(query.Cursor, names)
	if err != nil {
		return WebLogPage{}, err
	}
	page := WebLogPage{Entries: []WebLogEntry{}, Reset: reset}
	if cursor.Sources == nil {
		cursor.Sources = make(map[string]webLogPosition)
	}
	since, until, err := webLogTimeBounds(query.Since, query.Until)
	if err != nil {
		return WebLogPage{}, err
	}
	remainingBytes := int64(webLogScanBytes)
	remainingLines := webLogPageLines
	for sourceIndex, source := range sources {
		if remainingBytes <= 0 || remainingLines <= 0 {
			page.HasMore = true
			break
		}
		position := cursor.Sources[source.name]
		share := remainingLines / (len(sources) - sourceIndex)
		if share < 1 {
			share = 1
		}
		byteShare := remainingBytes / int64(len(sources) - sourceIndex)
		if byteShare < 1 {
			byteShare = 1
		}
		entries, next, more, sourceReset, scanned, err := readWebLogSource(workspace, source, position, query.Cursor == "", share, byteShare, query, since, until)
		if err != nil {
			return WebLogPage{}, err
		}
		cursor.Sources[source.name] = next
		page.Entries = append(page.Entries, entries...)
		remainingLines -= len(entries)
		remainingBytes -= scanned
		page.HasMore = page.HasMore || more
		page.Reset = page.Reset || sourceReset
	}
	page.Cursor, err = encodeWebLogCursor(cursor)
	if err != nil {
		return WebLogPage{}, err
	}
	return page, nil
}

func webProcessState(process ServiceProcess) string {
	if !ProcessAlive(process.PID) {
		return "stopped"
	}
	if VerifyProcess(process) != nil {
		return "unverified"
	}
	return "running"
}

func webCopyPorts(ports map[string]int) map[string]int {
	result := make(map[string]int, len(ports))
	for name, port := range ports {
		result[name] = port
	}
	return result
}

func webListeners(listeners map[string]ListenerEvidence) []WebListener {
	names := make([]string, 0, len(listeners))
	for name := range listeners {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]WebListener, 0, len(names))
	for _, name := range names {
		listener := listeners[name]
		result = append(result, WebListener{Name: name, Address: listener.Address, Port: listener.Port, Mode: listener.Mode, OwnerPID: listener.OwnerPID, VerifiedAt: listener.VerifiedAt})
	}
	return result
}

func webHealthKey(sessionToken string, process ServiceProcess) string {
	return sessionToken + ":" + process.Name + ":" + strconv.Itoa(process.PID) + ":" + process.SourceFingerprint + ":" + process.PlanFingerprint
}

func webCachedHealth(workspace string, sessionToken string, process ServiceProcess, state string) WebHealth {
	if state != "running" {
		return WebHealth{Status: "unknown", Message: "The saved process is not verified as running."}
	}
	key := webHealthKey(sessionToken, process)
	webHealthCache.Lock()
	entry, found := webHealthCache.workspaces[workspace][process.Name]
	webHealthCache.Unlock()
	if !found || entry.key != key {
		return WebHealth{Status: "unknown", Message: "No network health evidence is available."}
	}
	health := entry.health
	if health.Status == "healthy" && !health.CheckedAt.IsZero() && time.Since(health.CheckedAt) > webHealthStaleAfter {
		health.Status = "stale"
		health.Message = "Network health evidence is stale."
	}
	return health
}

func webProbeServiceHealth(ctx context.Context, declared []SessionHealthCheck, process ServiceProcess) (WebHealth, bool) {
	checkedAt := time.Now().UTC()
	if webProcessState(process) != "running" {
		return WebHealth{Status: "unknown", CheckedAt: checkedAt, Message: "The saved process is not verified as running."}, false
	}
	checks := make([]HealthCheck, 0)
	for _, declaration := range declared {
		if declaration.Name != process.Name {
			continue
		}
		check := HealthCheck{Type: declaration.Type, Address: declaration.Address, URL: declaration.URL}
		switch check.Type {
		case "tcp":
		case "http":
		default:
			continue
		}
		checks = append(checks, check)
	}
	if len(checks) == 0 {
		return WebHealth{Status: "unknown", CheckedAt: checkedAt, Message: "No HTTP or TCP health check is declared."}, false
	}
	for _, check := range checks {
		probeContext, cancel := context.WithTimeout(ctx, webHealthProbeTimeout)
		err := webNetworkHealthCheck(probeContext, check)
		cancel()
		if err != nil {
			return WebHealth{Status: "unknown", CheckedAt: checkedAt, Message: "Network health check failed."}, true
		}
	}
	return WebHealth{Status: "healthy", CheckedAt: checkedAt, Message: "All declared network health checks succeeded."}, false
}

func webNetworkHealthCheck(ctx context.Context, check HealthCheck) error {
	switch check.Type {
	case "tcp":
		if strings.TrimSpace(check.Address) == "" {
			return errors.New("empty TCP health address")
		}
		connection, err := (&net.Dialer{Timeout: webHealthProbeTimeout}).DialContext(ctx, "tcp", check.Address)
		if err != nil {
			return err
		}
		return connection.Close()
	case "http":
		parsed, err := url.Parse(check.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return errors.New("unsafe HTTP health URL")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: webHealthProbeTimeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 400 {
			return fmt.Errorf("HTTP status %d", response.StatusCode)
		}
		return nil
	default:
		return errors.New("Web health supports only HTTP and TCP checks")
	}
}

func validateWebLogQuery(query WebLogQuery) error {
	if len(query.Services) > 64 {
		return errors.New("too many Web log services requested")
	}
	for _, value := range []string{query.Query, query.Level, query.RequestID, query.TraceID, query.Attempt, query.Since, query.Until} {
		if len(value) > webLogFilterBytes {
			return errors.New("Web log filter is too long")
		}
	}
	if len(query.Cursor) > webLogCursorBytes {
		return errors.New("Web log cursor is too long")
	}
	return nil
}

func webLogSources(workspace *WorkspaceData, session *Session, query WebLogQuery) ([]webLogSource, error) {
	if query.Attempt != "" && (session == nil || session.AttemptID != query.Attempt) {
		return webDiagnosticLogSources(workspace, query)
	}
	if session == nil {
		attempts, err := ReadDiagnostics(workspace)
		if err != nil {
			return nil, err
		}
		if len(attempts) == 0 {
			return []webLogSource{}, nil
		}
		query.Attempt = attempts[0].ID
		return webDiagnosticLogSources(workspace, query)
	}
	requested := query.Services
	available := make(map[string]string, len(session.Services)+1)
	minimumOffsets := make(map[string]int64, len(session.Services))
	for _, process := range session.Services {
		available[process.Name] = process.LogPath
		minimumOffsets[process.Name] = process.LogOffset
	}
	if session.Connection != nil && session.Connection.LogPath != "" {
		available["connection/"+session.Connection.Driver] = session.Connection.LogPath
	}
	if len(requested) == 0 {
		requested = make([]string, 0, len(available))
		for name := range available {
			requested = append(requested, name)
		}
		sort.Strings(requested)
	}
	seen := make(map[string]bool, len(requested))
	sources := make([]webLogSource, 0, len(requested))
	for _, name := range requested {
		if name == "" || seen[name] {
			return nil, fmt.Errorf("invalid or duplicate Web log service %q", name)
		}
		path, found := available[name]
		if !found {
			return nil, fmt.Errorf("service %q is not part of the current session", name)
		}
		root := filepath.Join(workspace.Store.CurrentDir, "logs")
		if strings.HasPrefix(name, "connection/") {
			root = workspace.Store.Root
			if filepath.Clean(path) != ConnectionLogPath(workspace.Store.Root) {
				return nil, fmt.Errorf("connection %q has an unsafe log source", name)
			}
		} else if path == "" || !pathWithinDirectory(root, path) {
			return nil, fmt.Errorf("service %q has an unsafe log source", name)
		}
		seen[name] = true
		sources = append(sources, webLogSource{name: name, path: filepath.Clean(path), root: root, minimumOffset: minimumOffsets[name]})
	}
	return sources, nil
}

func webDiagnosticLogSources(workspace *WorkspaceData, query WebLogQuery) ([]webLogSource, error) {
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		return nil, err
	}
	var matched *DiagnosticAttempt
	for index := range attempts {
		if attempts[index].ID == query.Attempt {
			matched = &attempts[index]
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("diagnostic attempt %q is not available", query.Attempt)
	}
	allowed := make(map[string]bool, len(matched.Services))
	for _, name := range matched.Services {
		allowed[name] = true
	}
	for _, name := range query.Services {
		if !allowed[name] {
			return nil, fmt.Errorf("service %q is not part of diagnostic attempt %q", name, query.Attempt)
		}
	}
	sources := make([]webLogSource, 0, len(matched.Logs))
	for index, archived := range matched.Logs {
		if len(allowed) > 0 && !allowed[archived.Service] {
			continue
		}
		if len(query.Services) > 0 && !webContainsString(query.Services, archived.Service) {
			continue
		}
		data := []byte(archived.Tail)
		if len(data) == 0 {
			continue
		}
		if data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		generation := "diagnostic-" + matched.ID + "-" + strconv.Itoa(index) + "-" + strconv.FormatInt(archived.CapturedAt.UnixNano(), 10)
		sources = append(sources, webLogSource{name: archived.Service, data: data, generation: generation})
	}
	if len(sources) > 0 || matched.Failure == nil || matched.Failure.LogTail == "" {
		return sources, nil
	}
	name := matched.Failure.Service
	if name == "" && len(matched.Services) == 1 {
		name = matched.Services[0]
	}
	if name == "" {
		name = "diagnostic"
	}
	if len(query.Services) > 0 && !webContainsString(query.Services, name) {
		return []webLogSource{}, nil
	}
	data := []byte(matched.Failure.LogTail)
	if data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return []webLogSource{{name: name, data: data, generation: "diagnostic-" + matched.ID + "-failure"}}, nil
}

func decodeWebLogCursor(value string, services []string) (webLogCursor, bool, error) {
	cursor := webLogCursor{Version: 1, Services: append([]string(nil), services...), Sources: make(map[string]webLogPosition)}
	if value == "" {
		return cursor, false, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) > webLogCursorBytes {
		return cursor, false, errors.New("invalid Web log cursor")
	}
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 || cursor.Sources == nil {
		return webLogCursor{}, false, errors.New("invalid Web log cursor")
	}
	if len(cursor.Sources) > 64 {
		return webLogCursor{}, false, errors.New("invalid Web log cursor")
	}
	if !webEqualStrings(cursor.Services, services) {
		return webLogCursor{Version: 1, Services: append([]string(nil), services...), Sources: make(map[string]webLogPosition)}, true, nil
	}
	for name, position := range cursor.Sources {
		if position.Offset < 0 || !webContainsString(services, name) {
			return webLogCursor{}, false, errors.New("invalid Web log cursor")
		}
	}
	return cursor, false, nil
}

func encodeWebLogCursor(cursor webLogCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	if len(data) > webLogCursorBytes {
		return "", errors.New("Web log cursor exceeds its size limit")
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func readWebLogSource(workspace *WorkspaceData, source webLogSource, position webLogPosition, initial bool, lineLimit int, byteLimit int64, query WebLogQuery, since *time.Time, until *time.Time) ([]WebLogEntry, webLogPosition, bool, bool, int64, error) {
	if source.data != nil {
		return readWebDiagnosticLogSource(source, position, initial, lineLimit, byteLimit, query, since, until)
	}
	file, info, generation, err := openWebLogFile(source.root, source.path)
	if os.IsNotExist(err) {
		return []WebLogEntry{}, webLogPosition{}, false, position.Generation != "", 0, nil
	}
	if err != nil {
		return nil, position, false, false, 0, err
	}
	defer file.Close()
	reset := false
	if position.Generation != "" && (position.Generation != generation || position.Offset > info.Size()) {
		position = webLogPosition{}
		initial = true
		reset = true
	}
	position.Generation = generation
	minimumOffset := source.minimumOffset
	if minimumOffset < 0 {
		minimumOffset = 0
	}
	if minimumOffset > info.Size() {
		minimumOffset = info.Size()
	}
	if position.Offset < minimumOffset {
		if position.Offset > 0 {
			reset = true
		}
		position.Offset = minimumOffset
		initial = true
	}
	tailStart := false
	if initial && info.Size()-position.Offset > byteLimit {
		position.Offset = info.Size() - byteLimit
		if position.Offset < minimumOffset {
			position.Offset = minimumOffset
		}
		tailStart = position.Offset > minimumOffset
		position.Pending = false
	}
	if _, err := file.Seek(position.Offset, io.SeekStart); err != nil {
		return nil, position, false, reset, 0, err
	}
	maximum := byteLimit
	if remaining := info.Size() - position.Offset; maximum > remaining {
		maximum = remaining
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum))
	if err != nil {
		return nil, position, false, reset, 0, err
	}
	readBytes := int64(len(data))
	baseOffset := position.Offset
	if position.Discarding {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			position.Offset += int64(len(data))
			return []WebLogEntry{}, position, position.Offset < info.Size(), reset, int64(len(data)), nil
		}
		data = data[newline+1:]
		baseOffset += int64(newline + 1)
		position.Offset = baseOffset
		position.Discarding = false
		position.Pending = false
	}
	if tailStart {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
			baseOffset += int64(newline + 1)
		} else {
			position.Offset += int64(len(data))
			return []WebLogEntry{}, position, position.Offset < info.Size(), reset, int64(len(data)), nil
		}
	}
	entries := make([]WebLogEntry, 0, lineLimit)
	consumed := 0
	pending := position.Pending
	for consumed < len(data) && len(entries) < lineLimit {
		end := bytes.IndexByte(data[consumed:], '\n')
		if end < 0 {
			if len(data)-consumed > webLogLineBytes {
				consumed = len(data)
				position.Discarding = true
				position.Pending = false
			}
			break
		}
		lineEnd := consumed + end
		advance := end + 1
		raw := data[consumed:lineEnd]
		lineOffset := baseOffset + int64(consumed)
		consumed += advance
		if len(raw) > webLogLineBytes {
			pending = false
			continue
		}
		parsed, nextPending := parseWebLogLine(string(bytes.TrimSuffix(raw, []byte{'\r'})), pending)
		pending = nextPending
		if !webLogMatches(parsed, query, since, until) {
			continue
		}
		entries = append(entries, WebLogEntry{ID: source.name + ":" + generation + ":" + strconv.FormatInt(lineOffset, 10), Service: source.name, Text: parsed.text, Time: parsed.time, Level: parsed.level, RequestID: parsed.requestID, TraceID: parsed.traceID})
	}
	position.Offset = baseOffset + int64(consumed)
	position.Pending = pending
	more := position.Offset < info.Size()
	return entries, position, more, reset, readBytes, nil
}

func readWebDiagnosticLogSource(source webLogSource, position webLogPosition, initial bool, lineLimit int, byteLimit int64, query WebLogQuery, since *time.Time, until *time.Time) ([]WebLogEntry, webLogPosition, bool, bool, int64, error) {
	size := int64(len(source.data))
	reset := false
	if position.Generation != "" && (position.Generation != source.generation || position.Offset > size) {
		position = webLogPosition{}
		initial = true
		reset = true
	}
	position.Generation = source.generation
	if initial && position.Offset == 0 && size > byteLimit {
		position.Offset = size - byteLimit
	}
	start := position.Offset
	end := size
	if end-start > byteLimit {
		end = start + byteLimit
	}
	data := source.data[start:end]
	readBytes := int64(len(data))
	baseOffset := start
	if position.Discarding {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			position.Offset += int64(len(data))
			return []WebLogEntry{}, position, position.Offset < size, reset, int64(len(data)), nil
		}
		data = data[newline+1:]
		baseOffset += int64(newline + 1)
		position.Offset = baseOffset
		position.Discarding = false
		position.Pending = false
	}
	if initial && baseOffset > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			position.Offset += int64(len(data))
			return []WebLogEntry{}, position, position.Offset < size, reset, int64(len(data)), nil
		}
		data = data[newline+1:]
		baseOffset += int64(newline + 1)
	}
	entries := make([]WebLogEntry, 0, lineLimit)
	consumed := 0
	pending := position.Pending
	for consumed < len(data) && len(entries) < lineLimit {
		newline := bytes.IndexByte(data[consumed:], '\n')
		if newline < 0 {
			if len(data)-consumed > webLogLineBytes {
				consumed = len(data)
				position.Discarding = true
				position.Pending = false
			}
			break
		}
		raw := data[consumed : consumed+newline]
		lineOffset := baseOffset + int64(consumed)
		consumed += newline + 1
		if len(raw) > webLogLineBytes {
			pending = false
			continue
		}
		parsed, nextPending := parseWebLogLine(string(bytes.TrimSuffix(raw, []byte{'\r'})), pending)
		pending = nextPending
		if webLogMatches(parsed, query, since, until) {
			entries = append(entries, WebLogEntry{ID: source.name + ":" + source.generation + ":" + strconv.FormatInt(lineOffset, 10), Service: source.name, Text: parsed.text, Time: parsed.time, Level: parsed.level, RequestID: parsed.requestID, TraceID: parsed.traceID})
		}
	}
	position.Offset = baseOffset + int64(consumed)
	position.Pending = pending
	return entries, position, position.Offset < size, reset, readBytes, nil
}

func openWebLogFile(root string, path string) (*os.File, os.FileInfo, string, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinDirectory(root, path) {
		return nil, nil, "", errors.New("Web log source is outside the runtime log directory")
	}
	current := root
	info, err := os.Lstat(current)
	if err != nil {
		return nil, nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, nil, "", errors.New("Web log directory must be a real directory")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return nil, nil, "", err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, nil, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, "", errors.New("Web log source cannot contain symbolic links")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, nil, "", errors.New("Web log source ancestor is not a directory")
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, "", err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, "", err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, "", errors.New("Web log source must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		file.Close()
		return nil, nil, "", errors.New("cannot identify Web log source generation")
	}
	generation := strconv.FormatUint(uint64(stat.Dev), 16) + "-" + strconv.FormatUint(uint64(stat.Ino), 16)
	return file, info, generation, nil
}

func parseWebLogLine(line string, pendingSecret bool) (webParsedLog, bool) {
	if pendingSecret {
		return webParsedLog{text: "[REDACTED]"}, false
	}
	nextPending := webLogSecretOnlyPattern.MatchString(line)
	var object map[string]interface{}
	if json.Unmarshal([]byte(line), &object) == nil {
		parsed := webParsedLog{}
		rawText := webJSONText(object)
		parsed.level = webJSONString(object, "level", "severity")
		parsed.requestID = webJSONString(object, "requestId", "request_id", "request-id")
		parsed.traceID = webJSONString(object, "traceId", "trace_id", "trace-id", "trace")
		parsed.attempt = webJSONString(object, "attempt", "attemptId", "attempt_id")
		if value := webJSONString(object, "time", "timestamp", "@timestamp", "ts"); value != "" {
			parsed.time = webParseLogTime(value)
		}
		if parsed.requestID == "" {
			parsed.requestID = webPlainLogValue(rawText, "requestId", "request_id", "request-id")
		}
		if parsed.traceID == "" {
			parsed.traceID = webPlainLogValue(rawText, "traceId", "trace_id", "trace-id", "trace")
		}
		parsed.text = redactWebLogText(rawText)
		if parsed.requestID == "" {
			parsed.requestID = webPlainLogValue(parsed.text, "requestId", "request_id", "request-id")
		}
		if parsed.traceID == "" {
			parsed.traceID = webPlainLogValue(parsed.text, "traceId", "trace_id", "trace-id", "trace")
		}
		parsed.level = scrubWebLogText(parsed.level)
		parsed.requestID = scrubWebLogText(parsed.requestID)
		parsed.traceID = scrubWebLogText(parsed.traceID)
		parsed.attempt = scrubWebLogText(parsed.attempt)
		return parsed, nextPending
	}
	parsed := webParsedLog{text: redactWebLogText(line)}
	if match := webLogTimePattern.FindString(line); match != "" {
		parsed.time = webParseLogTime(match)
	}
	if match := webLogLevelPattern.FindStringSubmatch(line); len(match) > 1 {
		parsed.level = strings.ToLower(match[1])
		if parsed.level == "warning" {
			parsed.level = "warn"
		}
	}
	parsed.requestID = webPlainLogValue(line, "requestId", "request_id", "request-id")
	parsed.traceID = webPlainLogValue(line, "traceId", "trace_id", "trace-id")
	parsed.attempt = webPlainLogValue(line, "attempt", "attemptId", "attempt_id")
	return parsed, nextPending
}

func webJSONText(object map[string]interface{}) string {
	for _, key := range []string{"message", "msg", "text", "log", "content"} {
		if value, found := object[key]; found {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	data, err := json.Marshal(object)
	if err != nil {
		return ""
	}
	return string(data)
}

func webJSONString(object map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, found := object[key]
		if !found {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case json.Number:
			return typed.String()
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		}
	}
	return ""
}

func webPlainLogValue(line string, keys ...string) string {
	for _, key := range keys {
		pattern := regexp.MustCompile(`(?i)(?:^|[\s,])` + regexp.QuoteMeta(key) + `\s*[=:]\s*([^\s,;]+)`)
		match := pattern.FindStringSubmatch(line)
		if len(match) > 1 {
			return scrubWebLogText(match[1])
		}
	}
	return ""
}

func redactWebLogText(value string) string {
	value = RedactDiagnosticText(value)
	value = webLogOSCPattern.ReplaceAllString(value, "")
	value = webLogANSIPattern.ReplaceAllString(value, "")
	value = webLogURLUserPattern.ReplaceAllString(value, "$1[REDACTED]@")
	value = webLogURLSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = webLogAuthorizationPattern.ReplaceAllString(value, "$1 [REDACTED]")
	value = webLogSecretPattern.ReplaceAllString(value, "$1$2[REDACTED]")
	return scrubWebLogText(value)
}

func scrubWebLogText(value string) string {
	return strings.TrimSpace(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			if character == '\t' || character == '\n' || character == '\r' {
				return ' '
			}
			return -1
		}
		return character
	}, value))
}

func webParseLogTime(value string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

func webLogMatches(entry webParsedLog, query WebLogQuery, since *time.Time, until *time.Time) bool {
	if query.Query != "" && !strings.Contains(strings.ToLower(entry.text), strings.ToLower(query.Query)) {
		return false
	}
	if query.Level != "" && !strings.EqualFold(entry.level, query.Level) {
		return false
	}
	if query.RequestID != "" && entry.requestID != query.RequestID {
		return false
	}
	if query.TraceID != "" && entry.traceID != query.TraceID {
		return false
	}
	if since != nil && (entry.time == nil || entry.time.Before(*since)) {
		return false
	}
	if until != nil && (entry.time == nil || entry.time.After(*until)) {
		return false
	}
	return true
}

func webLogTimeBounds(sinceValue string, untilValue string) (*time.Time, *time.Time, error) {
	var since *time.Time
	var until *time.Time
	if sinceValue != "" {
		parsed, err := time.Parse(time.RFC3339Nano, sinceValue)
		if err != nil {
			return nil, nil, errors.New("invalid Web log since time")
		}
		since = &parsed
	}
	if untilValue != "" {
		parsed, err := time.Parse(time.RFC3339Nano, untilValue)
		if err != nil {
			return nil, nil, errors.New("invalid Web log until time")
		}
		until = &parsed
	}
	if since != nil && until != nil && since.After(*until) {
		return nil, nil, errors.New("Web log since time is after until time")
	}
	return since, until, nil
}

func webEqualStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func webContainsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
