package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	dashboardHistoryLines = 10000
	dashboardHistoryBytes = 64 * 1024 * 1024
	dashboardLineBytes    = 1024 * 1024
	dashboardFrameInterval = 50 * time.Millisecond
	dashboardFieldWidth    = 9
	dashboardReset         = "\x1b[0m"
	dashboardBold          = "\x1b[1m"
	dashboardDim           = "\x1b[2m"
	dashboardCyan          = "\x1b[36m"
	dashboardBoldCyan      = "\x1b[1;36m"
	dashboardGreen         = "\x1b[32m"
	dashboardYellow        = "\x1b[33m"
	dashboardRed           = "\x1b[31m"
	dashboardWhite         = "\x1b[37m"
)

type dashboardSegment struct {
	Text  string
	Style string
}

type dashboardLine struct {
	Segments []dashboardSegment
}

type dashboardService struct {
	Name      string
	Ports     map[string]int
	StartedAt time.Time
}

type dashboardInfo struct {
	Version             string
	Workspace           string
	Environment         string
	Address             string
	Interface           string
	Cluster             string
	Services            []dashboardService
	DisabledRPCBindings []string
	StartedAt           time.Time
	Color               bool
}

type TailOptions struct {
	Names               []string
	Version             string
	DisabledRPCBindings []string
}

type dashboardHistory struct {
	lines        []string
	start        int
	count        int
	bytes        int
	maximumBytes int
}

type dashboardView struct {
	Follow        bool
	Top           int
	TopOffset     int
	NewLines      int
	SearchMode    bool
	SearchDraft   string
	SearchQuery   string
	SearchMatch   int
	SearchMessage string
}

type dashboardCursor struct {
	Line   int
	Offset int
}

type dashboardLogFragment struct {
	Start int
	End   int
	Text  string
}

type dashboardVisibleLogRow struct {
	HistoryIndex int
	Offset       int
	Text         string
}

type dashboardInputKind int

const (
	dashboardInputText dashboardInputKind = iota
	dashboardInputEscape
	dashboardInputEnter
	dashboardInputBackspace
	dashboardInputInterrupt
	dashboardInputUp
	dashboardInputDown
	dashboardInputPageUp
	dashboardInputPageDown
	dashboardInputHome
	dashboardInputEnd
	dashboardInputIgnored
)

type dashboardInputEvent struct {
	Kind dashboardInputKind
	Text string
}

type localIPv4Candidate struct {
	Interface string
	Index     int
	Flags     net.Flags
	IP        net.IP
}

func DashboardAvailable(input *os.File, output io.Writer) bool {
	outputFile, outputIsFile := output.(*os.File)
	if input == nil || !outputIsFile || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(outputFile.Fd())) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	width, height, err := term.GetSize(int(outputFile.Fd()))
	return err == nil && width >= 20 && height >= 4
}

func TailLogs(ctx context.Context, workspace *WorkspaceData, session *Session, options TailOptions, input *os.File, output io.Writer) error {
	logs, err := selectLogs(session, options.Names)
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return errors.New("no logs are available for the current session")
	}
	if !DashboardAvailable(input, output) {
		return errors.New("dashboard requires an interactive terminal at least 20x4; use conven services --logs --tail for plain log streaming")
	}
	outputFile := output.(*os.File)
	width, height, err := term.GetSize(int(outputFile.Fd()))
	if err != nil || width < 20 || height < 4 {
		return errors.New("dashboard requires an interactive terminal at least 20x4; use conven services --logs --tail for plain log streaming")
	}
	address, interfaceName := discoverLocalIPv4()
	services := dashboardServices(workspace, session, options.Names)
	info := dashboardInfo{
		Version:             strings.TrimSpace(options.Version),
		Workspace:           workspace.Manifest.Workspace.Name,
		Environment:         session.Environment,
		Address:             address,
		Interface:           interfaceName,
		Cluster:             dashboardSessionCluster(session),
		Services:            services,
		DisabledRPCBindings: append([]string(nil), options.DisabledRPCBindings...),
		StartedAt:           dashboardServicesStartedAt(services),
		Color:               dashboardColorEnabled(),
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Cluster == "" {
		info.Cluster = "-"
	}
	if strings.TrimSpace(info.Workspace) == "" {
		info.Workspace = filepath.Base(workspace.Root)
	}
	if err := runDashboard(ctx, logs, info, input, outputFile, width, height); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Detached; services are still running.")
	return err
}

func runDashboard(ctx context.Context, logs []namedLog, info dashboardInfo, input *os.File, output *os.File, width int, height int) (err error) {
	inputFD := int(input.Fd())
	previousState, err := term.MakeRaw(inputFD)
	if err != nil {
		return fmt.Errorf("dashboard: enable raw terminal input: %w", err)
	}
	entered := true
	defer func() {
		var exitErr error
		if entered {
			restoreScreen := "\x1b[?1006l\x1b[?1000l\x1b[?7h\x1b[?25h\x1b[?1049l"
			if info.Color {
				restoreScreen = dashboardReset + restoreScreen
			}
			_, exitErr = io.WriteString(output, restoreScreen)
			if exitErr != nil {
				exitErr = fmt.Errorf("dashboard: restore screen: %w", exitErr)
			}
		}
		restoreErr := term.Restore(inputFD, previousState)
		if restoreErr != nil {
			restoreErr = fmt.Errorf("dashboard: restore terminal input: %w", restoreErr)
		}
		err = errors.Join(err, exitErr, restoreErr)
	}()
	if _, err := io.WriteString(output, "\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h\x1b[2J"); err != nil {
		return fmt.Errorf("dashboard: enter screen: %w", err)
	}
	dashboardContext, cancel := context.WithCancel(ctx)
	entries, logErrors := startLogStreamWithBudget(dashboardContext, logs, dashboardHistoryLines)
	keys, inputErrors, inputDone := readDashboardInput(dashboardContext, input)
	defer func() {
		cancel()
		<-inputDone
	}()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	ticker := time.NewTicker(dashboardFrameInterval)
	defer ticker.Stop()

	history := newDashboardHistory(dashboardHistoryLines)
	view := dashboardView{Follow: true, SearchMatch: -1}
	if err := writeDashboardViewFrame(output, info, width, height, history, &view); err != nil {
		return err
	}
	dirty := false
	for entries != nil || logErrors != nil {
		select {
		case entry, open := <-entries:
			if !open {
				entries = nil
				continue
			}
			evicted := history.Append(fmt.Sprintf("[%s] %s", entry.Name, entry.Line))
			view.RecordAppend(history, evicted)
			view.Clamp(history, width, dashboardLogRows(info, width, height))
			dirty = true
		case streamErr, open := <-logErrors:
			if !open {
				logErrors = nil
				continue
			}
			return streamErr
		case key, open := <-keys:
			if !open {
				keys = nil
				continue
			}
			if handleDashboardInput(key, history, &view, width, dashboardLogRows(info, width, height)) {
				return nil
			}
			dirty = true
		case inputErr, open := <-inputErrors:
			if !open {
				inputErrors = nil
				continue
			}
			return inputErr
		case <-resize:
			if nextWidth, nextHeight, sizeErr := term.GetSize(int(output.Fd())); sizeErr == nil && nextWidth > 0 && nextHeight > 0 {
				width = nextWidth
				height = nextHeight
			}
			view.Clamp(history, width, dashboardLogRows(info, width, height))
			if err := writeDashboardViewFrame(output, info, width, height, history, &view); err != nil {
				return err
			}
			dirty = false
		case <-ticker.C:
			if !dirty {
				continue
			}
			if err := writeDashboardViewFrame(output, info, width, height, history, &view); err != nil {
				return err
			}
			dirty = false
		case <-dashboardContext.Done():
			return nil
		}
	}
	if dirty {
		return writeDashboardViewFrame(output, info, width, height, history, &view)
	}
	return nil
}

