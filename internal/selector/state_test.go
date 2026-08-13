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

func TestHiddenMovementKeysRemainAvailable(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyRune, rune: 'j'})
	if state.cursor != 1 {
		t.Fatalf("j should move down, got cursor %d", state.cursor)
	}
	state.handle(key{kind: keyRune, rune: 'k'})
	if state.cursor != 0 {
		t.Fatalf("k should move up, got cursor %d", state.cursor)
	}
	state.handle(key{kind: keyDown})
	if state.cursor != 1 {
		t.Fatalf("down arrow should move down, got cursor %d", state.cursor)
	}
	state.handle(key{kind: keyUp})
	if state.cursor != 0 {
		t.Fatalf("up arrow should move up, got cursor %d", state.cursor)
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

func TestConfirmationAcceptsNOrNoAsCancellation(t *testing.T) {
	tests := []string{"n", "no", "N", "NO", "No"}

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

func TestInvalidConfirmationAnswerClearsInputAndRetries(t *testing.T) {
	for _, answer := range []string{"", "q", "yeah", "yeſ"} {
		t.Run(answer, func(t *testing.T) {
			state := selectedPickerState()
			for _, value := range answer {
				state.handle(key{kind: keyRune, rune: value})
			}
			state.handle(key{kind: keyEnter})

			if state.mode != modeConfirming {
				t.Fatalf("invalid answer %q should remain in confirmation, got mode %d", answer, state.mode)
			}
			if len(state.confirmation) != 0 {
				t.Fatalf("invalid answer %q was not cleared: %q", answer, state.confirmation)
			}
		})
	}
}

func TestToggleAllSelectsThenClears(t *testing.T) {
	state := newPickerState(testCandidates())

	state.handle(key{kind: keyRune, rune: 'a'})
	if state.selectedCount() != 0 {
		t.Fatalf("lowercase a should not change the selection, got %d", state.selectedCount())
	}

	state.handle(key{kind: keyRune, rune: 'A'})
	if state.selectedCount() != len(state.candidates) {
		t.Fatalf("A should select all candidates, got %d", state.selectedCount())
	}

	state.handle(key{kind: keyRune, rune: 'A'})
	if state.selectedCount() != 0 {
		t.Fatalf("second A should clear all candidates, got %d", state.selectedCount())
	}
}

func TestSingleSelectionReplacesThePreviousCandidate(t *testing.T) {
	state := newSinglePickerState(testCandidates(), Prompt{EmptySelectionNotice: "Select one."})

	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyDown})
	state.handle(key{kind: keyRune, rune: 'f'})

	if state.selected[0] {
		t.Fatal("selecting the second candidate should clear the first candidate")
	}
	if !state.selected[1] {
		t.Fatal("the second candidate was not selected")
	}
	if state.selectedCount() != 1 {
		t.Fatalf("single picker selected count = %d, want 1", state.selectedCount())
	}
}

func TestSingleSelectionCanClearTheCurrentCandidate(t *testing.T) {
	state := newSinglePickerState(testCandidates(), Prompt{EmptySelectionNotice: "Select one."})

	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyRune, rune: 'f'})

	if state.selectedCount() != 0 {
		t.Fatalf("second f should clear the current single selection, got %d", state.selectedCount())
	}
	state.handle(key{kind: keyEnter})
	if state.mode != modePicking || state.notice != "Select one." {
		t.Fatalf("empty single selection mode=%d notice=%q", state.mode, state.notice)
	}
}

func TestSingleSelectionIgnoresToggleAll(t *testing.T) {
	state := newSinglePickerState(testCandidates(), Prompt{EmptySelectionNotice: "Select one."})

	state.handle(key{kind: keyRune, rune: 'A'})

	if state.selectedCount() != 0 {
		t.Fatalf("single picker A selected %d candidates", state.selectedCount())
	}
}

func TestSingleSelectionUpperFReplacesAndMoves(t *testing.T) {
	state := newSinglePickerState(testCandidates(), Prompt{EmptySelectionNotice: "Select one."})
	state.handle(key{kind: keyRune, rune: 'f'})
	state.handle(key{kind: keyRune, rune: 'F'})

	if state.selectedCount() != 0 || state.cursor != 1 {
		t.Fatalf("F on selected candidate should clear it and move: selected=%d cursor=%d", state.selectedCount(), state.cursor)
	}
	state.handle(key{kind: keyRune, rune: 'F'})
	if state.selectedCount() != 1 || !state.selected[1] {
		t.Fatalf("F should select the next candidate without exceeding one selection: selected=%v", state.selected)
	}
}

func TestSelectedCandidatesReturnCompleteCandidate(t *testing.T) {
	candidates := []Candidate{{Name: "generate-policy", Path: "/plugins/generate-policy.py", Detail: "Python", Tag: "global"}}
	state := newSinglePickerState(candidates, Prompt{EmptySelectionNotice: "Select one."})
	state.handle(key{kind: keyRune, rune: 'f'})

	selected := state.selectedCandidates()
	if len(selected) != 1 || selected[0] != candidates[0] {
		t.Fatalf("selected candidates = %#v, want %#v", selected, candidates)
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
