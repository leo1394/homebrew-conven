package selector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func TestReadKeyRecognizesMovement(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  keyKind
	}{
		{name: "up", input: "\x1b[A", want: keyUp},
		{name: "down", input: "\x1b[B", want: keyDown},
		{name: "escape", input: "\x1b", want: keyEscape},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readKey(bufio.NewReader(strings.NewReader(test.input)), -1)
			if err != nil {
				t.Fatalf("readKey returned an error: %v", err)
			}
			if got.kind != test.want {
				t.Fatalf("readKey kind = %d, want %d", got.kind, test.want)
			}
		})
	}
}

func TestRenderConfirmationIncludesConveningServices(t *testing.T) {
	state := selectedPickerState()
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}

	want := "Convening local services: user-svc"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("confirmation output %q does not contain %q", output.String(), want)
	}
	if !strings.Contains(output.String(), "Confirm? [y] continue · [n] cancel: ") {
		t.Fatalf("confirmation output has the wrong prompt: %q", output.String())
	}
}

func TestConfirmationRendersOnceAndEchoesInput(t *testing.T) {
	names, confirmed, err, output := runConfirmation(t, "yes\r")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || len(names) != 1 || names[0] != "user-svc" {
		t.Fatalf("confirmation result names=%v confirmed=%t", names, confirmed)
	}
	if strings.Count(output, "Convening local services:") != 1 || strings.Count(output, "Confirm? [y] continue · [n] cancel: ") != 1 {
		t.Fatalf("confirmation output was redrawn: %q", output)
	}
	if !strings.HasSuffix(output, "yes\r\n") {
		t.Fatalf("confirmation output did not echo input once: %q", output)
	}
}

func TestConfirmationAcceptsNoWithoutRetry(t *testing.T) {
	names, confirmed, err, output := runConfirmation(t, "no\r")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed || names != nil {
		t.Fatalf("confirmation result names=%v confirmed=%t, want cancellation", names, confirmed)
	}
	if strings.Count(output, "Confirm? [y] continue · [n] cancel: ") != 1 {
		t.Fatalf("confirmation unexpectedly retried: %q", output)
	}
	if !strings.HasSuffix(output, "no\r\n") {
		t.Fatalf("confirmation output did not echo cancellation once: %q", output)
	}
}

func TestConfirmationRetriesInvalidInputThreeTimesThenConfirms(t *testing.T) {
	names, confirmed, err, output := runConfirmation(t, "maybe\rq\r\rYES\r")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || len(names) != 1 || names[0] != "user-svc" {
		t.Fatalf("confirmation result names=%v confirmed=%t", names, confirmed)
	}
	if strings.Count(output, "Convening local services:") != 1 {
		t.Fatalf("confirmation summary was redrawn: %q", output)
	}
	if strings.Count(output, "Confirm? [y] continue · [n] cancel: ") != 4 {
		t.Fatalf("confirmation prompt count is wrong: %q", output)
	}
	if strings.Count(output, "Please enter y/yes or n/no.") != 3 {
		t.Fatalf("confirmation retry notice count is wrong: %q", output)
	}
	if !strings.HasSuffix(output, "YES\r\n") {
		t.Fatalf("confirmation output did not finish with the valid answer: %q", output)
	}
}

func TestConfirmationStopsAfterThreeRetries(t *testing.T) {
	names, confirmed, err, output := runConfirmation(t, "bad\rbad\rbad\rbad\r")
	if !errors.Is(err, errConfirmationRetriesExceeded) {
		t.Fatalf("confirmation error = %v, want %v", err, errConfirmationRetriesExceeded)
	}
	if confirmed || names != nil {
		t.Fatalf("confirmation result names=%v confirmed=%t after retry exhaustion", names, confirmed)
	}
	if strings.Count(output, "Confirm? [y] continue · [n] cancel: ") != 4 {
		t.Fatalf("confirmation prompt count is wrong: %q", output)
	}
	if strings.Count(output, "Please enter y/yes or n/no.") != 3 {
		t.Fatalf("confirmation retry notice count is wrong: %q", output)
	}
}

func TestSelectorScreenUsesAlternateBuffer(t *testing.T) {
	if !strings.Contains(selectorEnterScreen, "\x1b[?1049h") {
		t.Fatalf("selector enter sequence does not use the alternate buffer: %q", selectorEnterScreen)
	}
	if !strings.Contains(selectorLeaveScreen, "\x1b[?1049l") {
		t.Fatalf("selector leave sequence does not restore the main buffer: %q", selectorLeaveScreen)
	}
	if !strings.Contains(selectorEnterScreen, "\x1b[?25l") || !strings.Contains(selectorLeaveScreen, "\x1b[?25h") {
		t.Fatalf("selector screen sequences do not restore cursor visibility: enter=%q leave=%q", selectorEnterScreen, selectorLeaveScreen)
	}
}

func TestRenderPickerShowsCompactControlsAndSelectedCount(t *testing.T) {
	state := newPickerState(testCandidates())
	state.handle(key{kind: keyRune, rune: 'f'})
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}

	for _, expected := range []string{
		"> [✔︎] user-svc",
		"selected 1",
		"[f|A] selection · [Enter] confirm · [q/Esc] cancel",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("picker output %q does not contain %q", output.String(), expected)
		}
	}
	for _, hidden := range []string{"j/k", "arrows move", "F toggle+next", "all/none"} {
		if strings.Contains(output.String(), hidden) {
			t.Fatalf("picker output %q still contains hidden help %q", output.String(), hidden)
		}
	}
}

