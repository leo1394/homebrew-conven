package config

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"

	"github.com/leo1394/homebrew-conven/internal/model"
)

// Repair only the known discovery-only guard when local routing uses Target.
// Source bytes outside that condition are preserved, including user edits.
func repairWorkspaceRPCTargetGuards(manifest *model.Manifest, workspace string, diagnostics *[]string) ([]string, error) {
	notes := []string{}
	disabled := make(map[string]bool)
	for _, binding := range manifest.Workspace.DisabledBindings { disabled[binding] = true }
	for _, name := range ServiceNames(manifest) {
		service := manifest.Services[name]
		policyName := service.Policy
		if policyName == "" { policyName = manifest.Workspace.Policy }
		policy := manifest.Policies[policyName]
		runtime := policy.Drivers.Runtime
		if runtime == "" { runtime = policy.Drivers.Framework }
		value, ok := policy.Routing.LocalDependency.Value.(map[string]interface{})
		if runtime != "go-zero" || policy.Routing.LocalDependency.Mode != "replace" || !ok || value["target"] == nil || value["discovType"] != nil { continue }
		bindings := []string{}
		seen := make(map[string]bool)
		for _, dependency := range service.Dependencies {
			if dependency.Binding != "" && !disabled[dependency.Binding] && !seen[dependency.Binding] { bindings = append(bindings, dependency.Binding); seen[dependency.Binding] = true }
		}
		sort.Strings(bindings)
		for _, binding := range bindings {
			for {
				err := InspectGoRPCTargetInitialization(filepath.Join(workspace, service.Path), service.Runner.Workdir, binding)
				if err == nil { break }
				var guard *rpcTargetGuardError
				if !errors.As(err, &guard) || !guard.repairable {
					*diagnostics = append(*diagnostics, fmt.Sprintf("service %s binding %s source unchanged: %v", name, binding, err))
					break
				}
				if err := repairRPCTargetGuard(guard); err != nil {
					return notes, fmt.Errorf("service %s binding %s source repair failed: %w", name, binding, err)
				}
				notes = append(notes, fmt.Sprintf("service %s binding %s: automatically added Target support in %s:%d; existing formatting preserved", name, binding, guard.path, guard.line))
			}
		}
	}
	return notes, nil
}

func repairRPCTargetGuard(guard *rpcTargetGuardError) error {
	info, err := os.Lstat(guard.path)
	if err != nil { return err }
	if !info.Mode().IsRegular() { return fmt.Errorf("source must be a regular file: %s", guard.path) }
	source, err := os.ReadFile(guard.path)
	if err != nil { return err }
	if !bytes.Equal(source, guard.source) { return fmt.Errorf("source changed during target inspection: %s", guard.path) }
	if guard.end > len(source) || guard.receiverStart < guard.start || guard.receiverEnd > guard.end { return fmt.Errorf("source changed during target inspection: %s", guard.path) }
	replacement := string(source[guard.start:guard.end]) + " || " + string(source[guard.receiverStart:guard.receiverEnd]) + ".Target != \"\""
	updated := string(source[:guard.start]) + replacement + string(source[guard.end:])
	if _, err := parser.ParseFile(token.NewFileSet(), guard.path, updated, 0); err != nil { return err }
	file, err := os.CreateTemp(filepath.Dir(guard.path), ".conven-target-*")
	if err != nil { return err }
	defer os.Remove(file.Name())
	if err := file.Chmod(info.Mode().Perm()); err != nil { file.Close(); return err }
	if _, err := file.WriteString(updated); err != nil { file.Close(); return err }
	if err := file.Close(); err != nil { return err }
	return os.Rename(file.Name(), guard.path)
}
