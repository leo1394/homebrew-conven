package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"gopkg.in/yaml.v3"
)

const (
	externalDependencyResponseLimit = 1 << 20
	externalDependencyTimeout = 3 * time.Second
	externalDependencyAttempts = 3
	externalDependencyRetryDelay = 200 * time.Millisecond
)

type ExternalConsulDependency struct {
	Owner string
	Path  string
	Host  string
	Port  int
	Key   string
}

func (dependency ExternalConsulDependency) Reference() string {
	if dependency.Path == "" {
		return dependency.Owner
	}
	return dependency.Owner + "." + dependency.Path
}

func detectExternalConsulDependencies(service PlannedService, kind string) ([]ExternalConsulDependency, error) {
	dependencies, _, err := inspectRuntimeContractExternalDependencies(service, kind)
	return dependencies, err
}

func inspectExternalConsulDependencies(service PlannedService, kind string) ([]ExternalConsulDependency, error) {
	applicationPath, err := externalDependencyApplicationPath(service.Config.Plan)
	if err != nil {
		return nil, fmt.Errorf("inspect %s external dependencies: %w", service.Name, err)
	}
	data, err := os.ReadFile(applicationPath)
	if err != nil {
		return nil, fmt.Errorf("read %s materialized application for external dependency inspection: %w", service.Name, err)
	}
	document, err := decodeExternalDependencyYAML(data)
	if err != nil {
		return nil, fmt.Errorf("decode %s materialized application for external dependency inspection: %w", service.Name, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s materialized application must contain one mapping document", service.Name)
	}
	localRoutes := make(map[string]bool)
	for _, route := range service.Config.Routes {
		if route.Local {
			localRoutes[strings.TrimSpace(route.Binding)] = true
		}
	}
	dependencies := make([]ExternalConsulDependency, 0)
	if err := walkExternalDependencyYAML(document.Content[0], "", service.Name, kind, localRoutes, &dependencies, make(map[*yaml.Node]bool)); err != nil {
		return nil, err
	}
	sort.Slice(dependencies, func(left int, right int) bool {
		leftKey := dependencies[left].Reference() + "\x00" + dependencies[left].Host + "\x00" + strconv.Itoa(dependencies[left].Port) + "\x00" + dependencies[left].Key
		rightKey := dependencies[right].Reference() + "\x00" + dependencies[right].Host + "\x00" + strconv.Itoa(dependencies[right].Port) + "\x00" + dependencies[right].Key
		return leftKey < rightKey
	})
	return dependencies, nil
}

func externalDependencyApplicationPath(plan materialize.Plan) (string, error) {
	if strings.TrimSpace(plan.Application) == "" {
		return "", errors.New("materialized application path is empty")
	}
	if filepath.IsAbs(plan.Application) {
		return "", errors.New("materialized application path must be relative")
	}
	target := filepath.Clean(plan.TargetDir)
	path := filepath.Clean(filepath.Join(target, plan.Application))
	if !pathWithinDirectory(target, path) {
		return "", errors.New("materialized application path escapes the config directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("materialized application must be a real file")
	}
	return path, nil
}

func decodeExternalDependencyYAML(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	document := &yaml.Node{}
	if err := decoder.Decode(document); err != nil {
		return nil, err
	}
	extra := &yaml.Node{}
	if err := decoder.Decode(extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("materialized application must contain exactly one YAML document")
	}
	return document, nil
}

func walkExternalDependencyYAML(node *yaml.Node, path string, owner string, kind string, localRoutes map[string]bool, dependencies *[]ExternalConsulDependency, stack map[*yaml.Node]bool) error {
	if node == nil {
		return nil
	}
	if stack[node] {
		return fmt.Errorf("service %s materialized application contains a YAML alias cycle at %s", owner, externalDependencyPathLabel(path))
	}
	stack[node] = true
	defer delete(stack, node)
	if node.Kind == yaml.AliasNode {
		return walkExternalDependencyYAML(node.Alias, path, owner, kind, localRoutes, dependencies, stack)
	}
	if err := validateExternalDependencyYAMLTag(node, path, owner); err != nil {
		return err
	}
	switch node.Kind {
	case yaml.MappingNode:
		seenKeys := make(map[string]bool)
		seenKeyText := make(map[string]bool)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind == yaml.ScalarNode && (key.Value == "<<" || key.Tag == "!!merge") {
				return fmt.Errorf("service %s materialized application uses an unsupported YAML merge key at %s", owner, externalDependencyPathLabel(path))
			}
			if key.Kind != yaml.ScalarNode || (key.Tag != "!!str" && key.Tag != "!!int") {
				return fmt.Errorf("service %s materialized application uses an unsupported YAML mapping key at %s", owner, externalDependencyPathLabel(path))
			}
			identifier, textIdentifier, err := externalYAMLKeyIdentifiers(key)
			if err != nil {
				return fmt.Errorf("service %s materialized application uses an unsupported YAML mapping key at %s", owner, externalDependencyPathLabel(path))
			}
			if seenKeys[identifier] || seenKeyText[textIdentifier] {
				return fmt.Errorf("service %s materialized application uses duplicate YAML key %q at %s", owner, key.Value, externalDependencyPathLabel(path))
			}
			seenKeys[identifier] = true
			seenKeyText[textIdentifier] = true
		}
		discovType := externalMappingValue(node, "discovType")
		if externalStringValue(discovType) == "consul" && path == "" && (kind == "rpc" || kind == "http") {
			return fmt.Errorf("service %s kind %s still has remote Consul registration enabled in the final materialized config", owner, kind)
		}
		if externalStringValue(discovType) == "consul" {
			if localRoutes[path] {
				return fmt.Errorf("service %s local route %s still uses Consul in the final materialized config", owner, externalDependencyPathLabel(path))
			}
			consul := externalMappingValue(node, "consul")
			if consul == nil || consul.Kind != yaml.MappingNode {
				return fmt.Errorf("service %s active Consul client %s requires a consul mapping", owner, externalDependencyPathLabel(path))
			}
			host := strings.TrimSpace(externalStringValue(externalMappingValue(consul, "host")))
			if host == "" {
				return fmt.Errorf("service %s active Consul client %s requires consul.host", owner, externalDependencyPathLabel(path))
			}
			port, err := externalPortValue(externalMappingValue(consul, "port"))
			if err != nil {
				return fmt.Errorf("service %s active Consul client %s has invalid consul.port: %w", owner, externalDependencyPathLabel(path), err)
			}
			key := strings.TrimSpace(externalStringValue(externalMappingValue(consul, "key")))
			if key == "" {
				return fmt.Errorf("service %s active Consul client %s requires consul.key", owner, externalDependencyPathLabel(path))
			}
			*dependencies = append(*dependencies, ExternalConsulDependency{
				Owner: owner,
				Path:  path,
				Host:  host,
				Port:  port,
				Key:   key,
			})
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				continue
			}
			childPath := key.Value
			if path != "" {
				childPath = path + "." + key.Value
			}
			if err := walkExternalDependencyYAML(node.Content[index+1], childPath, owner, kind, localRoutes, dependencies, stack); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if err := walkExternalDependencyYAML(child, childPath, owner, kind, localRoutes, dependencies, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateExternalDependencyYAMLTag(node *yaml.Node, path string, owner string) error {
	allowed := false
	switch node.Kind {
	case yaml.MappingNode:
		allowed = node.Tag == "!!map"
	case yaml.SequenceNode:
		allowed = node.Tag == "!!seq"
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!int", "!!bool", "!!float", "!!null", "!!timestamp":
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("service %s materialized application uses unsupported YAML tag %q at %s", owner, node.Tag, externalDependencyPathLabel(path))
	}
	return nil
}

func externalMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		candidate := mapping.Content[index]
		if candidate.Kind == yaml.ScalarNode && candidate.Tag == "!!str" && candidate.Value == key {
			value := mapping.Content[index+1]
			if value.Kind == yaml.AliasNode {
				return value.Alias
			}
			return value
		}
	}
	return nil
}

func externalYAMLKeyIdentifiers(node *yaml.Node) (string, string, error) {
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s\x00%#v", node.Tag, decoded), fmt.Sprint(decoded), nil
}

func externalStringValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return ""
	}
	return node.Value
}

