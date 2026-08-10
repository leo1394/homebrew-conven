package selector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
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

func TestRenderConfirmationIncludesLoomingServices(t *testing.T) {
	state := selectedPickerState()
	var output bytes.Buffer

	if err := render(&output, state, 100, 24); err != nil {
		t.Fatalf("render returned an error: %v", err)
	}

	want := "Looming local services: user-svc"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("confirmation output %q does not contain %q", output.String(), want)
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
