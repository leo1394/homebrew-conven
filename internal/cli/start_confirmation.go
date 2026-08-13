package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/terminal"
	"golang.org/x/term"
)

func confirmStartReplacement(ctx context.Context, input *os.File, output io.Writer, services []string) (bool, error) {
	message := fmt.Sprintf("workspace already has running services: %s; replacement confirmation requires an interactive terminal; use conven services --restart or conven services --stop first", strings.Join(services, ", "))
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return false, errors.New(message)
	}
	outputFile, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(outputFile.Fd())) {
		return false, errors.New(message)
	}
	return askStartReplacementContext(ctx, input, output, services)
}

func askStartReplacement(input io.Reader, output io.Writer, services []string) (bool, error) {
	if err := writeStartReplacementPrompt(output, services); err != nil {
		return false, fmt.Errorf("write start replacement prompt: %w", err)
	}
	reader := bufio.NewReader(input)
	for {
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read start replacement confirmation: %w", err)
		}
		if replace, known := startReplacementAnswer(answer); known {
			return replace, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if _, err := fmt.Fprint(output, terminal.New(output).Action("Choose [s] Stop then start or [c] Cancel: ")); err != nil {
			return false, fmt.Errorf("write start replacement prompt: %w", err)
		}
	}
}

func askStartReplacementContext(ctx context.Context, input *os.File, output io.Writer, services []string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := writeStartReplacementPrompt(output, services); err != nil {
		return false, fmt.Errorf("write start replacement prompt: %w", err)
	}
	reader := bufio.NewReader(input)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ready, err := waitForConfirmationInput(reader, int(input.Fd()), 100*time.Millisecond)
		if err != nil {
			return false, fmt.Errorf("wait for start replacement confirmation: %w", err)
		}
		if !ready {
			continue
		}
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read start replacement confirmation: %w", err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		if replace, known := startReplacementAnswer(answer); known {
			return replace, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if _, err := fmt.Fprint(output, terminal.New(output).Action("Choose [s] Stop then start or [c] Cancel: ")); err != nil {
			return false, fmt.Errorf("write start replacement prompt: %w", err)
		}
	}
}

func writeStartReplacementPrompt(output io.Writer, services []string) error {
	printWarningBlock(output, "Workspace already has running services.", []string{
		"Services: " + strings.Join(services, ", "),
	}, nil)
	_, err := fmt.Fprint(output, terminal.New(output).Action("Choose [s] Stop then start or [c] Cancel (default): "))
	return err
}

func startReplacementAnswer(answer string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "s", "stop", "start", "y", "yes":
		return true, true
	case "", "c", "cancel", "n", "no":
		return false, true
	default:
		return false, false
	}
}