func externalPortValue(node *yaml.Node) (int, error) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return 0, errors.New("value is missing")
	}
	var port int
	if err := node.Decode(&port); err != nil {
		return 0, errors.New("value must be an integer")
	}
	if port < 1 || port > 65535 {
		return 0, errors.New("value must be between 1 and 65535")
	}
	return port, nil
}

func externalDependencyPathLabel(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}

func preflightExternalConsulDependencies(ctx context.Context, dependencies []ExternalConsulDependency) error {
	if len(dependencies) == 0 {
		return nil
	}
	client := &http.Client{
		Timeout: externalDependencyTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	type dependencyTarget struct {
		Host string
		Port int
		Key  string
	}
	targets := make(map[dependencyTarget][]ExternalConsulDependency)
	for _, dependency := range dependencies {
		target := dependencyTarget{Host: dependency.Host, Port: dependency.Port, Key: dependency.Key}
		targets[target] = append(targets[target], dependency)
	}
	ordered := make([]dependencyTarget, 0, len(targets))
	for target := range targets {
		ordered = append(ordered, target)
	}
	sort.Slice(ordered, func(left int, right int) bool {
		leftKey := ordered[left].Host + "\x00" + strconv.Itoa(ordered[left].Port) + "\x00" + ordered[left].Key
		rightKey := ordered[right].Host + "\x00" + strconv.Itoa(ordered[right].Port) + "\x00" + ordered[right].Key
		return leftKey < rightKey
	})
	problems := make([]string, 0)
	for _, target := range ordered {
		passing, err := externalConsulServicePassingWithRetry(ctx, client, target.Host, target.Port, target.Key)
		if err != nil {
			for _, dependency := range targets[target] {
				problems = append(problems, fmt.Sprintf("%s -> %s via %s: %v", dependency.Reference(), target.Key, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)), err))
			}
			continue
		}
		if passing {
			continue
		}
		for _, dependency := range targets[target] {
			problems = append(problems, fmt.Sprintf("%s -> %s via %s has no passing instances", dependency.Reference(), target.Key, net.JoinHostPort(target.Host, strconv.Itoa(target.Port))))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("external Consul dependency preflight failed: " + strings.Join(problems, "; "))
	}
	return nil
}

func externalConsulServicePassingWithRetry(ctx context.Context, client *http.Client, host string, port int, key string) (bool, error) {
	var lastErr error
	for attempt := 1; attempt <= externalDependencyAttempts; attempt++ {
		passing, err := externalConsulServicePassing(ctx, client, host, port, key)
		if err == nil {
			return passing, nil
		}
		lastErr = err
		if ctx.Err() != nil || attempt == externalDependencyAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * externalDependencyRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	return false, lastErr
}

func externalConsulServicePassing(ctx context.Context, client *http.Client, host string, port int, key string) (bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, externalDependencyTimeout)
	defer cancel()
	endpoint := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/v1/health/service/" + url.PathEscape(key) + "?passing=true"
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, externalDependencyResponseLimit+1))
	if err != nil {
		return false, err
	}
	if len(body) > externalDependencyResponseLimit {
		return false, fmt.Errorf("Consul response exceeds %d bytes", externalDependencyResponseLimit)
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Consul returned HTTP %s", response.Status)
	}
	var instances []json.RawMessage
	if err := json.Unmarshal(body, &instances); err != nil {
		return false, errors.New("Consul returned invalid JSON")
	}
	return len(instances) > 0, nil
}
