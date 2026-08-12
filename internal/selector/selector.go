package selector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leo1394/homebrew-conven/internal/terminal"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var (
	ErrNoCandidates = errors.New("selector: no service candidates")
	ErrNotTerminal   = errors.New("selector: input is not a terminal")
)

const (
	selectorEnterScreen    = "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H"
	selectorLeaveScreen    = "\x1b[0m\x1b[?1049l\x1b[?25h"
	confirmationRetryLimit = 3
)

var errConfirmationRetriesExceeded = errors.New("confirmation failed after 3 invalid retries")

func Select(ctx context.Context, in *os.File, out io.Writer, candidates []Candidate) (names []string, confirmed bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if in == nil {
		return nil, false, ErrNotTerminal
	}
	if out == nil {
		return nil, false, errors.New("selector: output is nil")
	}
	if len(candidates) == 0 {
		return nil, false, ErrNoCandidates
	}

	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		return nil, false, ErrNotTerminal
	}
	outputFile, outputIsFile := out.(*os.File)
	if !outputIsFile || !term.IsTerminal(int(outputFile.Fd())) {
		return nil, false, ErrNotTerminal
	}

	previousState, rawErr := term.MakeRaw(fd)
	if rawErr != nil {
		return nil, false, fmt.Errorf("selector: enable raw terminal input: %w", rawErr)
	}
	defer func() {
		restoreErr := term.Restore(fd, previousState)
		if restoreErr != nil {
			names = nil
			confirmed = false
			err = errors.Join(err, fmt.Errorf("selector: restore terminal: %w", restoreErr))
		}
	}()
	screenActive := false
	defer func() {
		if screenActive {
			if _, leaveErr := io.WriteString(out, selectorLeaveScreen); leaveErr != nil {
				names = nil
				confirmed = false
				err = errors.Join(err, fmt.Errorf("selector: leave alternate screen: %w", leaveErr))
			}
		}
	}()
	screenActive = true
	if _, err := io.WriteString(out, selectorEnterScreen); err != nil {
		return nil, false, fmt.Errorf("selector: enter alternate screen: %w", err)
	}

	state := newPickerState(candidates)
	reader := bufio.NewReader(in)
	for {
		width, height := terminalSize(fd)
		if renderErr := render(out, state, width, height); renderErr != nil {
			return nil, false, renderErr
		}

		input, readErr := readKeyContext(ctx, reader, fd)
		if readErr != nil {
			return nil, false, fmt.Errorf("selector: read input: %w", readErr)
		}
		state.handle(input)

		switch state.mode {
		case modeConfirming:
			if _, err := io.WriteString(out, selectorLeaveScreen); err != nil {
				return nil, false, fmt.Errorf("selector: leave alternate screen: %w", err)
			}
			screenActive = false
			return confirmSelection(ctx, reader, fd, out, state)
		case modeCancelled:
			return nil, false, nil
		}
	}
}

func confirmSelection(ctx context.Context, reader *bufio.Reader, fd int, out io.Writer, state *pickerState) (names []string, confirmed bool, err error) {
	defer func() {
		if _, newlineErr := io.WriteString(out, "\r\n"); newlineErr != nil {
			names = nil
			confirmed = false
			err = errors.Join(err, fmt.Errorf("selector: finish confirmation: %w", newlineErr))
		}
	}()
	if err := renderConfirmation(out, state); err != nil {
		return nil, false, err
	}
	style := terminal.New(out)
	invalidRetries := 0
	for {
		input, err := readKeyContext(ctx, reader, fd)
		if err != nil {
			return nil, false, fmt.Errorf("selector: read confirmation: %w", err)
		}
		confirmationLength := len(state.confirmation)
		state.handle(input)
		switch input.kind {
		case keyBackspace:
			if len(state.confirmation) < confirmationLength {
				if _, err := io.WriteString(out, "\b \b"); err != nil {
					return nil, false, fmt.Errorf("selector: echo confirmation: %w", err)
				}
			}
		case keyRune:
			if len(state.confirmation) > confirmationLength {
				if _, err := fmt.Fprintf(out, "%c", input.rune); err != nil {
					return nil, false, fmt.Errorf("selector: echo confirmation: %w", err)
				}
			}
		}
		switch state.mode {
		case modeConfirmed:
			return state.selectedNames(), true, nil
		case modeCancelled:
			return nil, false, nil
		}
		if input.kind == keyEnter {
			if invalidRetries >= confirmationRetryLimit {
				return nil, false, errConfirmationRetriesExceeded
			}
			invalidRetries++
			if _, err := fmt.Fprintf(out, "\r\n%s\r\n", style.Failure("Please enter y/yes or n/no.")); err != nil {
				return nil, false, fmt.Errorf("selector: render confirmation retry: %w", err)
			}
			if err := renderConfirmationPrompt(out); err != nil {
				return nil, false, err
			}
		}
	}
}

func readKeyContext(ctx context.Context, reader *bufio.Reader, fd int) (key, error) {
	for {
		if err := ctx.Err(); err != nil {
			return key{}, err
		}
		ready, err := waitForInput(reader, fd, 100*time.Millisecond)
		if err != nil {
			return key{}, err
		}
		if ready {
			return readKey(reader, fd)
		}
	}
}

func terminalSize(fd int) (int, int) {
	width, height, err := term.GetSize(fd)
	if err != nil || width < 20 || height < 8 {
		return 100, 24
	}
	return width, height
}

