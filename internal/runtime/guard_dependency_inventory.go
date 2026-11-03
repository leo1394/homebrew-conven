package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/rpcconfig"
	"gopkg.in/yaml.v3"
)

// This is a static inventory, not proof of reachability or authorization. Values
// computed by application code cannot in general be recovered from YAML.
type guardDependencyInventory struct {
	Version int `json:"version"`
	Environment string `json:"environment"`
	Items []guardDependencyItem `json:"items"`
}

type guardDependencyItem struct {
	ID string `json:"id"`
	Consumer string `json:"consumer"`
	Source string `json:"source"`
	Protocol string `json:"protocol,omitempty"`
	Host string `json:"host,omitempty"`
	Port int `json:"port,omitempty"`
	Discovery string `json:"discovery,omitempty"`
	RegistryHost string `json:"registryHost,omitempty"`
	RegistryPort int `json:"registryPort,omitempty"`
	Mode string `json:"mode"`
	Code string `json:"code,omitempty"`
	ConfigPath []string `json:"configPath,omitempty"`
	Format string `json:"format,omitempty"`
}

func (inventory *guardDependencyInventory) add(item guardDependencyItem) {
	identity := strings.Join([]string{item.Consumer, item.Source, item.Protocol, item.Host, strconv.Itoa(item.Port), item.Discovery, item.RegistryHost, strconv.Itoa(item.RegistryPort)}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	item.ID = hex.EncodeToString(digest[:16])
	inventory.Items = append(inventory.Items, item)
}

func (inventory *guardDependencyInventory) sort() {
	sort.Slice(inventory.Items, func(i, j int) bool {
		a, b := inventory.Items[i], inventory.Items[j]
		return a.Consumer + "\x00" + a.Source + "\x00" + a.ID < b.Consumer + "\x00" + b.Source + "\x00" + b.ID
	})
}

// Binding keys come from the source inspector and manifest, not from an approved
// network policy. Keep failures as separate items so one bad client cannot hide
// the remaining services' dependencies.
func (inventory *guardDependencyInventory) inspect(consumer string, data []byte, bindings map[string]string, endpointKey string, endpointErr error) {
	document, err := decodeExternalDependencyYAML(data)
	if err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		inventory.add(guardDependencyItem{Consumer: consumer, Source: "application", Mode: "invalid", Code: "invalid-yaml"})
		return
	}
	root := document.Content[0]
	for binding, mode := range bindings {
		node := externalMappingValue(root, binding)
		item := guardDependencyItem{Consumer: consumer, Source: binding, Protocol: "grpc", ConfigPath: []string{binding}, Format: "rpc-client", Mode: mode}
		active, validateErr := rpcconfig.Validate(node, endpointKey, endpointErr)
		if mode == "disabled" {
			if validateDisabledRPCConfig(node) != nil { item.Mode = "invalid"; item.Code = "disabled-route-active" }
			inventory.add(item)
			continue
		}
		if validateErr != nil || !active {
			item.Mode = "invalid"; item.Code = "invalid-rpc-client"
			if node == nil { item.Code = "missing-rpc-client" }
			inventory.add(item)
			bindings[binding] = "invalid"
			continue
		}
		if mode == "local" { inventory.add(item); continue }
		if externalStringValue(externalMappingValue(node, "discovType")) == "consul" {
			inventory.inspectConsul(consumer, node, []string{binding}, mode)
			continue
		}
		// Direct endpoints are emitted by the scalar walker below. Etcd and
		// resolver targets remain explicit unsupported candidates.
		if externalMappingValue(node, "etcd") != nil {
			item.Mode = "unsupported"; item.Code = "unsupported-discovery"; item.Discovery = "etcd"
			inventory.add(item)
		}
	}
	inventory.walk(consumer, root, nil, bindings, make(map[*yaml.Node]bool))
}

func (inventory *guardDependencyInventory) inspectConsul(consumer string, node *yaml.Node, path []string, mode string) {
	item := guardDependencyItem{Consumer: consumer, Source: strings.Join(path, "."), Protocol: "grpc", Mode: mode, ConfigPath: append(append([]string(nil), path...), "consul", "host"), Format: "consul"}
	consul := externalMappingValue(node, "consul")
	host := externalStringValue(externalMappingValue(consul, "host"))
	port, err := externalPortValue(externalMappingValue(consul, "port"))
	key := externalStringValue(externalMappingValue(consul, "key"))
	if !guardInventoryHost(host) || err != nil || !guardInventoryDiscoveryKey(key) {
		item.Mode = "invalid"; item.Code = "invalid-consul-client"
	} else { item.RegistryHost = host; item.RegistryPort = port; item.Discovery = key }
	inventory.add(item)
}