func newDashboardHistory(capacity int) *dashboardHistory {
	if capacity < 1 {
		capacity = 1
	}
	return &dashboardHistory{lines: make([]string, capacity), maximumBytes: dashboardHistoryBytes}
}

func (history *dashboardHistory) Len() int {
	if history == nil {
		return 0
	}
	return history.count
}

func (history *dashboardHistory) Append(line string) int {
	line = truncateDashboardHistoryLine(sanitizeDashboardText(line))
	evicted := 0
	if history.count == len(history.lines) {
		history.dropOldest()
		evicted++
	}
	index := (history.start + history.count) % len(history.lines)
	history.lines[index] = line
	history.count++
	history.bytes += len(line)
	for history.count > 0 && history.bytes > history.maximumBytes {
		history.dropOldest()
		evicted++
	}
	return evicted
}

func (history *dashboardHistory) dropOldest() {
	if history == nil || history.count == 0 {
		return
	}
	history.bytes -= len(history.lines[history.start])
	history.lines[history.start] = ""
	history.start = (history.start + 1) % len(history.lines)
	history.count--
}

func truncateDashboardHistoryLine(line string) string {
	if len(line) <= dashboardLineBytes {
		return line
	}
	suffix := "…[truncated]"
	limit := dashboardLineBytes - len(suffix)
	line = line[:limit]
	for len(line) > 0 && !utf8.ValidString(line) {
		line = line[:len(line)-1]
	}
	return line + suffix
}

func (history *dashboardHistory) At(index int) string {
	if history == nil || index < 0 || index >= history.count {
		return ""
	}
	return history.lines[(history.start+index)%len(history.lines)]
}

func (view *dashboardView) RecordAppend(history *dashboardHistory, evicted int) {
	if evicted > 0 {
		if view.Top < evicted {
			view.Top = 0
			view.TopOffset = 0
		} else {
			view.Top -= evicted
		}
		if view.SearchMatch >= 0 {
			view.SearchMatch -= evicted
			if view.SearchMatch < 0 {
				view.SearchMatch = -1
				view.SearchMessage = "current match expired"
			}
		}
	}
	if !view.Follow {
		view.NewLines++
	}
	if view.SearchQuery != "" && strings.Contains(strings.ToLower(history.At(history.Len()-1)), strings.ToLower(view.SearchQuery)) {
		view.SearchMessage = ""
	}
}

func (view *dashboardView) Clamp(history *dashboardHistory, width int, rows int) {
	if rows < 1 {
		rows = 1
	}
	length := history.Len()
	if length == 0 {
		view.Top = 0
		view.TopOffset = 0
		view.NewLines = 0
		if view.SearchMatch >= 0 {
			view.SearchMatch = -1
		}
		return
	}
	maximumTop := dashboardFollowCursor(history, width, rows)
	if view.Follow {
		view.Top = maximumTop.Line
		view.TopOffset = maximumTop.Offset
		view.NewLines = 0
	} else {
		cursor := normalizeDashboardCursor(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width)
		if compareDashboardCursors(cursor, maximumTop) > 0 {
			cursor = maximumTop
		}
		view.Top = cursor.Line
		view.TopOffset = cursor.Offset
	}
	if view.SearchMatch >= length {
		view.SearchMatch = -1
	}
}

func (view *dashboardView) Pause(history *dashboardHistory, width int, rows int) {
	if view.Follow {
		view.Clamp(history, width, rows)
		view.Follow = false
		view.NewLines = 0
	}
}

func (view *dashboardView) Resume() {
	view.Follow = true
	view.NewLines = 0
}

func handleDashboardInput(event dashboardInputEvent, history *dashboardHistory, view *dashboardView, width int, rows int) bool {
	if event.Kind == dashboardInputInterrupt {
		return true
	}
	if view.SearchMode {
		switch event.Kind {
		case dashboardInputEscape:
			view.SearchMode = false
			view.SearchDraft = ""
			view.SearchMessage = ""
		case dashboardInputEnter:
			view.SearchMode = false
			view.SearchQuery = strings.TrimSpace(view.SearchDraft)
			view.SearchDraft = ""
			view.SearchMatch = -1
			view.SearchMessage = ""
			if view.SearchQuery != "" {
				selectDashboardMatch(history, view, width, rows, view.Top-1, 1)
			}
		case dashboardInputBackspace:
			view.SearchDraft = removeDashboardLastRune(view.SearchDraft)
		case dashboardInputText:
			view.SearchDraft += event.Text
		}
		return false
	}

	switch event.Kind {
	case dashboardInputEscape:
		view.SearchQuery = ""
		view.SearchMatch = -1
		view.SearchMessage = ""
	case dashboardInputUp:
		view.Pause(history, width, rows)
		cursor := moveDashboardCursor(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, -1)
		view.Top = cursor.Line
		view.TopOffset = cursor.Offset
		view.Clamp(history, width, rows)
	case dashboardInputDown:
		view.Pause(history, width, rows)
		cursor := moveDashboardCursor(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, 1)
		maximumTop := dashboardFollowCursor(history, width, rows)
		if compareDashboardCursors(cursor, maximumTop) >= 0 {
			view.Resume()
		} else {
			view.Top = cursor.Line
			view.TopOffset = cursor.Offset
		}
		view.Clamp(history, width, rows)
	case dashboardInputPageUp:
		view.Pause(history, width, rows)
		cursor := moveDashboardCursor(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, -rows)
		view.Top = cursor.Line
		view.TopOffset = cursor.Offset
		view.Clamp(history, width, rows)
	case dashboardInputPageDown:
		view.Pause(history, width, rows)
		cursor := moveDashboardCursor(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, rows)
		maximumTop := dashboardFollowCursor(history, width, rows)
		if compareDashboardCursors(cursor, maximumTop) >= 0 {
			view.Resume()
		} else {
			view.Top = cursor.Line
			view.TopOffset = cursor.Offset
		}
		view.Clamp(history, width, rows)
	case dashboardInputHome:
		view.Pause(history, width, rows)
		view.Top = 0
		view.TopOffset = 0
		view.Clamp(history, width, rows)
	case dashboardInputEnd:
		view.Resume()
		view.Clamp(history, width, rows)
	case dashboardInputText:
		switch event.Text {
		case "q", "Q":
			return true
		case "/":
			view.SearchMode = true
			view.SearchDraft = ""
			view.SearchMessage = ""
		case "g":
			view.Pause(history, width, rows)
			view.Top = 0
			view.TopOffset = 0
			view.Clamp(history, width, rows)
		case "G":
			view.Resume()
			view.Clamp(history, width, rows)
		case "n":
			if view.SearchQuery != "" {
				selectDashboardMatch(history, view, width, rows, view.SearchMatch, 1)
			}
		case "N":
			if view.SearchQuery != "" {
				start := view.SearchMatch
				if start < 0 {
					visible := dashboardVisibleLogRows(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, rows)
					if len(visible) > 0 {
						start = visible[len(visible)-1].HistoryIndex + 1
					} else {
						start = view.Top + 1
					}
				}
				selectDashboardMatch(history, view, width, rows, start, -1)
			}
		}
	}
	return false
}

