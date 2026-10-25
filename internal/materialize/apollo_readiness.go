package materialize

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ApolloReadinessAddress resolves the TCP endpoint actually used by the source adapter.
func ApolloReadinessAddress(plan Plan) (string, error) {
	path, err := secureGuardPath(plan.SourceDir, plan.Bootstrap)
	if err != nil { return "", err }
	data, err := os.ReadFile(path)
	if err != nil { return "", err }
	document, err := decodeStrictSingleYAML(data, "Apollo bootstrap")
	if err != nil { return "", err }
	var bootstrap apolloBootstrap
	if err := document.Decode(&bootstrap); err != nil { return "", err }
	endpoint := strings.TrimSpace(bootstrap.IP)
	if endpoint == "" { return "", fmt.Errorf("Apollo bootstrap requires ip") }
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid Apollo endpoint")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" { port = "443" }
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 { return "", fmt.Errorf("invalid Apollo endpoint port") }
	return net.JoinHostPort(strings.ToLower(parsed.Hostname()), port), nil
}
