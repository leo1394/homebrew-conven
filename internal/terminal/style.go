package terminal

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	reset      = "\x1b[0m"
	boldBlue   = "\x1b[1;34m"
	boldCyan   = "\x1b[1;36m"
	boldYellow = "\x1b[1;33m"
	boldRed    = "\x1b[1;31m"
	boldGreen  = "\x1b[1;32m"
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
	return style.wrap(boldBlue, value)
}

func (style Style) Identifier(value string) string {
	return style.wrap(boldCyan, value)
}

func (style Style) Warning(value string) string {
	return style.wrap(boldYellow, value)
}

func (style Style) Failure(value string) string {
	return style.wrap(boldRed, value)
}

func (style Style) Success(value string) string {
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