func removeDashboardLastRune(value string) string {
	if value == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(value)
	if size <= 0 {
		return ""
	}
	return value[:len(value)-size]
}

func selectDashboardMatch(history *dashboardHistory, view *dashboardView, width int, rows int, start int, direction int) {
	match := findDashboardMatch(history, view.SearchQuery, start, direction)
	if match < 0 {
		view.SearchMatch = -1
		view.SearchMessage = "no matches"
		return
	}
	view.SearchMatch = match
	view.SearchMessage = ""
	view.Follow = false
	view.NewLines = 0
	cursor := normalizeDashboardCursor(history, dashboardCursor{Line: match, Offset: dashboardSearchOffset(history.At(match), view.SearchQuery)}, width)
	cursor = moveDashboardCursor(history, cursor, width, -(rows / 2))
	view.Top = cursor.Line
	view.TopOffset = cursor.Offset
	view.Clamp(history, width, rows)
}

func findDashboardMatch(history *dashboardHistory, query string, start int, direction int) int {
	length := history.Len()
	query = strings.ToLower(strings.TrimSpace(query))
	if length == 0 || query == "" {
		return -1
	}
	if direction < 0 {
		direction = -1
	} else {
		direction = 1
	}
	index := start
	for checked := 0; checked < length; checked++ {
		index = (index + direction) % length
		if index < 0 {
			index += length
		}
		if strings.Contains(strings.ToLower(history.At(index)), query) {
			return index
		}
	}
	return -1
}

func dashboardSearchOffset(value string, query string) int {
	foldedValue := strings.ToLower(value)
	foldedQuery := strings.ToLower(strings.TrimSpace(query))
	index := strings.Index(foldedValue, foldedQuery)
	if index <= 0 {
		return 0
	}
	runeIndex := utf8.RuneCountInString(foldedValue[:index])
	current := 0
	for offset := range value {
		if current == runeIndex {
			return offset
		}
		current++
	}
	return len(value)
}

func dashboardLogFragments(value string, width int) []dashboardLogFragment {
	value = sanitizeDashboardText(value)
	if width < 1 {
		width = 1
	}
	fragments := make([]dashboardLogFragment, 0, dashboardDisplayWidth(value)/width+1)
	start := 0
	used := 0
	for offset, character := range value {
		characterWidth := dashboardRuneWidth(character)
		if used > 0 && used+characterWidth > width {
			fragments = append(fragments, dashboardLogFragment{Start: start, End: offset, Text: value[start:offset]})
			start = offset
			used = 0
		}
		used += characterWidth
	}
	fragments = append(fragments, dashboardLogFragment{Start: start, End: len(value), Text: value[start:]})
	return fragments
}

func dashboardFragmentIndex(fragments []dashboardLogFragment, offset int) int {
	if len(fragments) == 0 || offset <= 0 {
		return 0
	}
	for index, fragment := range fragments {
		if offset < fragment.End {
			return index
		}
	}
	return len(fragments) - 1
}

func normalizeDashboardCursor(history *dashboardHistory, cursor dashboardCursor, width int) dashboardCursor {
	length := history.Len()
	if length == 0 {
		return dashboardCursor{}
	}
	if cursor.Line < 0 {
		cursor.Line = 0
	}
	if cursor.Line >= length {
		cursor.Line = length - 1
	}
	fragments := dashboardLogFragments(history.At(cursor.Line), width)
	fragment := fragments[dashboardFragmentIndex(fragments, cursor.Offset)]
	cursor.Offset = fragment.Start
	return cursor
}

func compareDashboardCursors(left dashboardCursor, right dashboardCursor) int {
	if left.Line < right.Line {
		return -1
	}
	if left.Line > right.Line {
		return 1
	}
	if left.Offset < right.Offset {
		return -1
	}
	if left.Offset > right.Offset {
		return 1
	}
	return 0
}

func dashboardFollowCursor(history *dashboardHistory, width int, rows int) dashboardCursor {
	if history.Len() == 0 {
		return dashboardCursor{}
	}
	if rows < 1 {
		rows = 1
	}
	remaining := rows
	for line := history.Len() - 1; line >= 0; line-- {
		fragments := dashboardLogFragments(history.At(line), width)
		if len(fragments) >= remaining {
			fragment := fragments[len(fragments)-remaining]
			return dashboardCursor{Line: line, Offset: fragment.Start}
		}
		remaining -= len(fragments)
	}
	return dashboardCursor{}
}

func moveDashboardCursor(history *dashboardHistory, cursor dashboardCursor, width int, distance int) dashboardCursor {
	if history.Len() == 0 || distance == 0 {
		return normalizeDashboardCursor(history, cursor, width)
	}
	cursor = normalizeDashboardCursor(history, cursor, width)
	line := cursor.Line
	fragments := dashboardLogFragments(history.At(line), width)
	fragmentIndex := dashboardFragmentIndex(fragments, cursor.Offset)
	if distance < 0 {
		remaining := -distance
		for {
			if remaining <= fragmentIndex {
				return dashboardCursor{Line: line, Offset: fragments[fragmentIndex-remaining].Start}
			}
			remaining -= fragmentIndex + 1
			if line == 0 {
				return dashboardCursor{}
			}
			line--
			fragments = dashboardLogFragments(history.At(line), width)
			fragmentIndex = len(fragments) - 1
		}
	}
	remaining := distance
	for {
		available := len(fragments) - fragmentIndex - 1
		if remaining <= available {
			return dashboardCursor{Line: line, Offset: fragments[fragmentIndex+remaining].Start}
		}
		remaining -= available + 1
		if line == history.Len()-1 {
			return dashboardCursor{Line: line, Offset: fragments[len(fragments)-1].Start}
		}
		line++
		fragments = dashboardLogFragments(history.At(line), width)
		fragmentIndex = 0
	}
}

