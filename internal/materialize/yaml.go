package materialize

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func validatePatchPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("YAML path is empty")
	}
	for _, segment := range strings.Split(path, ".") {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("YAML path %q contains an empty segment", path)
		}
	}
	return nil
}

func applyYAMLPatch(path string, dotPath string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	document, err := decodeSingleYAML(data)
	if err != nil {
		return err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("YAML patch requires a mapping document")
	}
	valueNode, err := encodeYAMLValue(value)
	if err != nil {
		return err
	}
	segments := strings.Split(dotPath, ".")
	current := document.Content[0]
	for index, segment := range segments {
		segment = strings.TrimSpace(segment)
		last := index == len(segments)-1
		position := mappingPosition(current, segment)
		if last {
			if position >= 0 {
				current.Content[position+1] = valueNode
			} else {
				current.Content = append(current.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
					valueNode,
				)
			}
			break
		}
		if position >= 0 {
			next := current.Content[position+1]
			if next.Kind != yaml.MappingNode {
				return fmt.Errorf("YAML path segment %q is not a mapping", strings.Join(segments[:index+1], "."))
			}
			current = next
			continue
		}
		next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		current.Content = append(current.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
			next,
		)
		current = next
	}
	buffer := &bytes.Buffer{}
	encoder := yaml.NewEncoder(buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode patched YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close patched YAML encoder: %w", err)
	}
	return writePrivateFile(path, buffer.Bytes())
}

func requireExistingYAMLGuard(path string, dotPath string) error {
	document, err := readYAMLGuardDocument(path)
	if err != nil {
		return err
	}
	value, found, err := yamlValueAtPath(document, dotPath)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("YAML guard path %q does not exist", dotPath)
	}
	value = dereferenceYAMLAlias(value)
	if value == nil || value.Kind != yaml.ScalarNode {
		return fmt.Errorf("YAML guard path %q must reference a scalar", dotPath)
	}
	return nil
}

func applyYAMLGuard(path string, dotPath string, value any, allowCreate bool) error {
	document, err := readYAMLGuardDocument(path)
	if err != nil {
		return err
	}
	valueNode, err := encodeYAMLGuardValue(value)
	if err != nil {
		return err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("YAML guard requires a mapping document")
	}
	segments := strings.Split(dotPath, ".")
	current := document.Content[0]
	for index, segment := range segments {
		segment = strings.TrimSpace(segment)
		last := index == len(segments)-1
		position := mappingPosition(current, segment)
		if last {
			if position >= 0 {
				existing := dereferenceYAMLAlias(current.Content[position+1])
				if existing == nil || existing.Kind != yaml.ScalarNode {
					return fmt.Errorf("YAML guard path %q must reference a scalar", dotPath)
				}
				current.Content[position+1] = valueNode
			} else if allowCreate {
				current.Content = append(current.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
					valueNode,
				)
			} else {
				return fmt.Errorf("YAML guard path %q does not exist", dotPath)
			}
			break
		}
		if position >= 0 {
			next := dereferenceYAMLAlias(current.Content[position+1])
			if next == nil || next.Kind != yaml.MappingNode {
				return fmt.Errorf("YAML guard path segment %q is not a mapping", strings.Join(segments[:index+1], "."))
			}
			current = next
			continue
		}
		if !allowCreate {
			return fmt.Errorf("YAML guard path %q does not exist", dotPath)
		}
		next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		current.Content = append(current.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
			next,
		)
		current = next
	}
	buffer := &bytes.Buffer{}
	encoder := yaml.NewEncoder(buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode guarded YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close guarded YAML encoder: %w", err)
	}
	return writePrivateFile(path, buffer.Bytes())
}

func verifyYAMLGuard(path string, dotPath string, expected any) error {
	document, err := readYAMLGuardDocument(path)
	if err != nil {
		return err
	}
	actualNode, found, err := yamlValueAtPath(document, dotPath)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("YAML guard path %q does not exist", dotPath)
	}
	actualNode = dereferenceYAMLAlias(actualNode)
	if actualNode == nil || actualNode.Kind != yaml.ScalarNode {
		return fmt.Errorf("YAML guard path %q must reference a scalar", dotPath)
	}
	expectedNode, err := encodeYAMLGuardValue(expected)
	if err != nil {
		return err
	}
	actualIdentifier, err := yamlScalarIdentifier(actualNode)
	if err != nil {
		return fmt.Errorf("decode YAML guard path %q: %w", dotPath, err)
	}
	expectedIdentifier, err := yamlScalarIdentifier(expectedNode)
	if err != nil {
		return fmt.Errorf("decode expected YAML guard value for %q: %w", dotPath, err)
	}
	if actualIdentifier != expectedIdentifier {
		return fmt.Errorf("YAML guard path %q does not match its enforced scalar value", dotPath)
	}
	return nil
}

