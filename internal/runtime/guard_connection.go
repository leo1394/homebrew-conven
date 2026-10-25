package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Configured only by the bundled GUI adapter, never by environment variables.
var guardConnectionHelper string

func ConfigureGuardConnectionHelper(path string) { guardConnectionHelper = path }

type guardConnectionReply struct {
	Error string `json:"error"`
	PID int `json:"pid"`
	PGID int `json:"pgid"`
	Command []string `json:"command"`
	Log []byte `json:"log"`
	Offset int `json:"offset"`
	Exited bool `json:"exited"`
	ExitCode int `json:"exitCode"`
}

func callGuardConnection(ctx context.Context, request map[string]any) (guardConnectionReply, error) {
	var reply guardConnectionReply
	if guardConnectionHelper == "" { return reply, errors.New("this connection requires the signed Conven Guard application to manage it") }
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second); defer cancel()
	data, err := json.Marshal(request); if err != nil { return reply, err }
	command := exec.CommandContext(ctx, guardConnectionHelper)
	command.Stdin = bytes.NewReader(data)
	var output bytes.Buffer
	command.Stdout = &output
	// Credentials are sent only over stdin/XPC, never argv or diagnostic output.
	command.Stderr = io.Discard
	runErr := command.Run()
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil { return reply, fmt.Errorf("Guard system connection helper unavailable (approval or signature required): %w", errors.Join(err, runErr)) }
	if reply.Error != "" { return reply, errors.New(reply.Error) }
	return reply, runErr
}

func guardConnectionRequest(argv []string) (map[string]any, error) {
	if len(argv) < 3 { return nil, errors.New("missing ktctl connection arguments") }
	executable, err := os.Open(argv[0]); if err != nil { return nil, fmt.Errorf("read workspace ktctl.path: %w", err) }
	digest := sha256.New()
	_, digestErr := io.Copy(digest, executable); executable.Close()
	if digestErr != nil { return nil, fmt.Errorf("verify workspace ktctl.path: %w", digestErr) }
	var path string
	args := []string{}
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--kubeconfig" {
			if path != "" || i+1 >= len(argv) { return nil, errors.New("invalid kubeconfig option") }
			i++; path = argv[i]
		} else { args = append(args, argv[i]) }
	}
	if path == "" { return nil, errors.New("explicit pinned kubeconfig required") }
	file, err := os.Open(path); if err != nil { return nil, err }; defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 2*1024*1024+1)); if err != nil { return nil, err }
	if len(data) > 2*1024*1024 { return nil, errors.New("kubeconfig too large") }
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil { return nil, errors.New("cannot parse pinned kubeconfig") }
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil { return nil, err }
	identifier := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	return map[string]any{"operation":"start", "id":identifier, "arguments":args, "kubeconfig":config, "sourceDigest":fmt.Sprintf("%x", digest.Sum(nil))}, nil
}

func startGuardConnection(ctx context.Context, driver string, argv []string, logPath string, fingerprint string) (*ConnectionProcess, <-chan error, error) {
	if driver != "ktctl" { return nil, nil, errors.New("Guard system authorization supports only ktctl, not arbitrary connection commands") }
	request, err := guardConnectionRequest(argv); if err != nil { return nil, nil, err }
	logFile, err := openFreshLog(logPath); if err != nil { return nil, nil, err }
	reply, err := callGuardConnection(ctx, request)
	id := request["id"].(string)
	cleanup := func() error {
		_, err := callGuardConnection(context.Background(), map[string]any{"operation":"signal", "id":id, "signal":"KILL"}); return err
	}
	if err != nil {
		logFile.Close()
		if reply.Error != "" { return nil, nil, err }
		return nil, nil, errors.Join(err, cleanup())
	}
	if reply.PID <= 1 || reply.PGID != reply.PID || len(reply.Command) < 2 {
		logFile.Close(); return nil, nil, errors.Join(errors.New("invalid privileged connection identity"), cleanup())
	}
	identity, err := processIdentity(reply.PID)
	if err != nil { logFile.Close(); return nil, nil, errors.Join(err, cleanup()) }
	process := &ConnectionProcess{Driver:driver, PID:reply.PID, PGID:reply.PGID, Command:reply.Command, Identity:identity, LogPath:logPath, StartedAt:time.Now(), Owned:true, Elevated:true, Fingerprint:fingerprint, GuardConnectionID:id}
	completed := make(chan error, 1)
	go func() {
		defer logFile.Close()
		offset := 0
		for {
			response, err := callGuardConnection(context.Background(), map[string]any{"operation":"read", "id":id, "offset":offset})
			if err != nil { completed <- fmt.Errorf("Guard connection observation disconnected: %w", err); return }
			if _, err := logFile.Write(response.Log); err != nil { completed <- err; return }; offset = response.Offset
			if response.Exited {
				if response.ExitCode == 0 { completed <- nil } else { completed <- fmt.Errorf("ktctl exited with status %d", response.ExitCode) }; return
			}
			time.Sleep(300*time.Millisecond)
		}
	}()
	return process, completed, nil
}

func signalGuardConnection(process *ConnectionProcess, signal syscall.Signal) error {
	name := strings.TrimPrefix(map[syscall.Signal]string{syscall.SIGINT:"INT", syscall.SIGTERM:"TERM", syscall.SIGKILL:"KILL"}[signal], "SIG")
	if name == "" { return errors.New("unsupported Guard connection signal") }
	_, err := callGuardConnection(context.Background(), map[string]any{"operation":"signal", "id":process.GuardConnectionID, "signal":name})
	return err
}
