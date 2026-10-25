package runtime

import (
	"reflect"
	"testing"
)

func TestGuardOfflineGoPreparationOnlyReplacesExactPrefetch(t *testing.T) {
	input := []string{"go", "mod", "download"}
	got := guardOfflineGoPrepare(input, []string{"go", "build", "."})
	if !reflect.DeepEqual(got, []string{"go", "list", "-mod=readonly", "-deps", "-f={{if .Error}}{{.Error.Err}}{{end}}", "."}) || input[2] != "download" { t.Fatal("invalid offline cache validation") }
	for _, prepare := range [][]string{nil, {"sh", "prepare.sh"}, {"go", "mod", "download", "example.org/module"}, {"go", "generate", "./..."}} {
		if !reflect.DeepEqual(guardOfflineGoPrepare(prepare, []string{"go", "build", "."}), prepare) { t.Fatal("changed custom preparation") }
	}
	if !reflect.DeepEqual(guardOfflineGoPrepare(input, []string{"custom-build"}), input) { t.Fatal("changed unrelated build") }
	if !reflect.DeepEqual(guardOfflineGoPrepare(input, []string{"go", "build", "-tags=custom", "."}), input) { t.Fatal("changed an unrecognized build contract") }
}