func TestRenderPickerShowsSelectedServiceAfterCursorMoves(t *testing.T) {
	state := newPickerState(testCandidates())
	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyDown})
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}

	want := "  [✔︎] user-svc  /workspace/user-svc · Go\r\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("picker output %q does not contain %q", output.String(), want)
	}
}

func TestRenderPickerShowsEmptySelectionNotice(t *testing.T) {
	state := newPickerState(testCandidates())
	state.handle(key{kind: keyEnter})
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}
	if !strings.Contains(output.String(), "Select at least one service before confirming.") {
		t.Fatalf("picker output does not contain the empty selection notice: %q", output.String())
	}
}

func TestRenderSinglePickerUsesPromptAndSingleSelectionControls(t *testing.T) {
	prompt := Prompt{
		Title:                "Select a plugin",
		ConfirmationLabel:    "Running plugin",
		EmptySelectionNotice: "Select one plugin before confirming.",
	}
	state := newSinglePickerState([]Candidate{{Name: "generate-policy", Tag: "global"}}, prompt)
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}

	for _, expected := range []string{
		"Select a plugin",
		"> [ ] generate-policy · global",
		"[f] selection · [Enter] confirm · [q/Esc] cancel",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("single picker output %q does not contain %q", output.String(), expected)
		}
	}
	if strings.Contains(output.String(), "[f|A]") {
		t.Fatalf("single picker output exposes multi-select control: %q", output.String())
	}

	state.handle(key{kind: keyEnter})
	if state.notice != prompt.EmptySelectionNotice {
		t.Fatalf("single picker notice = %q, want %q", state.notice, prompt.EmptySelectionNotice)
	}
	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyEnter})
	output.Reset()
	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render confirmation returned an error: %v", err)
	}
	if !strings.Contains(output.String(), "Running plugin: generate-policy") {
		t.Fatalf("single confirmation output = %q", output.String())
	}
}

func TestRenderUnselectedCandidateTagReservesWidthBeforeStyling(t *testing.T) {
	style := terminal.New(&bytes.Buffer{})
	line := renderUnselectedCandidateLine(style, "> [ ] generate-policy  /a/very/long/path", "global", 30)
	if utf8.RuneCountInString(line) > 30 {
		t.Fatalf("tagged line exceeds width: %q", line)
	}
	if !strings.HasSuffix(line, " · global") {
		t.Fatalf("tagged line does not preserve tag: %q", line)
	}
}

func TestSingleConfirmationReturnsCompleteCandidate(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err := write.WriteString("yes\r"); err != nil {
		write.Close()
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Name: "generate-policy", Path: "/plugins/generate-policy.py", Detail: "Python", Tag: "global"}
	state := newSinglePickerState([]Candidate{candidate}, Prompt{
		ConfirmationLabel:    "Running plugin",
		EmptySelectionNotice: "Select one plugin before confirming.",
	})
	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyEnter})
	var output bytes.Buffer

	selected, confirmed, err := confirmSelection(context.Background(), bufio.NewReader(read), int(read.Fd()), &output, state)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || len(selected) != 1 || selected[0] != candidate {
		t.Fatalf("single confirmation selected=%#v confirmed=%t", selected, confirmed)
	}
}

func TestSelectOneRejectsNoCandidates(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()

	selected, confirmed, err := SelectOne(context.Background(), read, &bytes.Buffer{}, Prompt{}, nil)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("SelectOne error = %v, want ErrNoCandidates", err)
	}
	if confirmed || selected != (Candidate{}) {
		t.Fatalf("SelectOne result selected=%#v confirmed=%t", selected, confirmed)
	}
}

func TestSelectRejectsNoCandidates(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned an error: %v", err)
	}
	defer read.Close()
	defer write.Close()

	_, confirmed, err := Select(context.Background(), read, &bytes.Buffer{}, nil)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("Select error = %v, want ErrNoCandidates", err)
	}
	if confirmed {
		t.Fatal("Select should not confirm invalid input")
	}
}

func TestSelectRejectsNonTerminalInput(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned an error: %v", err)
	}
	defer read.Close()
	defer write.Close()

	_, confirmed, err := Select(context.Background(), read, &bytes.Buffer{}, testCandidates())
	if !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Select error = %v, want ErrNotTerminal", err)
	}
	if confirmed {
		t.Fatal("Select should not confirm invalid input")
	}
}

func runConfirmation(t *testing.T, input string) ([]string, bool, error, string) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err := write.WriteString(input); err != nil {
		write.Close()
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	state := selectedPickerState()
	var output bytes.Buffer

	selected, confirmed, err := confirmSelection(context.Background(), bufio.NewReader(read), int(read.Fd()), &output, state)
	var names []string
	if selected != nil {
		names = make([]string, 0, len(selected))
		for _, candidate := range selected {
			names = append(names, candidate.Name)
		}
	}
	return names, confirmed, err, output.String()
}