func dashboardVisibleLogRows(history *dashboardHistory, cursor dashboardCursor, width int, rows int) []dashboardVisibleLogRow {
	if history.Len() == 0 || rows <= 0 {
		return nil
	}
	cursor = normalizeDashboardCursor(history, cursor, width)
	visible := make([]dashboardVisibleLogRow, 0, rows)
	for line := cursor.Line; line < history.Len() && len(visible) < rows; line++ {
		fragments := dashboardLogFragments(history.At(line), width)
		first := 0
		if line == cursor.Line {
			first = dashboardFragmentIndex(fragments, cursor.Offset)
		}
		for index := first; index < len(fragments) && len(visible) < rows; index++ {
			visible = append(visible, dashboardVisibleLogRow{
				HistoryIndex: line,
				Offset:       fragments[index].Start,
				Text:         fragments[index].Text,
			})
		}
	}
	return visible
}

func readDashboardInput(ctx context.Context, input *os.File) (<-chan dashboardInputEvent, <-chan error, <-chan struct{}) {
	keys := make(chan dashboardInputEvent, 16)
	errorsChannel := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(keys)
		defer close(errorsChannel)
		defer close(done)
		descriptors := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		buffer := make([]byte, 32)
		pending := make([]byte, 0, 32)
		emit := func(event dashboardInputEvent) bool {
			if event.Kind == dashboardInputIgnored {
				return true
			}
			select {
			case keys <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		drain := func() bool {
			for len(pending) > 0 {
				event, consumed, incomplete := parseDashboardInputEvent(pending)
				if incomplete {
					return true
				}
				pending = pending[consumed:]
				if !emit(event) {
					return false
				}
			}
			return true
		}
		for {
			if ctx.Err() != nil {
				return
			}
			timeout := 100
			if len(pending) > 0 {
				timeout = 25
			}
			ready, err := unix.Poll(descriptors, timeout)
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				errorsChannel <- fmt.Errorf("dashboard: poll terminal input: %w", err)
				return
			}
			if ready == 0 {
				if len(pending) > 0 && pending[0] == 0x1b {
					pending = pending[1:]
					if !emit(dashboardInputEvent{Kind: dashboardInputEscape}) || !drain() {
						return
					}
				}
				continue
			}
			if ctx.Err() != nil {
				return
			}
			count, err := input.Read(buffer)
			if err != nil {
				if ctx.Err() == nil {
					errorsChannel <- fmt.Errorf("dashboard: read terminal input: %w", err)
				}
				return
			}
			pending = append(pending, buffer[:count]...)
			if !drain() {
				return
			}
		}
	}()
	return keys, errorsChannel, done
}

func parseDashboardInputEvent(data []byte) (dashboardInputEvent, int, bool) {
	if len(data) == 0 {
		return dashboardInputEvent{}, 0, true
	}
	switch data[0] {
	case 0x03:
		return dashboardInputEvent{Kind: dashboardInputInterrupt}, 1, false
	case 0x08, 0x7f:
		return dashboardInputEvent{Kind: dashboardInputBackspace}, 1, false
	case '\r', '\n':
		return dashboardInputEvent{Kind: dashboardInputEnter}, 1, false
	case 0x1b:
		if len(data) == 1 {
			return dashboardInputEvent{}, 0, true
		}
		if data[1] == '[' {
			end := -1
			for index := 2; index < len(data); index++ {
				if data[index] >= 0x40 && data[index] <= 0x7e {
					end = index
					break
				}
			}
			if end < 0 {
				return dashboardInputEvent{}, 0, true
			}
			sequence := string(data[:end+1])
			if strings.HasPrefix(sequence, "\x1b[<") && (sequence[len(sequence)-1] == 'M' || sequence[len(sequence)-1] == 'm') {
				fields := strings.Split(sequence[3:len(sequence)-1], ";")
				if len(fields) == 3 {
					button, err := strconv.Atoi(fields[0])
					if err == nil && button&64 != 0 {
						if button&1 == 0 {
							return dashboardInputEvent{Kind: dashboardInputUp}, end + 1, false
						}
						return dashboardInputEvent{Kind: dashboardInputDown}, end + 1, false
					}
				}
				return dashboardInputEvent{Kind: dashboardInputIgnored}, end + 1, false
			}
			switch sequence {
			case "\x1b[A":
				return dashboardInputEvent{Kind: dashboardInputUp}, end + 1, false
			case "\x1b[B":
				return dashboardInputEvent{Kind: dashboardInputDown}, end + 1, false
			case "\x1b[5~":
				return dashboardInputEvent{Kind: dashboardInputPageUp}, end + 1, false
			case "\x1b[6~":
				return dashboardInputEvent{Kind: dashboardInputPageDown}, end + 1, false
			case "\x1b[H", "\x1b[1~", "\x1b[7~":
				return dashboardInputEvent{Kind: dashboardInputHome}, end + 1, false
			case "\x1b[F", "\x1b[4~", "\x1b[8~":
				return dashboardInputEvent{Kind: dashboardInputEnd}, end + 1, false
			default:
				return dashboardInputEvent{Kind: dashboardInputIgnored}, end + 1, false
			}
		}
		if data[1] == 'O' {
			if len(data) < 3 {
				return dashboardInputEvent{}, 0, true
			}
			switch data[2] {
			case 'H':
				return dashboardInputEvent{Kind: dashboardInputHome}, 3, false
			case 'F':
				return dashboardInputEvent{Kind: dashboardInputEnd}, 3, false
			default:
				return dashboardInputEvent{Kind: dashboardInputIgnored}, 3, false
			}
		}
		return dashboardInputEvent{Kind: dashboardInputEscape}, 1, false
	}
	if data[0] < 0x20 {
		return dashboardInputEvent{Kind: dashboardInputIgnored}, 1, false
	}
	if !utf8.FullRune(data) {
		return dashboardInputEvent{}, 0, true
	}
	character, size := utf8.DecodeRune(data)
	if character == utf8.RuneError && size == 1 || !unicode.IsPrint(character) {
		return dashboardInputEvent{Kind: dashboardInputIgnored}, size, false
	}
	return dashboardInputEvent{Kind: dashboardInputText, Text: string(character)}, size, false
}