func (inventory *guardDependencyInventory) walk(consumer string, node *yaml.Node, path []string, bindings map[string]string, stack map[*yaml.Node]bool) {
	if node == nil { return }
	if stack[node] || node.Kind == yaml.AliasNode {
		inventory.add(guardDependencyItem{Consumer: consumer, Source: strings.Join(path, "."), Mode: "unsupported", Code: "yaml-alias", ConfigPath: path})
		return
	}
	stack[node] = true
	defer delete(stack, node)
	if len(path) == 1 {
		if mode := bindings[path[0]]; mode == "local" || mode == "disabled" || mode == "invalid" { return }
	}
	if guardInventoryNonOutboundPath(path) { return }
	if node.Kind == yaml.MappingNode {
		mappedEndpoint := false
		if len(path)>0 && guardInventoryDatabaseProtocol(path[len(path)-1:])=="redis" {
			endpoint:=false
			for i:=0;i+1<len(node.Content);i+=2 { if guardInventoryEndpointField(strings.ToLower(node.Content[i].Value)) { endpoint=true } }
			if !endpoint { inventory.add(guardDependencyItem{Consumer:consumer,Source:strings.Join(path,"."),Protocol:"redis",Mode:"invalid",Code:"missing-redis-endpoint",ConfigPath:path}) }
		}
		if len(path) > 0 && externalStringValue(externalMappingValue(node, "discovType")) == "consul" {
			if _, declared := bindings[strings.Join(path, ".")]; !declared { inventory.inspectConsul(consumer, node, path, "remote") }
			return
		}
		if len(path) > 0 {
			protocol := guardInventoryDatabaseProtocol(path)
			// The mq/topics schema is consumed by KafkaManager. Disabling
			// readers does not disable writers; retain the broker as Kafka.
			if len(path) == 2 && strings.EqualFold(path[0], "mq") {
				if _, err := strconv.Atoi(path[1]); err == nil { protocol = "kafka" }
			}
			if protocol != "" || (externalMappingValue(node, "host") != nil && externalMappingValue(node, "port") != nil) {
				host := externalStringValue(externalMappingValue(node, "host"))
				if host != "" {
					port, err := externalPortValue(externalMappingValue(node, "port"))
					item := guardDependencyItem{Consumer: consumer, Source: strings.Join(path, ".") + ".host", Protocol: protocol, ConfigPath: append(append([]string(nil), path...), "host"), Format: "host-port-map", Mode: "remote"}
					if !guardInventoryHost(host) || err != nil { item.Mode = "invalid"; item.Code = "invalid-database-endpoint" } else { item.Host = host; item.Port = port }
					if protocol == "" && item.Mode != "invalid" { item.Mode = "unsupported"; item.Code = "unknown-endpoint-protocol" }
					inventory.add(item)
					mappedEndpoint = true
				}
			}
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if len(path) == 0 && key == "consul" {
				// RpcServerConf registers only for discovType=consul. A
				// stale server registration block is not a client route.
				discovery := externalMappingValue(node, "discovType")
				if discovery != nil && discovery.Kind == yaml.ScalarNode && discovery.Tag == "!!str" && discovery.Value != "consul" { continue }
			}
			if mappedEndpoint && (key == "host" || key == "port") { continue }
			child := append(append([]string(nil), path...), key)
			if key == "<<" || seen[key] {
				inventory.add(guardDependencyItem{Consumer: consumer, Source: strings.Join(child, "."), Mode: "invalid", Code: "ambiguous-yaml", ConfigPath: child})
				continue
			}
			seen[key] = true
			inventory.walk(consumer, node.Content[i+1], child, bindings, stack)
		}
		return
	}
	if node.Kind == yaml.SequenceNode {
		for i, child := range node.Content { inventory.walk(consumer, child, append(append([]string(nil), path...), strconv.Itoa(i)), bindings, stack) }
		return
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || len(path) == 0 { return }
	key := strings.ToLower(path[len(path)-1])
	if key == "password" || key == "token" || key == "secret" { return }
	endpointKey := key
	if _, err := strconv.Atoi(key); err == nil && len(path) > 1 { endpointKey = strings.ToLower(path[len(path)-2]) }
	if !guardInventoryEndpointField(endpointKey) { return }
	// Listen/registration addresses are not outbound dependencies.
	if len(path) == 1 && (key == "host" || key == "address" || key == "listenon" || key == "listen" || key == "ip") { return }
	for _, part := range path[:len(path)-1] {
		if strings.EqualFold(part, "server") || strings.EqualFold(part, "listener") || strings.EqualFold(part, "registration") { return }
	}
	value := strings.TrimSpace(node.Value)
	item := guardDependencyItem{Consumer: consumer, Source: strings.Join(path, "."), ConfigPath: path, Mode: "remote"}
	if strings.Contains(value, "://") {
		item.Format = "url"
		parsed, err := url.Parse(value)
		if err != nil || parsed.Hostname() == "" || !guardInventoryHost(parsed.Hostname()) {
			item.Mode = "invalid"; item.Code = "invalid-url"
		} else {
			item.Host = parsed.Hostname()
			item.Protocol = strings.ToLower(parsed.Scheme)
			if item.Protocol == "postgresql" { item.Protocol = "postgres" }
			defaults := map[string]int{"http": 80, "https": 443, "postgres": 5432, "mysql": 3306, "redis": 6379, "rediss": 6379, "mongodb": 27017}
			item.Port = defaults[item.Protocol]
			if item.Port == 0 { item.Mode = "unsupported"; item.Code = "unsupported-url-scheme" }
			if parsed.Port() != "" { item.Port, err = strconv.Atoi(parsed.Port()) }
			if err != nil || (parsed.Port() != "" && item.Port < 1) || item.Port > 65535 { item.Mode = "invalid"; item.Code = "invalid-port"; item.Port = 0 }
		}
	} else if start := strings.Index(value, "@tcp("); start >= 0 {
		item.Protocol = "mysql"; item.Format = "mysql-dsn"
		end := strings.Index(value[start+5:], ")")
		if end < 0 { item.Mode = "invalid"; item.Code = "invalid-dsn" } else { item.Host, item.Port = guardInventoryAddress(value[start+5:start+5+end]); if item.Host == "" { item.Mode = "invalid"; item.Code = "invalid-dsn" } }
	} else {
		item.Host, item.Port = guardInventoryAddress(value)
		if item.Host == "" {
			if value == "" {
				if guardInventoryDatabaseProtocol(path)=="redis" { item.Protocol="redis"; item.Mode="invalid"; item.Code="missing-redis-endpoint"; inventory.add(item) }
				return
			}
			item.Mode = "unsupported"; item.Code = "unrecognized-endpoint"
		} else {
			item.Mode = "unsupported"; item.Code = "unknown-endpoint-protocol"; item.Format = "host-port"
			if protocol := guardInventoryDatabaseProtocol(path); protocol != "" { item.Mode = "remote"; item.Protocol = protocol; item.Code = "" } else if len(path) > 1 && bindings[path[0]] != "" { item.Mode = "remote"; item.Protocol = "grpc"; item.Code = "" }
		}
	}
	inventory.add(item)
}

func guardInventoryDiscoveryKey(key string) bool {
	if len(key) == 0 || len(key) > 256 { return false }
	for _, character := range key {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' { continue }
		return false
	}
	return true
}

func guardInventoryDatabaseProtocol(path []string) string {
	for _, part := range path {
		switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(part,"_",""),"-","")) {
		case "postgresql": return "postgres"
		case "rdm", "redisconfig": return "redis"
		case "mongo": return "mongodb"
		case "mysql", "postgres", "redis", "mongodb": return strings.ToLower(part)
		}
	}
	return ""
}