func render(out io.Writer, state *pickerState, width int, height int) error {
	if _, err := io.WriteString(out, "\x1b[2J\x1b[H"); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}

	if state.mode == modeConfirming {
		return renderConfirmation(out, state)
	}
	return renderPicker(out, state, width, height)
}

func renderPicker(out io.Writer, state *pickerState, width int, height int) error {
	style := terminal.New(out)
	if _, err := io.WriteString(out, "Select local services\r\n\r\n"); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}

	rowLimit := height - 7
	if rowLimit < 1 {
		rowLimit = 1
	}
	start := 0
	if state.cursor >= rowLimit {
		start = state.cursor - rowLimit + 1
	}
	if start+rowLimit > len(state.candidates) {
		start = len(state.candidates) - rowLimit
		if start < 0 {
			start = 0
		}
	}
	end := start + rowLimit
	if end > len(state.candidates) {
		end = len(state.candidates)
	}

	for index := start; index < end; index++ {
		candidate := state.candidates[index]
		cursor := " "
		if index == state.cursor {
			cursor = ">"
		}
		mark := " "
		if state.selected[index] {
			mark = "✔︎"
		}
		metadata := candidateMetadata(candidate)
		line := fmt.Sprintf("%s [%s] %s", cursor, mark, candidate.Name)
		if metadata != "" {
			line += "  " + metadata
		}
		line = clip(line, width)
		if state.selected[index] {
			line = style.Selection(line, index == state.cursor)
		}
		if _, err := fmt.Fprintf(out, "%s\r\n", line); err != nil {
			return fmt.Errorf("selector: render: %w", err)
		}
	}

	selectedCount := style.Success(fmt.Sprintf("%d", state.selectedCount()))
	if _, err := fmt.Fprintf(out, "\r\nShowing %d-%d of %d · selected %s\r\n", start+1, end, len(state.candidates), selectedCount); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}
	if _, err := io.WriteString(out, "[f|A] selection · [Enter] confirm · [q/Esc] cancel\r\n"); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}
	if state.notice != "" {
		if _, err := fmt.Fprintf(out, "%s\r\n", style.Failure(state.notice)); err != nil {
			return fmt.Errorf("selector: render: %w", err)
		}
	}
	return nil
}

func renderConfirmation(out io.Writer, state *pickerState) error {
	if _, err := fmt.Fprintf(out, "Convening local services: %s\r\n\r\n", strings.Join(state.selectedNames(), ", ")); err != nil {
		return fmt.Errorf("selector: render confirmation: %w", err)
	}
	return renderConfirmationPrompt(out)
}

func renderConfirmationPrompt(out io.Writer) error {
	if _, err := io.WriteString(out, "Confirm? [y/yes] continue · [n/no] cancel: "); err != nil {
		return fmt.Errorf("selector: render confirmation: %w", err)
	}
	return nil
}

func candidateMetadata(candidate Candidate) string {
	parts := make([]string, 0, 2)
	if candidate.Path != "" {
		parts = append(parts, candidate.Path)
	}
	if candidate.Detail != "" {
		parts = append(parts, candidate.Detail)
	}
	return strings.Join(parts, " · ")
}

func clip(value string, width int) string {
	if width < 2 || utf8.RuneCountInString(value) <= width {
		return value
	}
	runes := []rune(value)
	return string(runes[:width-1]) + "…"
}

func readKey(reader *bufio.Reader, fd int) (key, error) {
	value, err := reader.ReadByte()
	if err != nil {
		return key{}, err
	}

	switch value {
	case '\r', '\n':
		return key{kind: keyEnter}, nil
	case 0x03, 0x1b:
		if value == 0x1b {
			return readEscapeSequence(reader, fd)
		}
		return key{kind: keyEscape}, nil
	case 0x08, 0x7f:
		return key{kind: keyBackspace}, nil
	}

	if value < utf8.RuneSelf {
		return key{kind: keyRune, rune: rune(value)}, nil
	}
	if err := reader.UnreadByte(); err != nil {
		return key{}, err
	}
	decoded, _, err := reader.ReadRune()
	if err != nil {
		return key{}, err
	}
	return key{kind: keyRune, rune: decoded}, nil
}

func readEscapeSequence(reader *bufio.Reader, fd int) (key, error) {
	ready, err := waitForInput(reader, fd, 30*time.Millisecond)
	if err != nil {
		return key{}, err
	}
	if !ready {
		return key{kind: keyEscape}, nil
	}
	prefix, err := reader.ReadByte()
	if err != nil {
		return key{}, err
	}
	if prefix != '[' && prefix != 'O' {
		return key{kind: keyEscape}, nil
	}
	ready, err = waitForInput(reader, fd, 30*time.Millisecond)
	if err != nil {
		return key{}, err
	}
	if !ready {
		return key{kind: keyEscape}, nil
	}
	direction, err := reader.ReadByte()
	if err != nil {
		return key{}, err
	}
	switch direction {
	case 'A':
		return key{kind: keyUp}, nil
	case 'B':
		return key{kind: keyDown}, nil
	default:
		return key{kind: keyEscape}, nil
	}
}

func waitForInput(reader *bufio.Reader, fd int, timeout time.Duration) (bool, error) {
	if reader.Buffered() > 0 {
		return true, nil
	}
	if fd < 0 {
		return false, nil
	}

	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(descriptors, int(timeout/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
		if descriptors[0].Revents&unix.POLLNVAL != 0 {
			return false, errors.New("selector: terminal input descriptor is invalid")
		}
		return descriptors[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
	}
}
