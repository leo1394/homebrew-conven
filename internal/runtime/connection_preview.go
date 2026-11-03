package runtime

import "fmt"

// ConnectionPreview uses the launch resolver without establishing a connection,
// reading credentials or materializing business configuration.
func ConnectionPreview(workspace *WorkspaceData, environment string) (map[string]string, error) {
	env, found := workspace.Manifest.Environments[environment]
	if !found { return nil, fmt.Errorf("environment is not declared") }
	plan := &Plan{Workspace: workspace, EnvironmentName: environment, Environment: env, RunDir: workspace.Store.CurrentDir}
	connection, err := planConnection(plan, CommonOptions{Environment: environment})
	if err != nil { return nil, err }
	return map[string]string{"environment":environment, "driver":connection.Driver, "kubeconfig":connection.Kubeconfig, "source":connection.KubeconfigSource, "context":connection.Context, "namespace":connection.Namespace, "phase":"planned"}, nil
}