func guardInventoryEndpointField(key string) bool {
	for _, suffix := range []string{"addr", "url", "uri", "endpoint", "endpoints", "endpints", "dsn", "target", "address", "host", "hosts", "connectionstring"} {
		if strings.HasSuffix(key, suffix) { return true }
	}
	return false
}

// These declarative fields describe public output or inbound listeners, not
// connections initiated by the application. Arbitrary string content alone is
// not evidence that a URL is an outbound dependency.
func guardInventoryNonOutboundPath(path []string) bool {
	// Source-verified response fields: GetAppConfig/GetRedResetConfig copy
	// these to client responses; CNPG viewer builders create returned links.
	// Match complete schema paths, not arbitrary H5/QA/report URL names.
	switch strings.ToLower(strings.Join(path, ".")) {
	case "apph5.qaurl", "reporth5.planreporturl", "reporth5.taskreporturl", "redreset.login.loginurl", "cnpgstorekpidetailpageurl": return true
	}
	for _, part := range path {
		key := strings.ToLower(part)
		for _, marker := range []string{"callback", "redirect", "public", "response", "banner", "metadata", "advertise"} {
			if strings.Contains(key, marker) { return true }
		}
		switch key {
		case "server", "listener", "registration", "listen", "listenon": return true
		}
	}
	return false
}

func guardInventoryHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\\@?#$\t\n\r") { return false }
	if strings.Contains(host, ":") { return net.ParseIP(host) != nil }
	return true
}

func guardInventoryAddress(value string) (string, int) {
	host, portText, err := net.SplitHostPort(value)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || !guardInventoryHost(host) || port < 1 || port > 65535 { return "", 0 }
	return host, port
}
