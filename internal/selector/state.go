package selector

type Candidate struct {
	Name   string
	Path   string
	Detail string
}

type pickerMode uint8

const (
	modePicking pickerMode = iota
	modeConfirming
	modeConfirmed
	modeCancelled
)

type keyKind uint8

const (
	keyRune keyKind = iota
	keyUp
	keyDown
	keyEnter
	keyEscape
	keyBackspace
)

type key struct {
	kind keyKind
	rune rune
}

type pickerState struct {
	candidates   []Candidate
	cursor       int
	selected     []bool
	mode         pickerMode
	confirmation []rune
	notice       string
}

func newPickerState(candidates []Candidate) *pickerState {
	return &pickerState{
		candidates: candidates,
		selected:   make([]bool, len(candidates)),
		mode:       modePicking,
	}
}

func (s *pickerState) handle(input key) {
	if s.mode == modeConfirming {
		s.handleConfirmation(input)
		return
	}
	if s.mode != modePicking {
		return
	}

	switch input.kind {
	case keyUp:
		s.move(-1)
	case keyDown:
		s.move(1)
	case keyEnter:
		if s.selectedCount() == 0 {
			s.notice = "Select at least one service before confirming."
			return
		}
		s.mode = modeConfirming
		s.notice = ""
	case keyEscape:
		s.mode = modeCancelled
	case keyRune:
		s.handlePickerRune(input.rune)
	}
}

func (s *pickerState) handlePickerRune(value rune) {
	switch value {
	case 'j':
		s.move(1)
	case 'k':
		s.move(-1)
	case 'f':
		s.toggleCurrent()
	case 'F':
		s.toggleCurrent()
		s.move(1)
	case 'a':
		s.toggleAll()
	case 'q':
		s.mode = modeCancelled
	}
}

func (s *pickerState) handleConfirmation(input key) {
	switch input.kind {
	case keyEscape:
		s.mode = modeCancelled
	case keyBackspace:
		if len(s.confirmation) > 0 {
			s.confirmation = s.confirmation[:len(s.confirmation)-1]
		}
	case keyEnter:
		answer := string(s.confirmation)
		if asciiEqualFold(answer, "y") || asciiEqualFold(answer, "yes") {
			s.mode = modeConfirmed
			return
		}
		s.mode = modeCancelled
	case keyRune:
		if input.rune == 'q' {
			s.mode = modeCancelled
			return
		}
		if input.rune >= ' ' && input.rune != 0x7f {
			s.confirmation = append(s.confirmation, input.rune)
		}
	}
}

func asciiEqualFold(value string, expected string) bool {
	if len(value) != len(expected) {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if character != expected[index] {
			return false
		}
	}
	return true
}

func (s *pickerState) move(delta int) {
	if len(s.candidates) == 0 {
		return
	}
	next := s.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(s.candidates) {
		next = len(s.candidates) - 1
	}
	s.cursor = next
	s.notice = ""
}

func (s *pickerState) toggleCurrent() {
	if len(s.candidates) == 0 {
		return
	}
	s.selected[s.cursor] = !s.selected[s.cursor]
	s.notice = ""
}

func (s *pickerState) toggleAll() {
	selectAll := s.selectedCount() != len(s.selected)
	for index := range s.selected {
		s.selected[index] = selectAll
	}
	s.notice = ""
}

func (s *pickerState) selectedCount() int {
	count := 0
	for _, selected := range s.selected {
		if selected {
			count++
		}
	}
	return count
}

func (s *pickerState) selectedNames() []string {
	names := make([]string, 0, s.selectedCount())
	for index, selected := range s.selected {
		if selected {
			names = append(names, s.candidates[index].Name)
		}
	}
	return names
}