func dashboardLogRows(info dashboardInfo, width int, height int) int {
	rows := height - len(dashboardBanner(info, width, height))
	if rows < 1 {
		return 1
	}
	return rows
}

func writeDashboardViewFrame(output io.Writer, info dashboardInfo, width int, height int, history *dashboardHistory, view *dashboardView) error {
	frame := renderDashboardViewFrame(info, width, height, history, view)
	if _, err := io.WriteString(output, frame); err != nil {
		return fmt.Errorf("dashboard: render: %w", err)
	}
	return nil
}

func renderDashboardViewFrame(info dashboardInfo, width int, height int, history *dashboardHistory, view *dashboardView) string {
	if width < 1 {
		width = 1
	}
	if height < 2 {
		height = 2
	}
	logRows := dashboardLogRows(info, width, height)
	view.Clamp(history, width, logRows)
	hint := dashboardViewHint(view, history, logRows, width)
	banner := dashboardBannerWithHint(info, width, height, hint)
	visible := dashboardVisibleLogRows(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, logRows)
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for row := 0; row < height; row++ {
		frame.WriteString("\x1b[2K")
		if row < len(banner) {
			frame.WriteString(renderDashboardLine(banner[row], width, info.Color))
		} else if index := row - len(banner); index < len(visible) {
			frame.WriteString(renderDashboardLogLine(visible[index].Text, info.Color))
		}
		if row+1 < height {
			frame.WriteString("\r\n")
		}
	}
	return frame.String()
}

func dashboardViewHint(view *dashboardView, history *dashboardHistory, rows int, width int) string {
	if view.SearchMode {
		if width < 50 {
			return "/" + view.SearchDraft + " · Enter · Esc cancel"
		}
		return "/" + view.SearchDraft + " · Enter: search · Esc: cancel · Ctrl-C: detach"
	}
	if view.SearchQuery != "" {
		result := view.SearchMessage
		if result == "" && view.SearchMatch >= 0 {
			result = fmt.Sprintf("line %d/%d", view.SearchMatch+1, history.Len())
		}
		if result == "" {
			result = "search ready"
		}
		if width < 55 {
			return fmt.Sprintf("/%s · %s · n/N · Esc", view.SearchQuery, result)
		}
		return fmt.Sprintf("q: detach · /%s · %s · n/N: next/prev · Esc: clear", view.SearchQuery, result)
	}
	if view.Follow {
		if width < 50 {
			return "q: detach · FOLLOW · ↑/↓ scroll"
		}
		return "q / Ctrl-C: detach · FOLLOW · ↑/↓ scroll · / search"
	}
	start := 0
	end := 0
	visible := dashboardVisibleLogRows(history, dashboardCursor{Line: view.Top, Offset: view.TopOffset}, width, rows)
	if len(visible) > 0 {
		start = visible[0].HistoryIndex + 1
		end = visible[len(visible)-1].HistoryIndex + 1
	}
	position := fmt.Sprintf("%d-%d/%d", start, end, history.Len())
	newLines := ""
	if view.NewLines > 0 {
		newLines = fmt.Sprintf(" · %d new", view.NewLines)
	}
	if width < 60 {
		return fmt.Sprintf("q: detach · PAUSED · %s%s · End follow", position, newLines)
	}
	return fmt.Sprintf("q / Ctrl-C: detach · PAUSED · %s%s · End/G: follow · / search", position, newLines)
}

func dashboardBanner(info dashboardInfo, width int, height int) []dashboardLine {
	return dashboardBannerWithHint(info, width, height, dashboardLogHint(width))
}

func dashboardBannerWithHint(info dashboardInfo, width int, height int, hint string) []dashboardLine {
	minimumLogRows := 1
	if height >= 7 {
		minimumLogRows = 3
	}
	maximumBannerRows := height - minimumLogRows
	if maximumBannerRows < 1 {
		maximumBannerRows = 1
	}
	detachLine := dashboardDividerLine(width, hint)
	if maximumBannerRows == 1 {
		return []dashboardLine{detachLine}
	}

	workspaceLines := dashboardWorkspaceLines(info, width)
	showTitleRule := true
	showCluster := true
	showDisabled := true
	showServices := true
	fixedRows := func() int {
		rows := 2 + len(workspaceLines)
		if showTitleRule {
			rows++
		}
		if showCluster {
			rows++
		}
		if showDisabled {
			rows++
		}
		if showServices {
			rows++
		}
		return rows
	}
	if fixedRows() > maximumBannerRows {
		showServices = false
	}
	if fixedRows() > maximumBannerRows {
		showCluster = false
	}
	if fixedRows() > maximumBannerRows {
		showDisabled = false
	}
	if fixedRows() > maximumBannerRows && len(workspaceLines) > 1 {
		workspaceLines = []dashboardLine{dashboardCompactWorkspaceLine(info)}
	}
	if fixedRows() > maximumBannerRows {
		workspaceLines = nil
	}
	if fixedRows() > maximumBannerRows {
		showTitleRule = false
	}

	lines := []dashboardLine{dashboardTitleLine(info, width)}
	if showTitleRule {
		lines = append(lines, dashboardHorizontalRuleLine(width))
	}
	lines = append(lines, workspaceLines...)
	if showCluster {
		lines = append(lines, dashboardFieldLine("CLUSTER", info.Cluster, dashboardGreen))
	}
	if showDisabled {
		lines = append(lines, dashboardDisabledLine(info.DisabledRPCBindings))
	}
	if showServices {
		lines = append(lines, dashboardServicesTitleLine(len(info.Services)))
		serviceRows := maximumBannerRows - len(lines) - 1
		lines = append(lines, dashboardServiceLines(info.Services, width, serviceRows)...)
	}
	lines = append(lines, detachLine)
	return lines
}

func dashboardBannerLines(info dashboardInfo, width int, height int) []string {
	banner := dashboardBanner(info, width, height)
	lines := make([]string, 0, len(banner))
	for _, line := range banner {
		lines = append(lines, dashboardPlainLine(line))
	}
	return lines
}

func dashboardTitleLine(info dashboardInfo, width int) dashboardLine {
	segments := []dashboardSegment{
		{Text: "  "},
		{Text: "CONVEN", Style: dashboardBoldCyan},
		{Text: "  "},
		{Text: info.Version, Style: dashboardGreen},
	}
	if info.StartedAt.IsZero() {
		return dashboardLine{Segments: segments}
	}
	started := info.StartedAt.Format("2006-01-02 15:04:05")
	leftWidth := dashboardDisplayWidth("  CONVEN  " + info.Version)
	rightWidth := dashboardDisplayWidth("STARTED  " + started)
	padding := width - leftWidth - rightWidth
	if padding < 2 {
		return dashboardLine{Segments: segments}
	}
	segments = append(segments,
		dashboardSegment{Text: strings.Repeat(" ", padding)},
		dashboardSegment{Text: "STARTED", Style: dashboardWhite},
		dashboardSegment{Text: "  "},
		dashboardSegment{Text: started, Style: dashboardYellow},
	)
	return dashboardLine{Segments: segments}
}

