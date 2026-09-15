package materialize

import (
	"errors"
	"fmt"
	"os"

	"github.com/leo1394/homebrew-conven/internal/rpcconfig"
	"gopkg.in/yaml.v3"
)

type BindingFallback struct {
	Binding string
	EndpointKey string
	EndpointKeyError string
}

type BindingOrigin struct {
	Binding string
	Source string
}

// Check the repository document before connecting. Route validation is conditional
// on Apollo actually missing the key, so an unused repository route cannot mask it.
func ValidateBindingFallbackSource(directory, application string) error {
	path, err := secureGuardPath(directory, application)
	if os.IsNotExist(err) { return nil }
	if err != nil { return err }
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) { return nil }
	if err != nil { return err }
	_, err = bindingDocument(data)
	return err
}

func bindingDocument(data []byte) (*yaml.Node, error) {
	document, err := decodeStrictSingleYAML(data, "RPC binding fallback")
	if err != nil { return nil, errors.New("invalid single-document RPC binding YAML") }
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode { return nil, errors.New("RPC binding YAML must be a mapping") }
	if err := validateYAMLGuardMappings(document, map[*yaml.Node]bool{}, ""); err != nil { return nil, errors.New("RPC binding YAML contains duplicate, merge, or unsupported keys") }
	return cloneBindingNode(document, map[*yaml.Node]bool{})
}

func cloneBindingNode(node *yaml.Node, visiting map[*yaml.Node]bool) (*yaml.Node, error) {
	if node == nil || visiting[node] { return nil, errors.New("RPC binding YAML contains an invalid alias cycle") }
	visiting[node] = true
	defer delete(visiting, node)
	if node.Kind == yaml.AliasNode { return cloneBindingNode(node.Alias, visiting) }
	copy := *node
	copy.Anchor, copy.Alias, copy.Content = "", nil, nil
	for _, child := range node.Content {
		value, err := cloneBindingNode(child, visiting)
		if err != nil { return nil, err }
		copy.Content = append(copy.Content, value)
	}
	return &copy, nil
}

func mergeBindingFallbacks(apollo, repository []byte, fallbacks []BindingFallback) ([]byte, []BindingOrigin, error) {
	document, err := bindingDocument(apollo)
	if err != nil { return nil, nil, fmt.Errorf("Apollo: %w", err) }
	var source *yaml.Node
	if len(repository) > 0 {
		source, err = bindingDocument(repository)
		if err != nil { return nil, nil, fmt.Errorf("repository fallback: %w", err) }
	}
	root := document.Content[0]
	origins := []BindingOrigin{}
	for _, fallback := range fallbacks {
		if fallback.Binding == "" { return nil, nil, errors.New("empty fallback binding") }
		var endpointErr error
		if fallback.EndpointKeyError != "" { endpointErr = errors.New(fallback.EndpointKeyError) }
		position := mappingPosition(root, fallback.Binding)
		if position >= 0 {
			if _, err := rpcconfig.Validate(root.Content[position+1], fallback.EndpointKey, endpointErr); err != nil { return nil, nil, fmt.Errorf("binding %s: Apollo key exists but is invalid: %w", fallback.Binding, err) }
			origins = append(origins, BindingOrigin{fallback.Binding, "Apollo (preserve)"})
			continue
		}
		if source == nil { continue }
		position = mappingPosition(source.Content[0], fallback.Binding)
		if position < 0 { continue }
		value := source.Content[0].Content[position+1]
		active, err := rpcconfig.Validate(value, fallback.EndpointKey, endpointErr)
		if err != nil { return nil, nil, fmt.Errorf("binding %s: Apollo key missing; repository fallback invalid: %w", fallback.Binding, err) }
		if !active { continue }
		copy, err := cloneBindingNode(value, map[*yaml.Node]bool{})
		if err != nil { return nil, nil, err }
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fallback.Binding}, copy)
		origins = append(origins, BindingOrigin{fallback.Binding, "repository fallback (Apollo key missing)"})
	}
	data, err := yaml.Marshal(document)
	return data, origins, err
}
