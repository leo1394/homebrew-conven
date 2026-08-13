package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	reset                       = "\x1b[0m"
	boldCyan                    = "\x1b[1;36m"
	boldYellow                  = "\x1b[1;33m"
	boldRed                     = "\x1b[1;31m"
	boldGreen                   = "\x1b[1;32m"
	selectedRedBackground       = "\x1b[1;37;41m"
)

type Style struct {
	enabled bool
}

func New(output io.Writer) Style {
	return newStyle(output, term.IsTerminal)
}

func newStyle(output io.Writer, isTerminal func(int) bool) Style {
	file, ok := output.(*os.File)
	if !ok || !isTerminal(int(file.Fd())) {
		return Style{}
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return Style{}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return Style{}
	}
	return Style{enabled: true}
}

func (style Style) Label(value string) string {
	return value
}

func (style Style) Stage(value string) string {
	return style.wrap(boldGreen, "==> "+value)
}

func (style Style) Detail(value string) string {
	return "  - " + value
}

func (style Style) Action(value string) string {
	return "  => " + value
}

func (style Style) Identifier(value string) string {
	return style.wrap(boldCyan, value)
}

func (style Style) Warning(value string) string {
	return style.wrap(boldYellow, value)
}

// PrintWarningBlock writes one highlighted summary followed by plain details and actions.
func PrintWarningBlock(output io.Writer, summary string, details []string, actions []string) {
	style := New(output)
	fmt.Fprintln(output, style.Warning("Warning: "+summary))
	for _, detail := range details {
		fmt.Fprintln(output, style.Detail(detail))
	}
	for _, action := range actions {
		fmt.Fprintln(output, style.Action(action))
	}
}

func (style Style) Failure(value string) string {
	return style.wrap(boldRed, value)
}

func (style Style) Success(value string) string {
	return style.wrap(boldGreen, value)
}

func (style Style) Selection(value string, active bool) string {
	if active {
		return style.wrap(selectedRedBackground, value)
	}
	return style.wrap(boldGreen, value)
}

func (style Style) Identifiers(values []string, separator string) string {
	styled := make([]string, 0, len(values))
	for _, value := range values {
		styled = append(styled, style.Identifier(value))
	}
	return strings.Join(styled, separator)
}

func (style Style) wrap(code string, value string) string {
	if !style.enabled || value == "" {
		return value
	}
	return code + value + reset
}
