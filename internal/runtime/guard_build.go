package runtime

// An offline Guard cannot prefetch every module in the graph. For the exact Go
// prefetch contract only, resolve the build's dependency graph instead; the subsequent Go
// build still resolves and verifies every dependency it actually uses and fails
// if one is missing. Custom preparation commands remain untouched.
func guardOfflineGoPrepare(prepare, build []string) []string {
	if len(prepare) == 3 && prepare[0] == "go" && prepare[1] == "mod" && prepare[2] == "download" && len(build) > 1 && build[0] == "go" && build[1] == "build" {
		pkg := "."
		switch {
		case len(build) == 2:
		case len(build) == 3 && len(build[2]) > 0 && build[2][0] != '-': pkg = build[2]
		case len(build) == 5 && build[2] == "-o" && len(build[4]) > 0 && build[4][0] != '-': pkg = build[4]
		default: return prepare
		}
		return []string{"go", "list", "-mod=readonly", "-deps", "-f={{if .Error}}{{.Error.Err}}{{end}}", pkg}
	}
	return prepare
}