func dashboardHorizontalRuleLine(width int) dashboardLine {
	ruleWidth := width
	if ruleWidth < 1 {
		ruleWidth = 1
	}
	return dashboardLine{Segments: []dashboardSegment{
		{Text: strings.Repeat("─", ruleWidth), Style: dashboardWhite},
	}}
}

func dashboardWorkspaceLines(info dashboardInfo, width int) []dashboardLine {
	interfaceLabel := ""
	if info.Interface != "" {
		interfaceLabel = " (" + info.Interface + ")"
	}
	workspace := append(dashboardFieldPrefix("WORKSPACE"), dashboardSegment{Text: info.Workspace, Style: dashboardBold})
	environment := []dashboardSegment{
		{Text: "     "},
		{Text: "ENV", Style: dashboardWhite},
		{Text: "  "},
		{Text: info.Environment, Style: dashboardYellow},
	}
	network := []dashboardSegment{
		{Text: "     "},
		{Text: "LAN", Style: dashboardWhite},
		{Text: "  "},
		{Text: info.Address + interfaceLabel, Style: dashboardGreen},
	}
	if width >= 78 {
		segments := append(workspace, environment...)
		segments = append(segments, network...)
		return []dashboardLine{{Segments: segments}}
	}
	if width >= 50 {
		first := append(workspace, environment...)
		return []dashboardLine{{Segments: first}, dashboardFieldLine("LAN", info.Address+interfaceLabel, dashboardGreen)}
	}
	return []dashboardLine{
		dashboardFieldLine("WORKSPACE", info.Workspace, dashboardBold),
		dashboardFieldLine("ENV", info.Environment, dashboardYellow),
		dashboardFieldLine("LAN", info.Address+interfaceLabel, dashboardGreen),
	}
}

func dashboardCompactWorkspaceLine(info dashboardInfo) dashboardLine {
	interfaceLabel := ""
	if info.Interface != "" {
		interfaceLabel = " (" + info.Interface + ")"
	}
	segments := append(dashboardFieldPrefix("WORKSPACE"), dashboardSegment{Text: info.Workspace, Style: dashboardBold})
	segments = append(segments,
		dashboardSegment{Text: "     "},
		dashboardSegment{Text: "ENV", Style: dashboardWhite},
		dashboardSegment{Text: "  "},
		dashboardSegment{Text: info.Environment, Style: dashboardYellow},
		dashboardSegment{Text: "     "},
		dashboardSegment{Text: "LAN", Style: dashboardWhite},
		dashboardSegment{Text: "  "},
		dashboardSegment{Text: info.Address + interfaceLabel, Style: dashboardGreen},
	)
	return dashboardLine{Segments: segments}
}

func dashboardFieldPrefix(label string) []dashboardSegment {
	padding := dashboardFieldWidth - dashboardDisplayWidth(label) + 2
	if padding < 2 {
		padding = 2
	}
	return []dashboardSegment{
		{Text: "  "},
		{Text: label, Style: dashboardWhite},
		{Text: strings.Repeat(" ", padding)},
	}
}

func dashboardFieldLine(label string, value string, valueStyle string) dashboardLine {
	segments := dashboardFieldPrefix(label)
	segments = append(segments, dashboardSegment{Text: value, Style: valueStyle})
	return dashboardLine{Segments: segments}
}

func dashboardServicesTitleLine(count int) dashboardLine {
	segments := dashboardFieldPrefix("SERVICES")
	segments = append(segments,
		dashboardSegment{Text: fmt.Sprintf("%d", count), Style: dashboardGreen},
		dashboardSegment{Text: " local", Style: dashboardDim},
	)
	return dashboardLine{Segments: segments}
}

func dashboardDisabledLine(bindings []string) dashboardLine {
	if len(bindings) == 0 {
		return dashboardFieldLine("DISABLED", "none", dashboardDim)
	}
	return dashboardFieldLine("DISABLED", strings.Join(bindings, ", "), dashboardYellow)
}

func dashboardServiceLines(services []dashboardService, width int, rows int) []dashboardLine {
	if rows <= 0 || len(services) == 0 {
		return nil
	}
	shown := len(services)
	includeMore := false
	if shown > rows {
		includeMore = true
		shown = rows - 1
		if shown < 0 {
			shown = 0
		}
	}
	nameWidth := dashboardServiceNameWidth(width)
	lines := make([]dashboardLine, 0, rows)
	for index := 0; index < shown; index++ {
		lines = append(lines, dashboardServiceLine(services[index], nameWidth))
	}
	if includeMore {
		hidden := len(services) - shown
		message := fmt.Sprintf("…  +%d more services", hidden)
		if shown == 0 {
			message = fmt.Sprintf("…  %d services hidden at this terminal height", len(services))
		}
		lines = append(lines, dashboardLine{Segments: []dashboardSegment{
			{Text: "  "},
			{Text: message, Style: dashboardDim},
		}})
	}
	return lines
}

func dashboardServiceNameWidth(width int) int {
	if width >= 78 {
		return 32
	}
	available := width - 15
	if available > 32 {
		available = 32
	}
	if available < 8 {
		available = 8
	}
	return available
}

func dashboardServiceLine(service dashboardService, nameWidth int) dashboardLine {
	name := fitDashboardLine(service.Name, nameWidth, false)
	segments := []dashboardSegment{
		{Text: "    "},
		{Text: name, Style: dashboardBoldCyan},
		{Text: strings.Repeat(" ", nameWidth-dashboardDisplayWidth(name)) + "  "},
	}
	segments = append(segments, dashboardPortSegments(service.Ports)...)
	return dashboardLine{Segments: segments}
}

