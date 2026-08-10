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
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	dashboardHistoryLines = 2000
	dashboardFrameInterval = 50 * time.Millisecond
)

type dashboardService struct {
	Name  string
	Ports map[string]int
}

type dashboardInfo struct {
	Workspace   string
	Environment string
	Address     string
	Interface   string
	Services    []dashboardService
}

type localIPv4Candidate struct {
	Interface string
	Index     int
	Flags     net.Flags
	IP        net.IP
}

func TailLogs(ctx context.Context, workspace *WorkspaceData, session *Session, names []string, input *os.File, output io.Writer) error {
	logs, err := selectLogs(session, names)
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return errors.New("no logs are available for the current session")
	}
	outputFile, outputIsFile := output.(*os.File)
	if input == nil || !outputIsFile || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(outputFile.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return streamLogs(ctx, logs, func(entry logEntry) error {
			_, err := fmt.Fprintf(output, "[%s] %s\n", entry.Name, entry.Line)
			return err
		})
	}
	width, height, err := term.GetSize(int(outputFile.Fd()))
	if err != nil || width < 20 || height < 4 {
		return streamLogs(ctx, logs, func(entry logEntry) error {
			_, err := fmt.Fprintf(output, "[%s] %s\n", entry.Name, entry.Line)
			return err
		})
	}
	address, interfaceName := discoverLocalIPv4()
	info := dashboardInfo{
		Workspace:   workspace.Manifest.Workspace.Name,
		Environment: session.Environment,
		Address:     address,
		Interface:   interfaceName,
		Services:    dashboardServices(workspace, session, names),
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
			_, exitErr = io.WriteString(output, "\x1b[0m\x1b[?7h\x1b[?25h\x1b[?1049l")
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
	if _, err := io.WriteString(output, "\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[2J"); err != nil {
		return fmt.Errorf("dashboard: enter screen: %w", err)
	}
	dashboardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	entries, logErrors := startLogStream(dashboardContext, logs)
	keys, inputErrors := readDashboardInput(dashboardContext, input)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	ticker := time.NewTicker(dashboardFrameInterval)
	defer ticker.Stop()

	history := make([]string, 0, logTailLines*len(logs))
	if err := writeDashboardFrame(output, info, width, height, history); err != nil {
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
			history = appendDashboardHistory(history, fmt.Sprintf("[%s] %s", entry.Name, entry.Line))
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
			if key == 0x03 || key == 'q' || key == 'Q' {
				return nil
			}
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
			if err := writeDashboardFrame(output, info, width, height, history); err != nil {
				return err
			}
			dirty = false
		case <-ticker.C:
			if !dirty {
				continue
			}
			if err := writeDashboardFrame(output, info, width, height, history); err != nil {
				return err
			}
			dirty = false
		case <-dashboardContext.Done():
			return nil
		}
	}
	if dirty {
		return writeDashboardFrame(output, info, width, height, history)
	}
	return nil
}

