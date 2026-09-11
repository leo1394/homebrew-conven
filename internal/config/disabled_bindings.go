package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// SetBindingsDisabled changes the workspace declaration, not running processes.
func SetBindingsDisabled(path string, names []string, disabled bool) (bool, error) {
	if len(names) == 0 {
		return false, fmt.Errorf("binding update requires at least one binding")
	}
	selected := make(map[string]bool)
	for _, name := range names {
		if !validBindingName(name) {
			return false, fmt.Errorf("invalid binding %q: use a letter or '_' followed by letters, digits, '_' or '-'", name)
		}
		selected[name] = true
	}
	source, info, err := readManifestForUpdate(path)
	if err != nil {
		return false, err
	}
	manifest, err := decodeManifest(source, path)
	if err != nil {
		return false, err
	}
	candidate := *manifest
	candidate.Workspace.DisabledBindings = []string{}
	present := make(map[string]bool)
	changed := false
	for _, name := range manifest.Workspace.DisabledBindings {
		present[name] = true
		if !disabled && selected[name] {
			changed = true
			continue
		}
		candidate.Workspace.DisabledBindings = append(candidate.Workspace.DisabledBindings, name)
	}
	if disabled {
		for _, name := range names {
			if !present[name] {
				candidate.Workspace.DisabledBindings = append(candidate.Workspace.DisabledBindings, name)
				present[name] = true
				changed = true
			}
		}
	}
	if !changed {
		return false, verifyManifestSnapshot(path, source, info, "binding update")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return false, err
	}
	workspace := mappingValue(document.Content[0], "workspace")
	if workspace == nil || workspace.Kind != yaml.MappingNode {
		return false, fmt.Errorf("workspace must be a YAML mapping to update disabled bindings")
	}
	bindings := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	old := mappingValue(workspace, "disabledBindings")
	for _, name := range candidate.Workspace.DisabledBindings {
		node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		if old != nil && old.Kind == yaml.SequenceNode {
			for _, existing := range old.Content {
				if existing.Value == name {
					node = existing
					break
				}
			}
		}
		bindings.Content = append(bindings.Content, node)
	}
	if old != nil {
		bindings.HeadComment, bindings.LineComment, bindings.FootComment = old.HeadComment, old.LineComment, old.FootComment
	}
	setMappingValue(workspace, "disabledBindings", bindings)
	if err := saveManifestDocumentForOperation(path, &document, source, info, &candidate, "binding update"); err != nil {
		return false, err
	}
	return true, nil
}
