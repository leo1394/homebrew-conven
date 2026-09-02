package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type observedListener struct {
	PID     int
	Address string
	Port    int
}

func preflightServicePorts(service PlannedService) error {
	for _, kind := range service.Kinds {
		port := serviceListenerPort(service, kind)
		if port < 1 {
			continue
		}
		host := "127.0.0.1"
		if service.NetworkListen == model.NetworkListenAllInterfaces {
			host = "0.0.0.0"
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("service %s %s port %d is unavailable before start: %w", service.Name, kind, port, err)
		}
		if err := listener.Close(); err != nil {
			return fmt.Errorf("release service %s %s port preflight: %w", service.Name, kind, err)
		}
	}
	return nil
}

func verifyServiceListeners(ctx context.Context, service PlannedService, process ServiceProcess) (map[string]ListenerEvidence, error) {
	observed, err := processGroupListeners(ctx, process.PGID)
	if err != nil {
		return nil, fmt.Errorf("observe %s listeners: %w", service.Name, err)
	}
	result := make(map[string]ListenerEvidence, len(service.Kinds))
	for _, kind := range service.Kinds {
		port := serviceListenerPort(service, kind)
		matches := make([]observedListener, 0, 1)
		for _, listener := range observed {
			if listener.Port == port {
				matches = append(matches, listener)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("service %s process group %d does not own the declared %s listener on port %d", service.Name, process.PGID, kind, port)
		}
		sort.Slice(matches, func(left int, right int) bool { return matches[left].PID < matches[right].PID })
		match := matches[0]
		if err := verifyListenerMode(match.Address, service.NetworkListen); err != nil {
			return nil, fmt.Errorf("service %s %s listener %s:%d: %w", service.Name, kind, match.Address, port, err)
		}
		result[kind] = ListenerEvidence{Address: match.Address, Port: port, Mode: service.NetworkListen, OwnerPID: match.PID, VerifiedAt: time.Now()}
	}
	return result, nil
}

func serviceListenerPort(service PlannedService, kind string) int {
	if service.Config != nil {
		if port := service.Config.IsolationFor(kind).ListenerPort; port > 0 {
			return port
		}
	}
	return service.Ports[kind]
}

func verifyListenerMode(address string, mode string) error {
	address = strings.Trim(address, "[]")
	ip := net.ParseIP(address)
	if mode == model.NetworkListenAllInterfaces {
		if address == "*" || address == "0.0.0.0" || address == "::" || ip != nil && ip.IsUnspecified() {
			return nil
		}
		return fmt.Errorf("all-interfaces requires a wildcard address")
	}
	if address == "localhost" || ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("loopback requires 127.0.0.1 or ::1")
}

func processGroupListeners(ctx context.Context, pgid int) ([]observedListener, error) {
	switch runtime.GOOS {
	case "darwin":
		return darwinProcessGroupListeners(ctx, pgid)
	case "linux":
		return linuxProcessGroupListeners(pgid)
	default:
		return nil, fmt.Errorf("listener ownership verification is unsupported on %s", runtime.GOOS)
	}
}

func darwinProcessGroupListeners(ctx context.Context, pgid int) ([]observedListener, error) {
	lsof := "/usr/sbin/lsof"
	if _, err := os.Stat(lsof); err != nil {
		var lookupErr error
		lsof, lookupErr = exec.LookPath("lsof")
		if lookupErr != nil {
			return nil, errors.New("lsof is required for listener ownership verification on Darwin")
		}
	}
	command := exec.CommandContext(ctx, lsof, "-nP", "-a", "-g", strconv.Itoa(pgid), "-iTCP", "-sTCP:LISTEN", "-Fpn")
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(output) == 0 {
			return nil, nil
		}
		return nil, err
	}
	result := make([]observedListener, 0)
	pid := 0
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(strings.TrimPrefix(line, "p"))
			continue
		}
		if !strings.HasPrefix(line, "n") || pid < 1 {
			continue
		}
		address, port, found := splitObservedAddress(strings.TrimPrefix(line, "n"))
		if found {
			result = append(result, observedListener{PID: pid, Address: address, Port: port})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func splitObservedAddress(value string) (string, int, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(value, " (LISTEN)"))
	if strings.HasPrefix(value, "TCP ") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "TCP "))
	}
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil {
		return "", 0, false
	}
	address := strings.Trim(value[:index], "[]")
	return address, port, true
}

func linuxProcessGroupListeners(pgid int) ([]observedListener, error) {
	pids, err := linuxProcessGroupPIDs(pgid)
	if err != nil {
		return nil, err
	}
	inodes := make(map[string]int)
	for _, pid := range pids {
		entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = pid
		}
	}
	result := make([]observedListener, 0)
	for _, source := range []struct { Path string; IPv6 bool }{{"/proc/net/tcp", false}, {"/proc/net/tcp6", true}} {
		listeners, err := linuxTCPListeners(source.Path, source.IPv6, inodes)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		result = append(result, listeners...)
	}
	return result, nil
}

func linuxProcessGroupPIDs(pgid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := make([]int, 0)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		closing := strings.LastIndex(string(data), ")")
		if closing < 0 {
			continue
		}
		fields := strings.Fields(string(data)[closing+1:])
		if len(fields) < 3 {
			continue
		}
		processGroup, _ := strconv.Atoi(fields[2])
		if processGroup == pgid {
			result = append(result, pid)
		}
	}
	return result, nil
}

func linuxTCPListeners(path string, ipv6 bool, owned map[string]int) ([]observedListener, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]observedListener, 0)
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		pid, found := owned[fields[9]]
		if !found {
			continue
		}
		addressPort := strings.SplitN(fields[1], ":", 2)
		if len(addressPort) != 2 {
			continue
		}
		port64, err := strconv.ParseInt(addressPort[1], 16, 32)
		if err != nil {
			continue
		}
		address := linuxHexAddress(addressPort[0], ipv6)
		result = append(result, observedListener{PID: pid, Address: address, Port: int(port64)})
	}
	return result, scanner.Err()
}

func linuxHexAddress(value string, ipv6 bool) string {
	if !ipv6 && len(value) == 8 {
		parts := make([]string, 0, 4)
		for index := 6; index >= 0; index -= 2 {
			part, _ := strconv.ParseInt(value[index:index+2], 16, 16)
			parts = append(parts, strconv.Itoa(int(part)))
		}
		return strings.Join(parts, ".")
	}
	if ipv6 && value == strings.Repeat("0", 32) {
		return "::"
	}
	if ipv6 && strings.HasSuffix(value, "01000000") && strings.TrimSuffix(value, "01000000") == strings.Repeat("0", 24) {
		return "::1"
	}
	return value
}

func localAddresses() map[string]bool {
	result := map[string]bool{"127.0.0.1": true, "::1": true, "0.0.0.0": true, "::": true}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return result
	}
	for _, address := range addresses {
		value := address.String()
		if host, _, err := net.ParseCIDR(value); err == nil {
			result[host.String()] = true
		}
	}
	return result
}