func readYAMLGuardDocument(path string) (*yaml.Node, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("YAML guard file %q must be a real file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	document, err := decodeSingleYAML(data)
	if err != nil {
		return nil, err
	}
	if err := validateYAMLGuardMappings(document, make(map[*yaml.Node]bool), ""); err != nil {
		return nil, err
	}
	return document, nil
}

func validateYAMLGuardMappings(node *yaml.Node, visited map[*yaml.Node]bool, path string) error {
	if node == nil || visited[node] {
		return nil
	}
	visited[node] = true
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateYAMLGuardMappings(child, visited, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		seenText := make(map[string]bool, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind == yaml.ScalarNode && (key.Value == "<<" || key.Tag == "!!merge") {
				return fmt.Errorf("YAML guard document uses an unsupported merge key at %s", yamlPathLabel(path))
			}
			if key.Kind != yaml.ScalarNode || (key.Tag != "!!str" && key.Tag != "!!int") {
				return fmt.Errorf("YAML guard document uses an unsupported mapping key at %s", yamlPathLabel(path))
			}
			identifier, err := yamlScalarIdentifier(key)
			if err != nil {
				return fmt.Errorf("decode YAML guard mapping key at %s: %w", yamlPathLabel(path), err)
			}
			var decoded any
			if err := key.Decode(&decoded); err != nil {
				return fmt.Errorf("decode YAML guard mapping key at %s: %w", yamlPathLabel(path), err)
			}
			textIdentifier := fmt.Sprint(decoded)
			keyPath := key.Value
			if path != "" {
				keyPath = path + "." + key.Value
			}
			if seen[identifier] || seenText[textIdentifier] {
				return fmt.Errorf("YAML guard document uses duplicate key %s", keyPath)
			}
			seen[identifier] = true
			seenText[textIdentifier] = true
			if err := validateYAMLGuardMappings(node.Content[index+1], visited, keyPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if err := validateYAMLGuardMappings(child, visited, childPath); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return validateYAMLGuardMappings(node.Alias, visited, path)
	}
	return nil
}

func yamlValueAtPath(document *yaml.Node, dotPath string) (*yaml.Node, bool, error) {
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("YAML guard requires a mapping document")
	}
	segments := strings.Split(dotPath, ".")
	current := document.Content[0]
	for index, segment := range segments {
		segment = strings.TrimSpace(segment)
		position := mappingPosition(current, segment)
		if position < 0 {
			return nil, false, nil
		}
		value := current.Content[position+1]
		if index == len(segments)-1 {
			return value, true, nil
		}
		current = dereferenceYAMLAlias(value)
		if current == nil || current.Kind != yaml.MappingNode {
			return nil, false, fmt.Errorf("YAML guard path segment %q is not a mapping", strings.Join(segments[:index+1], "."))
		}
	}
	return nil, false, nil
}

func encodeYAMLGuardValue(value any) (*yaml.Node, error) {
	node, err := encodeYAMLValue(value)
	if err != nil {
		return nil, err
	}
	if node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return nil, errors.New("YAML guard value must be a non-null scalar")
	}
	return node, nil
}

func dereferenceYAMLAlias(node *yaml.Node) *yaml.Node {
	visited := make(map[*yaml.Node]bool)
	for node != nil && node.Kind == yaml.AliasNode && !visited[node] {
		visited[node] = true
		node = node.Alias
	}
	return node
}

func encodeYAMLValue(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode patch value: %w", err)
	}
	document := &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return nil, fmt.Errorf("decode patch value: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, errors.New("patch value did not produce one YAML document")
	}
	return document.Content[0], nil
}

func mappingPosition(mapping *yaml.Node, key string) int {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		candidate := mapping.Content[index]
		if candidate.Kind == yaml.ScalarNode && candidate.Tag == "!!str" && candidate.Value == key {
			return index
		}
	}
	return -1
}

func validateYAMLTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".yaml" && extension != ".yml" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		decoder := yaml.NewDecoder(file)
		documentNumber := 0
		for {
			document := &yaml.Node{}
			err = decoder.Decode(document)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				file.Close()
				return fmt.Errorf("validate YAML %s: %w", path, err)
			}
			documentNumber++
			if err := validateUniqueYAMLKeys(document, map[*yaml.Node]bool{}, ""); err != nil {
				file.Close()
				return fmt.Errorf("validate YAML %s document %d: %w", path, documentNumber, err)
			}
		}
		if err := file.Close(); err != nil {
			return err
		}
		return nil
	})
}

func decodeSingleYAML(data []byte) (*yaml.Node, error) {
	return decodeStrictSingleYAML(data, "YAML patch")
}

func decodeStrictSingleYAML(data []byte, purpose string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	document := &yaml.Node{}
	if err := decoder.Decode(document); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	if err := validateUniqueYAMLKeys(document, map[*yaml.Node]bool{}, ""); err != nil {
		return nil, err
	}
	extra := &yaml.Node{}
	if err := decoder.Decode(extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing YAML document: %w", err)
		}
		return nil, fmt.Errorf("%s requires exactly one document", purpose)
	}
	return document, nil
}

func validateUniqueYAMLKeys(node *yaml.Node, visited map[*yaml.Node]bool, path string) error {
	if node == nil || visited[node] {
		return nil
	}
	visited[node] = true
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateUniqueYAMLKeys(child, visited, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("YAML mapping at %s contains a non-scalar key", yamlPathLabel(path))
			}
			identifier, err := yamlScalarIdentifier(key)
			if err != nil {
				return fmt.Errorf("decode YAML mapping key at %s: %w", yamlPathLabel(path), err)
			}
			keyPath := key.Value
			if path != "" {
				keyPath = path + "." + key.Value
			}
			if seen[identifier] {
				return fmt.Errorf("duplicate YAML key %s", keyPath)
			}
			seen[identifier] = true
			if err := validateUniqueYAMLKeys(node.Content[index+1], visited, keyPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if err := validateUniqueYAMLKeys(child, visited, childPath); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return validateUniqueYAMLKeys(node.Alias, visited, path)
	}
	return nil
}

func yamlScalarIdentifier(node *yaml.Node) (string, error) {
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\x00%#v", node.Tag, decoded), nil
}

func yamlPathLabel(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}