func dashboardPortSegments(ports map[string]int) []dashboardSegment {
	if len(ports) == 0 {
		return []dashboardSegment{{Text: "-", Style: dashboardDim}}
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	segments := make([]dashboardSegment, 0, len(names)*4)
	for index, name := range names {
		if index > 0 {
			segments = append(segments, dashboardSegment{Text: "  ·  ", Style: dashboardDim})
		}
		protocol := strings.ToUpper(name)
		protocolPadding := 4 - dashboardDisplayWidth(protocol) + 2
		if protocolPadding < 2 {
			protocolPadding = 2
		}
		segments = append(segments,
			dashboardSegment{Text: protocol, Style: dashboardWhite},
			dashboardSegment{Text: strings.Repeat(" ", protocolPadding)},
			dashboardSegment{Text: fmt.Sprintf("%d", ports[name]), Style: dashboardGreen},
		)
	}
	return segments
}

func dashboardDetachLine(width int) dashboardLine {
	return dashboardDividerLine(width, dashboardLogHint(width))
}

func dashboardDividerLine(width int, hint string) dashboardLine {
	hintWidth := dashboardDisplayWidth(hint)
	ruleWidth := width - hintWidth - 2
	if ruleWidth < 0 {
		ruleWidth = 0
	}
	leftRuleWidth := ruleWidth / 2
	rightRuleWidth := ruleWidth - leftRuleWidth
	return dashboardLine{Segments: []dashboardSegment{
		{Text: strings.Repeat("─", leftRuleWidth), Style: dashboardWhite},
		{Text: " "},
		{Text: hint, Style: dashboardYellow},
		{Text: " "},
		{Text: strings.Repeat("─", rightRuleWidth), Style: dashboardWhite},
	}}
}

func dashboardLogHint(width int) string {
	if width < 42 {
		return "q: detach"
	}
	if width < 70 {
		return "q / Ctrl-C: detach"
	}
	return "q / Ctrl-C: detach · services keep running"
}

func dashboardPlainLine(line dashboardLine) string {
	var value strings.Builder
	for _, segment := range line.Segments {
		value.WriteString(sanitizeDashboardText(segment.Text))
	}
	return value.String()
}

func renderDashboardLine(line dashboardLine, width int, color bool) string {
	if width < 1 {
		return ""
	}
	plain := dashboardPlainLine(line)
	truncated := dashboardDisplayWidth(plain) > width
	limit := width
	if truncated {
		limit -= dashboardRuneWidth('…')
		if limit < 0 {
			limit = 0
		}
	}
	used := 0
	var rendered strings.Builder
	for _, segment := range line.Segments {
		text := sanitizeDashboardText(segment.Text)
		var visible strings.Builder
		for _, character := range text {
			characterWidth := dashboardRuneWidth(character)
			if used+characterWidth > limit {
				break
			}
			visible.WriteRune(character)
			used += characterWidth
		}
		if visible.Len() == 0 {
			if used >= limit {
				break
			}
			continue
		}
		if color && segment.Style != "" {
			rendered.WriteString(segment.Style)
		}
		rendered.WriteString(visible.String())
		if color && segment.Style != "" {
			rendered.WriteString(dashboardReset)
		}
		if used >= limit {
			break
		}
	}
	if truncated {
		rendered.WriteRune('…')
	}
	return rendered.String()
}

func renderDashboardLogLine(value string, color bool) string {
	line := sanitizeDashboardText(value)
	if !color || line == "" {
		return line
	}
	prefixEnd := 0
	if strings.HasPrefix(line, "[") {
		if end := strings.IndexByte(line, ']'); end >= 0 {
			prefixEnd = end + 1
		}
	}
	remainder := line[prefixEnd:]
	severityStart, severityEnd, severityStyle := dashboardLogSeverity(remainder)
	var rendered strings.Builder
	if prefixEnd > 0 {
		rendered.WriteString(dashboardCyan)
		rendered.WriteString(line[:prefixEnd])
		rendered.WriteString(dashboardReset)
	}
	if severityStart < 0 {
		rendered.WriteString(remainder)
		return rendered.String()
	}
	rendered.WriteString(remainder[:severityStart])
	rendered.WriteString(severityStyle)
	rendered.WriteString(remainder[severityStart:severityEnd])
	rendered.WriteString(dashboardReset)
	rendered.WriteString(remainder[severityEnd:])
	return rendered.String()
}

func dashboardLogSeverity(value string) (int, int, string) {
	upper := strings.ToUpper(value)
	for _, token := range []string{"ERROR", "FATAL", "PANIC"} {
		if index := strings.Index(upper, token); index >= 0 {
			return index, index + len(token), dashboardRed
		}
	}
	for _, token := range []string{"WARN", "SLOW"} {
		if index := strings.Index(upper, token); index >= 0 {
			return index, index + len(token), dashboardYellow
		}
	}
	return -1, -1, ""
}

func dashboardColorEnabled() bool {
	_, disabled := os.LookupEnv("NO_COLOR")
	return !disabled
}

func kubeconfigClusterName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func dashboardSessionCluster(session *Session) string {
	if session == nil {
		return ""
	}
	if cluster := strings.TrimSpace(session.Cluster); cluster != "" {
		return cluster
	}
	if session.Connection == nil || session.Connection.Driver != "ktctl" {
		return ""
	}
	command := session.Connection.Command
	for index := 0; index < len(command); index++ {
		argument := command[index]
		if argument == "connect" {
			break
		}
		if argument == "--kubeconfig" && index+1 < len(command) {
			return kubeconfigClusterName(command[index+1])
		}
		if strings.HasPrefix(argument, "--kubeconfig=") {
			return kubeconfigClusterName(strings.TrimPrefix(argument, "--kubeconfig="))
		}
	}
	return ""
}

func fitDashboardLine(value string, width int, pad bool) string {
	value = sanitizeDashboardText(value)
	displayWidth := dashboardDisplayWidth(value)
	if displayWidth > width {
		ellipsisWidth := dashboardRuneWidth('…')
		if width <= ellipsisWidth {
			return "…"
		}
		limit := width - ellipsisWidth
		used := 0
		var truncated strings.Builder
		for _, character := range value {
			characterWidth := dashboardRuneWidth(character)
			if used+characterWidth > limit {
				break
			}
			truncated.WriteRune(character)
			used += characterWidth
		}
		truncated.WriteRune('…')
		return truncated.String()
	}
	if pad && displayWidth < width {
		return value + strings.Repeat(" ", width-displayWidth)
	}
	return value
}

func dashboardDisplayWidth(value string) int {
	width := 0
	for _, character := range value {
		width += dashboardRuneWidth(character)
	}
	return width
}

func dashboardRuneWidth(character rune) int {
	if unicode.In(character, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	if character >= 0x1100 && (character <= 0x115f ||
		character == 0x2329 || character == 0x232a ||
		character >= 0x2e80 && character <= 0xa4cf && character != 0x303f ||
		character >= 0xac00 && character <= 0xd7a3 ||
		character >= 0xf900 && character <= 0xfaff ||
		character >= 0xfe10 && character <= 0xfe19 ||
		character >= 0xfe30 && character <= 0xfe6f ||
		character >= 0xff00 && character <= 0xff60 ||
		character >= 0xffe0 && character <= 0xffe6 ||
		character >= 0x1f300 && character <= 0x1faff ||
		character >= 0x20000 && character <= 0x3fffd) {
		return 2
	}
	return 1
}

func sanitizeDashboardText(value string) string {
	var result strings.Builder
	escape := uint8(0)
	for _, character := range value {
		switch escape {
		case 1:
			switch character {
			case '[':
				escape = 2
			case ']':
				escape = 3
			default:
				escape = 0
			}
			continue
		case 2:
			if character >= 0x40 && character <= 0x7e {
				escape = 0
			}
			continue
		case 3:
			if character == '\a' {
				escape = 0
			} else if character == 0x1b {
				escape = 4
			}
			continue
		case 4:
			if character == '\\' {
				escape = 0
			} else {
				escape = 3
			}
			continue
		}
		if character == 0x1b {
			escape = 1
			continue
		}
		if character == '\t' {
			result.WriteString("    ")
			continue
		}
		if character == '\r' || character == '\n' || unicode.IsControl(character) {
			continue
		}
		result.WriteRune(character)
	}
	return result.String()
}

func dashboardServices(workspace *WorkspaceData, session *Session, names []string) []dashboardService {
	processes := make(map[string]ServiceProcess, len(session.Services))
	for _, process := range session.Services {
		processes[process.Name] = process
	}
	order := append([]string(nil), names...)
	if len(order) == 0 {
		order = append(order, session.Selected...)
	}
	if len(order) == 0 {
		for _, process := range session.Services {
			order = append(order, process.Name)
		}
	}
	seen := make(map[string]bool, len(order))
	services := make([]dashboardService, 0, len(processes))
	for _, name := range order {
		process, found := processes[name]
		if !found || seen[name] {
			continue
		}
		seen[name] = true
		ports := process.Ports
		if ports == nil {
			ports = workspace.Manifest.Services[name].Ports
		}
		services = append(services, dashboardService{Name: name, Ports: ports, StartedAt: process.StartedAt})
	}
	if len(names) == 0 {
		for _, process := range session.Services {
			if seen[process.Name] {
				continue
			}
			ports := process.Ports
			if ports == nil {
				ports = workspace.Manifest.Services[process.Name].Ports
			}
			services = append(services, dashboardService{Name: process.Name, Ports: ports, StartedAt: process.StartedAt})
		}
	}
	return services
}

func dashboardServicesStartedAt(services []dashboardService) time.Time {
	var started time.Time
	for _, service := range services {
		if service.StartedAt.IsZero() {
			continue
		}
		if started.IsZero() || service.StartedAt.Before(started) {
			started = service.StartedAt
		}
	}
	return started
}

func discoverLocalIPv4() (string, string) {
	primary := defaultRouteIPv4()
	candidates := interfaceIPv4Candidates()
	selected := chooseLocalIPv4ForOS(goruntime.GOOS, primary, candidates)
	if selected.IP == nil {
		return "unavailable", ""
	}
	return selected.IP.String(), selected.Interface
}

func defaultRouteIPv4() net.IP {
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9})
	if err != nil {
		return nil
	}
	defer connection.Close()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	return address.IP.To4()
}

