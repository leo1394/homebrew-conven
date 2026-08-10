package selector

import "testing"

func TestFPressedTwiceCancelsSelection(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyRune, rune: 'f'})
	if !state.selected[0] {
		t.Fatal("first f should select the current candidate")
	}
	if state.cursor != 0 {
		t.Fatalf("f should not move the cursor, got %d", state.cursor)
	}

	state.handle(key{kind: keyRune, rune: 'f'})
	if state.selected[0] {
		t.Fatal("second f should cancel the current selection")
	}
}

func TestUpperFTogglesAndMovesDown(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyRune, rune: 'F'})

	if !state.selected[0] {
		t.Fatal("F should select the current candidate")
	}
	if state.cursor != 1 {
		t.Fatalf("F should move down after toggling, got cursor %d", state.cursor)
	}
}

func TestEnterCannotConfirmEmptySelection(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyEnter})

	if state.mode != modePicking {
		t.Fatalf("empty selection should remain in picker mode, got %d", state.mode)
	}
	if state.notice == "" {
		t.Fatal("empty selection should explain why confirmation did not open")
	}
}

func TestConfirmationAcceptsOnlyYOrYes(t *testing.T) {
	tests := []struct {
		name   string
		answer string
	}{
		{name: "y", answer: "y"},
		{name: "yes", answer: "yes"},
		{name: "uppercase", answer: "YES"},
		{name: "mixed case", answer: "YeS"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := selectedPickerState()
			for _, value := range test.answer {
				state.handle(key{kind: keyRune, rune: value})
			}
			state.handle(key{kind: keyEnter})

			if state.mode != modeConfirmed {
				t.Fatalf("answer %q should confirm, got mode %d", test.answer, state.mode)
			}
		})
	}
}

func TestConfirmationRejectsOtherAnswers(t *testing.T) {
	tests := []string{"", "n", "yeah", "yeſ"}

	for _, answer := range tests {
		t.Run(answer, func(t *testing.T) {
			state := selectedPickerState()
			for _, value := range answer {
				state.handle(key{kind: keyRune, rune: value})
			}
			state.handle(key{kind: keyEnter})

			if state.mode != modeCancelled {
				t.Fatalf("answer %q should cancel, got mode %d", answer, state.mode)
			}
		})
	}
}

func TestToggleAllSelectsThenClears(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyRune, rune: 'a'})
	if state.selectedCount() != len(state.candidates) {
		t.Fatalf("a should select all candidates, got %d", state.selectedCount())
	}

	state.handle(key{kind: keyRune, rune: 'a'})
	if state.selectedCount() != 0 {
		t.Fatalf("second a should clear all candidates, got %d", state.selectedCount())
	}
}

func TestQAndEscapeCancelPicker(t *testing.T) {
	tests := []struct {
		name  string
		input key
	}{
		{name: "q", input: key{kind: keyRune, rune: 'q'}},
		{name: "escape", input: key{kind: keyEscape}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newPickerState(testCandidates())
			state.handle(test.input)
			if state.mode != modeCancelled {
				t.Fatalf("%s should cancel the picker, got mode %d", test.name, state.mode)
			}
		})
	}
}

func TestConfirmationSummaryUsesCandidateOrder(t *testing.T) {
	state := newPickerState(testCandidates())
	state.cursor = 1
	state.handle(key{kind: keyRune, rune: 'f'})
	state.cursor = 0
	state.handle(key{kind: keyRune, rune: 'f'})

	names := state.selectedNames()
	if len(names) != 2 || names[0] != "user-svc" || names[1] != "order-svc" {
		t.Fatalf("selected names should use candidate order, got %v", names)
	}
}

func selectedPickerState() *pickerState {
	state := newPickerState(testCandidates())
	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyEnter})
	return state
}

func testCandidates() []Candidate {
	return []Candidate{
		{Name: "user-svc", Path: "/workspace/user-svc", Detail: "Go"},
		{Name: "order-svc", Path: "/workspace/order-svc", Detail: "Java"},
	}
}
