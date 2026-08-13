package cli

import (
	"bytes"
	"testing"
)

func TestPrintWarningBlockUsesCanonicalPlainTextLayout(t *testing.T) {
	var output bytes.Buffer
	printWarningBlock(&output, "Review required.", []string{
		"Plugin: generator",
		"Scope: workspace",
	}, []string{
		"conven doctor",
	})
	want := "Warning: Review required.\n" +
		"  - Plugin: generator\n" +
		"  - Scope: workspace\n" +
		"  => conven doctor\n"
	if output.String() != want {
		t.Fatalf("warning block = %q, want %q", output.String(), want)
	}
}

func TestPrintStartCancelledUsesCanonicalPlainTextLayout(t *testing.T) {
	var output bytes.Buffer
	printStartCancelled(&output, "No services were started.")
	want := "==> Start cancelled\n  - No services were started.\n"
	if output.String() != want {
		t.Fatalf("start cancellation = %q, want %q", output.String(), want)
	}
}