func interfaceIPv4Candidates() []localIPv4Candidate {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	candidates := make([]localIPv4Candidate, 0)
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			ip = ip.To4()
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			candidates = append(candidates, localIPv4Candidate{
				Interface: networkInterface.Name,
				Index:     networkInterface.Index,
				Flags:     networkInterface.Flags,
				IP:        append(net.IP(nil), ip...),
			})
		}
	}
	return candidates
}

func chooseLocalIPv4(primary net.IP, candidates []localIPv4Candidate) localIPv4Candidate {
	return chooseLocalIPv4ForOS(goruntime.GOOS, primary, candidates)
}

func chooseLocalIPv4ForOS(goos string, primary net.IP, candidates []localIPv4Candidate) localIPv4Candidate {
	primary = primary.To4()
	candidates = append([]localIPv4Candidate(nil), candidates...)
	sort.Slice(candidates, func(left int, right int) bool {
		if candidates[left].Index != candidates[right].Index {
			return candidates[left].Index < candidates[right].Index
		}
		if candidates[left].Interface != candidates[right].Interface {
			return candidates[left].Interface < candidates[right].Interface
		}
		return candidates[left].IP.String() < candidates[right].IP.String()
	})
	preferredInterface := ""
	switch goos {
	case "darwin":
		preferredInterface = "en0"
	case "linux":
		preferredInterface = "eth0"
	}
	selectCandidate := func(matches func(localIPv4Candidate) bool) (localIPv4Candidate, bool) {
		for _, candidate := range candidates {
			if candidate.IP == nil || !matches(candidate) {
				continue
			}
			return candidate, true
		}
		return localIPv4Candidate{}, false
	}
	nonTunnel := func(candidate localIPv4Candidate) bool {
		return !isTunnelIPv4Candidate(candidate)
	}
	privateNonTunnel := func(candidate localIPv4Candidate) bool {
		return candidate.IP.IsPrivate() && nonTunnel(candidate)
	}
	if preferredInterface != "" {
		if selected, found := selectCandidate(func(candidate localIPv4Candidate) bool {
			return candidate.Interface == preferredInterface && privateNonTunnel(candidate)
		}); found {
			return selected
		}
	}
	if primary != nil {
		if selected, found := selectCandidate(func(candidate localIPv4Candidate) bool {
			return candidate.IP.Equal(primary) && privateNonTunnel(candidate)
		}); found {
			return selected
		}
	}
	if selected, found := selectCandidate(privateNonTunnel); found {
		return selected
	}
	if preferredInterface != "" {
		if selected, found := selectCandidate(func(candidate localIPv4Candidate) bool {
			return candidate.Interface == preferredInterface && nonTunnel(candidate)
		}); found {
			return selected
		}
	}
	if primary != nil {
		if selected, found := selectCandidate(func(candidate localIPv4Candidate) bool {
			return candidate.IP.Equal(primary) && nonTunnel(candidate)
		}); found {
			return selected
		}
	}
	if selected, found := selectCandidate(nonTunnel); found {
		return selected
	}
	if primary != nil {
		if selected, found := selectCandidate(func(candidate localIPv4Candidate) bool {
			return candidate.IP.Equal(primary)
		}); found {
			return selected
		}
	}
	if selected, found := selectCandidate(func(localIPv4Candidate) bool { return true }); found {
		return selected
	}
	if primary != nil {
		return localIPv4Candidate{IP: append(net.IP(nil), primary...)}
	}
	return localIPv4Candidate{}
}

func isTunnelIPv4Candidate(candidate localIPv4Candidate) bool {
	if candidate.Flags&net.FlagPointToPoint != 0 {
		return true
	}
	name := strings.ToLower(candidate.Interface)
	return strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap")
}
