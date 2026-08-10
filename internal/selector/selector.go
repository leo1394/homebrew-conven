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

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var (
	ErrNoCandidates = errors.New("selector: no service candidates")
	ErrNotTerminal   = errors.New("selector: input is not a terminal")
)

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
		if err == nil && restoreErr != nil {
			names = nil
			confirmed = false
			err = fmt.Errorf("selector: restore terminal: %w", restoreErr)
		}
	}()
	defer func() {
		_, _ = io.WriteString(out, "\r\n")
	}()

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
		case modeConfirmed:
			return state.selectedNames(), true, nil
		case modeCancelled:
			return nil, false, nil
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
			mark = "x"
		}
		metadata := candidateMetadata(candidate)
		line := fmt.Sprintf("%s [%s] %s", cursor, mark, candidate.Name)
		if metadata != "" {
			line += "  " + metadata
		}
		if _, err := fmt.Fprintf(out, "%s\r\n", clip(line, width)); err != nil {
			return fmt.Errorf("selector: render: %w", err)
		}
	}

	if _, err := fmt.Fprintf(out, "\r\nShowing %d-%d of %d · selected %d\r\n", start+1, end, len(state.candidates), state.selectedCount()); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}
	if _, err := io.WriteString(out, "j/k or arrows move · f toggle · F toggle+next · a all/none · Enter confirm · q/Esc cancel\r\n"); err != nil {
		return fmt.Errorf("selector: render: %w", err)
	}
	if state.notice != "" {
		if _, err := fmt.Fprintf(out, "%s\r\n", state.notice); err != nil {
			return fmt.Errorf("selector: render: %w", err)
		}
	}
	return nil
}

func renderConfirmation(out io.Writer, state *pickerState) error {
	if _, err := fmt.Fprintf(out, "Looming local services: %s\r\n\r\n", strings.Join(state.selectedNames(), ", ")); err != nil {
		return fmt.Errorf("selector: render confirmation: %w", err)
	}
	if _, err := fmt.Fprintf(out, "Confirm? Type y or yes, then press Enter: %s", string(state.confirmation)); err != nil {
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