func readDashboardInput(ctx context.Context, input *os.File) (<-chan byte, <-chan error) {
	keys := make(chan byte, 16)
	errorsChannel := make(chan error, 1)
	go func() {
		defer close(keys)
		defer close(errorsChannel)
		descriptors := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		buffer := make([]byte, 32)
		for {
			if ctx.Err() != nil {
				return
			}
			ready, err := unix.Poll(descriptors, 100)
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				errorsChannel <- fmt.Errorf("dashboard: poll terminal input: %w", err)
				return
			}
			if ready == 0 {
				continue
			}
			count, err := input.Read(buffer)
			if err != nil {
				if ctx.Err() == nil {
					errorsChannel <- fmt.Errorf("dashboard: read terminal input: %w", err)
				}
				return
			}
			for _, key := range buffer[:count] {
				select {
				case keys <- key:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return keys, errorsChannel
}

func writeDashboardFrame(output io.Writer, info dashboardInfo, width int, height int, history []string) error {
	frame := renderDashboardFrame(info, width, height, history)
	if _, err := io.WriteString(output, frame); err != nil {
		return fmt.Errorf("dashboard: render: %w", err)
	}
	return nil
}

func renderDashboardFrame(info dashboardInfo, width int, height int, history []string) string {
	if width < 1 {
		width = 1
	}
	if height < 2 {
		height = 2
	}
	banner := dashboardBannerLines(info, height)
	logRows := height - len(banner)
	start := len(history) - logRows
	if start < 0 {
		start = 0
	}
	visible := history[start:]
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for row := 0; row < height; row++ {
		frame.WriteString("\x1b[2K")
		if row < len(banner) {
			frame.WriteString("\x1b[7m")
			frame.WriteString(fitDashboardLine(banner[row], width, true))
			frame.WriteString("\x1b[0m")
		} else if index := row - len(banner); index < len(visible) {
			frame.WriteString(fitDashboardLine(visible[index], width, false))
		}
		if row+1 < height {
			frame.WriteString("\r\n")
		}
	}
	return frame.String()
}

func dashboardBannerLines(info dashboardInfo, height int) []string {
	interfaceLabel := ""
	if info.Interface != "" {
		interfaceLabel = " (" + info.Interface + ")"
	}
	title := fmt.Sprintf(" LOOM | %s | env=%s | LAN=%s%s", info.Workspace, info.Environment, info.Address, interfaceLabel)
	maximumBannerRows := height - 1
	if maximumBannerRows < 1 {
		return []string{title}
	}
	serviceRows := maximumBannerRows - 2
	if serviceRows < 0 {
		serviceRows = 0
	}
	lines := []string{title}
	shown := len(info.Services)
	if shown > serviceRows {
		shown = serviceRows
		if serviceRows > 0 {
			shown--
		}
	}
	for index := 0; index < shown; index++ {
		service := info.Services[index]
		lines = append(lines, fmt.Sprintf(" %s | ports %s", service.Name, dashboardPorts(service.Ports)))
	}
	if shown < len(info.Services) && serviceRows > 0 {
		lines = append(lines, fmt.Sprintf(" ... +%d more services", len(info.Services)-shown))
	}
	if len(lines) < maximumBannerRows {
		lines = append(lines, " LOGS | q/Ctrl-C detach; services keep running")
	}
	return lines
}

func dashboardPorts(ports map[string]int) string {
	if len(ports) == 0 {
		return "-"
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, fmt.Sprintf("%s=%d", name, ports[name]))
	}
	return strings.Join(values, ", ")
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

func appendDashboardHistory(history []string, line string) []string {
	history = append(history, sanitizeDashboardText(line))
	if len(history) > dashboardHistoryLines {
		history = append([]string(nil), history[len(history)-dashboardHistoryLines:]...)
	}
	return history
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
		services = append(services, dashboardService{Name: name, Ports: ports})
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
			services = append(services, dashboardService{Name: process.Name, Ports: ports})
		}
	}
	return services
}

func discoverLocalIPv4() (string, string) {
	primary := defaultRouteIPv4()
	candidates := interfaceIPv4Candidates()
	selected := chooseLocalIPv4(primary, candidates)
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
	primary = primary.To4()
	if primary != nil {
		for _, candidate := range candidates {
			if candidate.IP.Equal(primary) {
				return candidate
			}
		}
		return localIPv4Candidate{IP: append(net.IP(nil), primary...)}
	}
	candidates = append([]localIPv4Candidate(nil), candidates...)
	sort.Slice(candidates, func(left int, right int) bool {
		leftScore := localIPv4Score(candidates[left])
		rightScore := localIPv4Score(candidates[right])
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		if candidates[left].Index != candidates[right].Index {
			return candidates[left].Index < candidates[right].Index
		}
		if candidates[left].Interface != candidates[right].Interface {
			return candidates[left].Interface < candidates[right].Interface
		}
		return candidates[left].IP.String() < candidates[right].IP.String()
	})
	if len(candidates) == 0 {
		return localIPv4Candidate{}
	}
	return candidates[0]
}

func localIPv4Score(candidate localIPv4Candidate) int {
	score := 0
	if !candidate.IP.IsPrivate() {
		score += 1
	}
	if candidate.Flags&net.FlagPointToPoint != 0 {
		score += 2
	}
	return score
}
