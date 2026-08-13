package cli

import (
	"fmt"
	"io"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func printWarningBlock(output io.Writer, summary string, details []string, actions []string) {
	terminal.PrintWarningBlock(output, summary, details, actions)
}

func printStartCancelled(output io.Writer, detail string) {
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Start cancelled"))
	fmt.Fprintln(output, style.Detail(detail))
}
