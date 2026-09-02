package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func InitLocalWorkspaceDetailsWithPolicySpecification(cwd string, application []byte, specification []byte) (InitResult, error) {
	result, err := InitWorkspaceDetailsWithPolicySpecification(cwd, application, specification)
	if err != nil {
		return result, err
	}
	if !result.Created {
		return result, nil
	}
	data, err := renderLocalManifest(result.Path)
	if err != nil {
		return result, err
	}
	if err := replaceGeneratedFile(result.Path, data, 0600); err != nil {
		return result, err
	}
	if _, err := Load(result.Path); err != nil {
		return result, err
	}
	return result, nil
}

func renderLocalManifest(path string) ([]byte, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read generated Conven manifest: %w", err)
	}
	document := &yaml.Node{}
	if err := yaml.Unmarshal(source, document); err != nil {
		return nil, fmt.Errorf("decode generated Conven manifest: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("generated Conven manifest root must be a mapping")
	}
	root := document.Content[0]
	version := mappingValue(root, "version")
	if version == nil {
		setLocalMappingValue(root, "version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "3"})
	} else {
		version.Kind = yaml.ScalarNode
		version.Tag = "!!int"
		version.Value = "3"
	}
	environments := mappingValue(root, "environments")
	if environments == nil || environments.Kind != yaml.MappingNode {
		environments = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setLocalMappingValue(root, "environments", environments)
	}
	environments.Style = 0
	localEnvironment := &yaml.Node{}
	localEnvironment.Encode(map[string]interface{}{
		"connection": map[string]interface{}{"driver": "none"},
	})
	clearLocalYAMLFlowStyle(localEnvironment)
	setLocalMappingValue(environments, "local", localEnvironment)
	var result bytes.Buffer
	encoder := yaml.NewEncoder(&result)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode local Conven manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close local Conven manifest encoder: %w", err)
	}
	return result.Bytes(), nil
}

func clearLocalYAMLFlowStyle(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		node.Style = 0
	}
	for _, child := range node.Content {
		clearLocalYAMLFlowStyle(child)
	}
}

func setLocalMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func replaceGeneratedFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".local-init-*")
	if err != nil {
		return fmt.Errorf("create temporary local manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary local manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary local manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary local manifest: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish local manifest: %w", err)
	}
	return nil
}
