package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/leo1394/homebrew-conven/internal/terminal"
	"gopkg.in/yaml.v3"
)

const ktctlConnectionMaxAttempts = 2
const kubernetesAPIStableProbes = 3
const kubernetesRecoveryStableObservations = 3
const kubernetesAPIGateTimeout = 30 * time.Second
const kubernetesCommandTimeout = 5 * time.Second
const kubernetesCommandWaitDelay = 500 * time.Millisecond
const kubernetesProbeInterval = time.Second
const kubernetesRecoveryObservationWindow = 3 * time.Second
const kubernetesRecoveryDeletionTimeout = 30 * time.Second
const ktctlAttemptMetadataKey = "conven.dev/connection-attempt"

type kubernetesAPIProbe struct {
	Name                string
	Executable          string
	Args                []string
	Timeout             time.Duration
	RequireKtctlSuccess bool
}

type kubernetesCommandOutput struct {
	Stdout []byte
	Stderr []byte
}

type kubernetesResourceList struct {
	Items []kubernetesResource `json:"items"`
}

type kubernetesResource struct {
	Kind     string                     `json:"kind"`
	Metadata kubernetesResourceMetadata `json:"metadata"`
}

type kubernetesResourceMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	DeletionTimestamp json.RawMessage   `json:"deletionTimestamp"`
}

func validateKtctlConnectionArgs(args []string) error {
	labelFlags := 0
	annotationFlags := 0
	for index := 0; index < len(args); index++ {
		argument := args[index]
		var metadata string
		switch {
		case argument == "--":
			return errors.New("ktctl argument -- is not supported because Conven must append connection-attempt ownership metadata")
		case ktctlBundledShortFlag(argument):
			return fmt.Errorf("ktctl bundled short argument %q is not supported because it can override Conven-managed Kubernetes target or ownership options; use separate or long-form flags instead", argument)
		case ktctlTargetFlag(argument):
			return fmt.Errorf("ktctl argument %q conflicts with connection kubeconfig/context/namespace; configure the Kubernetes target through the connection fields instead", argument)
		case argument == "--withLabel" || argument == "-l" || argument == "--withAnnotation":
			if index+1 >= len(args) {
				return fmt.Errorf("ktctl %s requires a value", argument)
			}
			if argument == "--withLabel" || argument == "-l" {
				labelFlags++
			} else {
				annotationFlags++
			}
			index++
			metadata = args[index]
		case ktctlOptionConsumesNextValue(argument):
			if index+1 >= len(args) {
				return fmt.Errorf("ktctl %s requires a value", argument)
			}
			index++
			continue
		case strings.HasPrefix(argument, "--withLabel="):
			labelFlags++
			metadata = strings.TrimPrefix(argument, "--withLabel=")
		case strings.HasPrefix(argument, "-l="):
			labelFlags++
			metadata = strings.TrimPrefix(argument, "-l=")
		case strings.HasPrefix(argument, "-l") && !strings.HasPrefix(argument, "--"):
			labelFlags++
			metadata = strings.TrimPrefix(argument, "-l")
		case strings.HasPrefix(argument, "--withAnnotation="):
			annotationFlags++
			metadata = strings.TrimPrefix(argument, "--withAnnotation=")
		default:
			continue
		}
		for _, item := range strings.Split(metadata, ",") {
			key := strings.TrimSpace(strings.SplitN(item, "=", 2)[0])
			if key == ktctlAttemptMetadataKey {
				return fmt.Errorf("ktctl metadata key %q is reserved by Conven", ktctlAttemptMetadataKey)
			}
		}
	}
	if labelFlags > 1 {
		return errors.New("ktctl argument --withLabel may only be specified once so Conven can preserve its connection-attempt label")
	}
	if annotationFlags > 1 {
		return errors.New("ktctl argument --withAnnotation may only be specified once so Conven can preserve its connection-attempt annotation")
	}
	for _, flag := range []string{"--shareShadow", "--useShadowDeployment", "--useLocalTime", "--skipCleanup"} {
		if _, _, err := ktctlBoolFlagValue(args, flag); err != nil {
			return err
		}
	}
	for _, flag := range []string{"--useLocalTime", "--skipCleanup"} {
		if value, present, _ := ktctlBoolFlagValue(args, flag); present && !value {
			return fmt.Errorf("ktctl argument %s=false is not supported because Conven-managed connections require it for isolated recovery", flag)
		}
	}
	return nil
}

func ktctlBundledShortFlag(argument string) bool {
	if len(argument) <= 2 || argument[0] != '-' || strings.HasPrefix(argument, "--") {
		return false
	}
	switch argument[1] {
	case 'd', 'f', 'h', 'v':
		return true
	default:
		return false
	}
}

func ktctlOptionConsumesNextValue(argument string) bool {
	switch argument {
	case "--mode", "--dnsMode", "--clusterDomain", "--includeIps", "--excludeIps", "--ingressIp", "--proxyPort", "--dnsCacheTtl", "--dnsPort", "--includeDomains",
		"--kubeconfig", "--context", "--namespace", "--image", "--imagePullSecret", "--serviceAccount", "--nodeSelector", "--withLabel", "--withAnnotation",
		"--portForwardTimeout", "--podCreationTimeout", "--podQuota", "-c", "-n", "-i", "-l":
		return true
	default:
		return false
	}
}

func ktctlTargetFlag(argument string) bool {
	if !strings.HasPrefix(argument, "--") && (strings.HasPrefix(argument, "-n") || strings.HasPrefix(argument, "-c")) {
		return true
	}
	for _, flag := range []string{"--kubeconfig", "--context", "--namespace"} {
		if argument == flag || strings.HasPrefix(argument, flag+"=") {
			return true
		}
	}
	return false
}

func ktctlArgsWithAttemptMetadata(args []string, attemptID string) ([]string, error) {
	if err := validateKtctlConnectionArgs(args); err != nil {
		return nil, err
	}
	result := append([]string(nil), args...)
	value := ktctlAttemptMetadataKey + "=" + attemptID
	labelAdded := false
	annotationAdded := false
	for index := 0; index < len(result); index++ {
		switch {
		case (result[index] == "--withLabel" || result[index] == "-l") && !labelAdded:
			result[index+1] = appendKtctlMetadata(result[index+1], value)
			labelAdded = true
			index++
		case strings.HasPrefix(result[index], "--withLabel=") && !labelAdded:
			result[index] = "--withLabel=" + appendKtctlMetadata(strings.TrimPrefix(result[index], "--withLabel="), value)
			labelAdded = true
		case strings.HasPrefix(result[index], "-l=") && !labelAdded:
			result[index] = "-l=" + appendKtctlMetadata(strings.TrimPrefix(result[index], "-l="), value)
			labelAdded = true
		case strings.HasPrefix(result[index], "-l") && !strings.HasPrefix(result[index], "--") && !labelAdded:
			result[index] = "-l" + appendKtctlMetadata(strings.TrimPrefix(result[index], "-l"), value)
			labelAdded = true
		case result[index] == "--withAnnotation" && !annotationAdded:
			result[index+1] = appendKtctlMetadata(result[index+1], value)
			annotationAdded = true
			index++
		case strings.HasPrefix(result[index], "--withAnnotation=") && !annotationAdded:
			result[index] = "--withAnnotation=" + appendKtctlMetadata(strings.TrimPrefix(result[index], "--withAnnotation="), value)
			annotationAdded = true
		case ktctlOptionConsumesNextValue(result[index]):
			index++
		}
	}
	if !labelAdded {
		result = append(result, "--withLabel", value)
	}
	if !annotationAdded {
		result = append(result, "--withAnnotation", value)
	}
	return result, nil
}

func ktctlArgsForAttempt(config ConnectionConfig, attemptID string) ([]string, error) {
	result, err := ktctlArgsWithAttemptMetadata(config.Args, attemptID)
	if err != nil {
		return nil, err
	}
	result = appendKtctlBoolDefault(result, "--shareShadow", false)
	result = appendKtctlBoolDefault(result, "--useShadowDeployment", false)
	result = append(result, "--useLocalTime=true", "--skipCleanup=true")
	return result, nil
}

func appendKtctlBoolDefault(args []string, flag string, value bool) []string {
	if _, present, _ := ktctlBoolFlagValue(args, flag); present {
		return args
	}
	return append(args, fmt.Sprintf("%s=%t", flag, value))
}

func ktctlBoolFlagValue(args []string, flag string) (bool, bool, error) {
	value := false
	present := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if ktctlOptionConsumesNextValue(argument) {
			index++
			continue
		}
		switch {
		case argument == flag:
			value = true
			present = true
		case strings.HasPrefix(argument, flag+"="):
			parsed, err := strconv.ParseBool(strings.TrimPrefix(argument, flag+"="))
			if err != nil {
				return false, true, fmt.Errorf("ktctl argument %q requires a boolean value", argument)
			}
			value = parsed
			present = true
		}
	}
	return value, present, nil
}

func pinKtctlKubernetesTarget(config ConnectionConfig) (ConnectionConfig, error) {
	if config.kubeconfigIdentity != "" && config.kubeconfigSnapshot != nil {
		return config, nil
	}
	if config.Kubeconfig == "" {
		return ConnectionConfig{}, errors.New("ktctl connection requires an explicit kubeconfig so readiness probes and elevated connection processes use the same cluster")
	}
	data, err := os.ReadFile(config.Kubeconfig)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("read kubeconfig to pin ktctl target: %w", err)
	}
	snapshot, err := resolveKtctlKubeconfigSnapshotPaths(data, config.Kubeconfig)
	if err != nil {
		return ConnectionConfig{}, err
	}
	identitySource := append([]byte(config.Kubeconfig+"\x00"), snapshot...)
	identity := sha256.Sum256(identitySource)
	config.kubeconfigIdentity = hex.EncodeToString(identity[:])
	config.kubeconfigSnapshot = snapshot
	config.Namespace = strings.TrimSpace(config.Namespace)
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	config.Context = strings.TrimSpace(config.Context)
	if config.Context != "" {
		return config, nil
	}
	current := struct {
		Context string `yaml:"current-context"`
	}{}
	if err := yaml.Unmarshal(data, &current); err != nil {
		return ConnectionConfig{}, fmt.Errorf("parse kubeconfig to pin ktctl context: %w", err)
	}
	config.Context = strings.TrimSpace(current.Context)
	if config.Context == "" {
		return ConnectionConfig{}, errors.New("kubeconfig current-context is empty; set connection.context explicitly before starting ktctl")
	}
	return config, nil
}

func resolveKtctlKubeconfigSnapshotPaths(data []byte, sourcePath string) ([]byte, error) {
	document := make(map[string]interface{})
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse kubeconfig to create private snapshot: %w", err)
	}
	base, err := filepath.Abs(filepath.Dir(sourcePath))
	if err != nil {
		return nil, fmt.Errorf("resolve kubeconfig source directory: %w", err)
	}
	clusters, err := kubeconfigEntries(document, "clusters")
	if err != nil {
		return nil, err
	}
	for _, cluster := range clusters {
		settings, err := kubeconfigMapping(cluster["cluster"], "cluster settings")
		if err != nil {
			return nil, err
		}
		if settings == nil {
			continue
		}
		if err := resolveKubeconfigPath(settings, "certificate-authority", base, false); err != nil {
			return nil, err
		}
	}
	users, err := kubeconfigEntries(document, "users")
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		settings, err := kubeconfigMapping(user["user"], "user settings")
		if err != nil {
			return nil, err
		}
		if settings == nil {
			continue
		}
		for _, key := range []string{"client-certificate", "client-key", "tokenFile"} {
			if err := resolveKubeconfigPath(settings, key, base, false); err != nil {
				return nil, err
			}
		}
		execConfig, err := kubeconfigMapping(settings["exec"], "exec settings")
		if err != nil {
			return nil, err
		}
		if execConfig != nil {
			if err := resolveKubeconfigPath(execConfig, "command", base, true); err != nil {
				return nil, err
			}
		}
	}
	snapshot, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode private ktctl kubeconfig snapshot: %w", err)
	}
	return snapshot, nil
}

func kubeconfigEntries(root map[string]interface{}, key string) ([]map[string]interface{}, error) {
	value := root[key]
	if value == nil {
		return nil, nil
	}
	entries, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("parse kubeconfig to create private snapshot: %s must be a sequence", key)
	}
	result := make([]map[string]interface{}, 0, len(entries))
	for _, value := range entries {
		entry, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("parse kubeconfig to create private snapshot: %s entry must be a mapping", key)
		}
		result = append(result, entry)
	}
	return result, nil
}

func kubeconfigMapping(value interface{}, label string) (map[string]interface{}, error) {
	if value == nil {
		return nil, nil
	}
	mapping, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("parse kubeconfig to create private snapshot: %s must be a mapping", label)
	}
	return mapping, nil
}

func resolveKubeconfigPath(mapping map[string]interface{}, key string, base string, requireSeparator bool) error {
	value := mapping[key]
	if value == nil {
		return nil
	}
	path, ok := value.(string)
	if !ok {
		return fmt.Errorf("parse kubeconfig to create private snapshot: %s must be a string", key)
	}
	if path == "" || filepath.IsAbs(path) || requireSeparator && !strings.ContainsRune(path, filepath.Separator) {
		return nil
	}
	mapping[key] = filepath.Join(base, path)
	return nil
}

func materializeKtctlKubeconfigSnapshot(config ConnectionConfig, fingerprint string) (string, error) {
	if config.kubeconfigIdentity == "" || config.kubeconfigSnapshot == nil {
		return "", errors.New("ktctl Kubernetes target was not pinned before creating its private kubeconfig snapshot")
	}
	directory, err := connectionStateDirectory()
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, fingerprint+".kubeconfig")
	temporary, err := os.CreateTemp(directory, ".kubeconfig-*")
	if err != nil {
		return "", fmt.Errorf("create private ktctl kubeconfig snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("protect private ktctl kubeconfig snapshot: %w", err)
	}
	if _, err := temporary.Write(config.kubeconfigSnapshot); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write private ktctl kubeconfig snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close private ktctl kubeconfig snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", fmt.Errorf("publish private ktctl kubeconfig snapshot: %w", err)
	}
	return path, nil
}

func removeKtctlKubeconfigSnapshot(fingerprint string) error {
	directory, err := connectionStateDirectory()
	if err != nil {
		return err
	}
	path := filepath.Join(directory, fingerprint+".kubeconfig")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove private ktctl kubeconfig snapshot: %w", err)
	}
	return nil
}

func appendKtctlMetadata(existing string, value string) string {
	if strings.TrimSpace(existing) == "" {
		return value
	}
	return existing + "," + value
}

func newKtctlAttemptID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate ktctl connection attempt identity: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func newKubernetesAPIProbe(config ConnectionConfig) (*kubernetesAPIProbe, error) {
	if kubectlPath, err := exec.LookPath("kubectl"); err == nil {
		return &kubernetesAPIProbe{
			Name:       "kubectl readyz",
			Executable: kubectlPath,
			Args:       kubernetesGlobalArgs(config, "get", "--raw=/readyz"),
			Timeout:    kubernetesCommandTimeout,
		}, nil
	}
	command := config.Command
	if command == "" {
		command = "ktctl"
	}
	ktctlPath, err := exec.LookPath(command)
	if err != nil {
		return nil, errors.New("Kubernetes API stability gate requires kubectl or the configured ktctl executable on PATH")
	}
	return &kubernetesAPIProbe{
		Name:                "ktctl birdseye",
		Executable:          ktctlPath,
		Args:                ktctlGlobalArgs(config, "birdseye", "--hideNaturalService"),
		Timeout:             10 * time.Second,
		RequireKtctlSuccess: true,
	}, nil
}

func waitForKubernetesAPI(ctx context.Context, config ConnectionConfig, output io.Writer) error {
	probe, err := newKubernetesAPIProbe(config)
	if err != nil {
		return err
	}
	return waitForKubernetesAPIProbe(ctx, probe, output)
}

func waitForKubernetesAPIProbe(ctx context.Context, probe *kubernetesAPIProbe, output io.Writer) error {
	if probe == nil {
		return errors.New("Kubernetes API stability probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(ctx, kubernetesAPIGateTimeout)
	defer cancel()
	style := terminal.New(output)
	fmt.Fprintf(output, "%s %s\n", style.Stage("Waiting for Kubernetes API stability"), style.Identifier(probe.Name))
	consecutive := 0
	var lastErr error
	for {
		if err := probeContext.Err(); err != nil {
			terminalErr := err
			if lastErr == nil {
				lastErr = terminalErr
			}
			return fmt.Errorf("Kubernetes API stability gate failed after %d/%d consecutive successful %s probes: %w", consecutive, kubernetesAPIStableProbes, probe.Name, errors.Join(terminalErr, lastErr))
		}
		result, err := runKubernetesCommandCaptureWithInput(probeContext, probe.Executable, probe.Timeout, nil, probe.Args...)
		if err == nil && probe.RequireKtctlSuccess {
			err = validateKtctlProbeOutput(result)
		}
		if err != nil {
			if probeContext.Err() == nil || lastErr == nil {
				lastErr = err
			}
			consecutive = 0
		} else {
			consecutive++
			if consecutive >= kubernetesAPIStableProbes {
				fmt.Fprintln(output, style.Success("✓ Kubernetes API stable."))
				return nil
			}
		}
		timer := time.NewTimer(kubernetesProbeInterval)
		select {
		case <-probeContext.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func kubernetesGlobalArgs(config ConnectionConfig, args ...string) []string {
	result := make([]string, 0, len(args)+7)
	if config.Kubeconfig != "" {
		result = append(result, "--kubeconfig", config.Kubeconfig)
	}
	if config.Context != "" {
		result = append(result, "--context", config.Context)
	}
	if config.Namespace != "" {
		result = append(result, "--namespace", config.Namespace)
	}
	result = append(result, "--request-timeout="+kubernetesCommandTimeout.String())
	return append(result, args...)
}

func ktctlGlobalArgs(config ConnectionConfig, args ...string) []string {
	namespace := config.Namespace
	if namespace == "" {
		namespace = "default"
	}
	result := []string{
		"--kubeconfig=" + config.Kubeconfig,
		"--context=" + config.Context,
		"--namespace=" + namespace,
		"--useLocalTime=true",
	}
	return append(result, args...)
}

func validateKtctlProbeOutput(output kubernetesCommandOutput) error {
	detail := sanitizeDashboardText(string(output.Stdout) + "\n" + string(output.Stderr))
	if strings.Contains(detail, "ERR Exit:") {
		return fmt.Errorf("ktctl birdseye reported an error despite returning success: %s", strings.TrimSpace(detail))
	}
	if !strings.Contains(detail, "Service in namespace") {
		return errors.New("ktctl birdseye returned success without an affirmative Kubernetes service response")
	}
	return nil
}

func runKubernetesCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return runKubernetesCommandWithInput(ctx, executable, kubernetesCommandTimeout, nil, args...)
}

func runKubernetesCommandWithInput(ctx context.Context, executable string, timeout time.Duration, input []byte, args ...string) ([]byte, error) {
	output, err := runKubernetesCommandCaptureWithInput(ctx, executable, timeout, input, args...)
	return output.Stdout, err
}

func runKubernetesCommandCaptureWithInput(ctx context.Context, executable string, timeout time.Duration, input []byte, args ...string) (kubernetesCommandOutput, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, args...)
	command.Env = CommandEnvironment(map[string]string{"LC_ALL": "C"})
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = kubernetesCommandWaitDelay
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if errors.Is(err, exec.ErrWaitDelay) && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		if commandContext.Err() != nil {
			err = commandContext.Err()
		}
		detail := strings.TrimSpace(sanitizeDashboardText(stderr.String()))
		if detail == "" {
			return kubernetesCommandOutput{}, err
		}
		return kubernetesCommandOutput{}, fmt.Errorf("%w: %s", err, detail)
	}
	return kubernetesCommandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func recoverKtctlAttempt(ctx context.Context, config ConnectionConfig, attemptID string) error {
	if err := ktctlRetrySafetyError(config); err != nil {
		return err
	}
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return errors.New("kubectl is required to audit a failed ktctl Pod CREATE")
	}
	resources, err := observeKtctlAttemptResources(ctx, kubectlPath, config, attemptID, kubernetesRecoveryStableObservations)
	if err != nil {
		return err
	}
	if len(resources) == 0 {
		return errors.New("no attempt-owned Kubernetes resource became observable, so completion of the interrupted CREATE cannot be proven and automatic retry is unsafe")
	}
	ownedPodObserved := false
	for _, resource := range resources {
		if resource.Kind == "Pod" {
			ownedPodObserved = true
			break
		}
	}
	if !ownedPodObserved {
		return errors.New("the attempt-owned Pod did not become observable, so completion of the interrupted Pod CREATE cannot be proven and automatic retry is unsafe")
	}
	for _, resource := range resources {
		if err := deleteKtctlAttemptResource(ctx, kubectlPath, config, resource); err != nil {
			return fmt.Errorf("delete attempt-owned Kubernetes resource %s/%s: %w", strings.ToLower(resource.Kind), resource.Metadata.Name, err)
		}
	}
	if err := waitForKtctlAttemptResourcesDeleted(ctx, kubectlPath, config, attemptID, resources); err != nil {
		return fmt.Errorf("verify attempt-owned Kubernetes resources were deleted: %w", err)
	}
	return nil
}

func waitForKtctlAttemptResourcesDeleted(ctx context.Context, kubectlPath string, config ConnectionConfig, attemptID string, deleted []kubernetesResource) error {
	expected := make(map[string]string, len(deleted))
	for _, resource := range deleted {
		expected[kubernetesResourceKey(resource)] = resource.Metadata.UID
	}
	waitContext, cancel := context.WithTimeout(ctx, kubernetesRecoveryDeletionTimeout)
	defer cancel()
	selector := ktctlAttemptMetadataKey + "=" + attemptID
	for {
		if err := waitContext.Err(); err != nil {
			return fmt.Errorf("timed out waiting for exact attempt-owned resource UIDs to disappear: %w", err)
		}
		output, err := runKubernetesCommand(waitContext, kubectlPath, kubernetesGlobalArgs(config, "get", "pods,configmaps", "--selector", selector, "-o", "json")...)
		if err != nil {
			return fmt.Errorf("query deleting attempt-owned Kubernetes resources: %w", err)
		}
		var list kubernetesResourceList
		if err := json.Unmarshal(output, &list); err != nil {
			return fmt.Errorf("query deleting attempt-owned Kubernetes resources returned malformed JSON: %w", err)
		}
		if list.Items == nil {
			return errors.New("query deleting attempt-owned Kubernetes resources returned an ambiguous resource list")
		}
		if err := validateKtctlAttemptResourceState(list.Items, config.Namespace, attemptID, true); err != nil {
			return err
		}
		for _, resource := range list.Items {
			uid, found := expected[kubernetesResourceKey(resource)]
			if !found {
				return fmt.Errorf("unexpected attempt-owned resource %q appeared while waiting for deletion", kubernetesResourceKey(resource))
			}
			if resource.Metadata.UID != uid {
				return fmt.Errorf("attempt-owned resource %q was replaced while waiting for deletion", kubernetesResourceKey(resource))
			}
		}
		remaining := 0
		for _, resource := range deleted {
			exists, err := exactKtctlAttemptResourceExists(waitContext, kubectlPath, config, resource)
			if err != nil {
				return err
			}
			if exists {
				remaining++
			}
		}
		if remaining == 0 && len(list.Items) == 0 {
			return nil
		}
		timer := time.NewTimer(kubernetesProbeInterval)
		select {
		case <-waitContext.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func exactKtctlAttemptResourceExists(ctx context.Context, kubectlPath string, config ConnectionConfig, expected kubernetesResource) (bool, error) {
	resourceName := ""
	switch expected.Kind {
	case "Pod":
		resourceName = "pod"
	case "ConfigMap":
		resourceName = "configmap"
	default:
		return false, fmt.Errorf("unsupported resource kind %q", expected.Kind)
	}
	output, err := runKubernetesCommand(ctx, kubectlPath, kubernetesGlobalArgs(config, "get", resourceName, expected.Metadata.Name, "--ignore-not-found=true", "-o", "json")...)
	if err != nil {
		return false, fmt.Errorf("query exact deleting Kubernetes resource %s: %w", kubernetesResourceKey(expected), err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, nil
	}
	var current kubernetesResource
	if err := json.Unmarshal(output, &current); err != nil {
		return false, fmt.Errorf("query exact deleting Kubernetes resource %s returned malformed JSON: %w", kubernetesResourceKey(expected), err)
	}
	if kubernetesResourceKey(current) != kubernetesResourceKey(expected) {
		return false, fmt.Errorf("query exact deleting Kubernetes resource %s returned a different resource %q", kubernetesResourceKey(expected), kubernetesResourceKey(current))
	}
	if current.Metadata.UID == "" {
		return false, fmt.Errorf("query exact deleting Kubernetes resource %s returned no UID", kubernetesResourceKey(expected))
	}
	if current.Metadata.UID != expected.Metadata.UID {
		return false, fmt.Errorf("attempt-owned resource %q was replaced while waiting for deletion", kubernetesResourceKey(expected))
	}
	return true, nil
}

func kubernetesResourceKey(resource kubernetesResource) string {
	return resource.Kind + "/" + resource.Metadata.Namespace + "/" + resource.Metadata.Name
}

func deleteKtctlAttemptResource(ctx context.Context, kubectlPath string, config ConnectionConfig, resource kubernetesResource) error {
	resourceName := ""
	switch resource.Kind {
	case "Pod":
		resourceName = "pods"
	case "ConfigMap":
		resourceName = "configmaps"
	default:
		return fmt.Errorf("unsupported resource kind %q", resource.Kind)
	}
	deleteOptions := struct {
		APIVersion    string `json:"apiVersion"`
		Kind          string `json:"kind"`
		Preconditions struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}{
		APIVersion: "v1",
		Kind:       "DeleteOptions",
	}
	deleteOptions.Preconditions.UID = resource.Metadata.UID
	body, err := json.Marshal(deleteOptions)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("/api/v1/namespaces/%s/%s/%s", url.PathEscape(resource.Metadata.Namespace), resourceName, url.PathEscape(resource.Metadata.Name))
	_, err = runKubernetesCommandWithInput(ctx, kubectlPath, kubernetesCommandTimeout, body, kubernetesGlobalArgs(config, "delete", "--raw="+rawURL, "-f", "-")...)
	return err
}

func ktctlRetrySafetyError(config ConnectionConfig) error {
	if config.Kubeconfig == "" {
		return errors.New("automatic retry requires an explicit kubeconfig so ktctl and kubectl audit the same cluster")
	}
	if config.Context == "" {
		return errors.New("automatic retry requires a pinned Kubernetes context so ktctl attempts and kubectl audits cannot switch clusters")
	}
	shareShadow, _, err := ktctlBoolFlagValue(config.Args, "--shareShadow")
	if err != nil {
		return err
	}
	if shareShadow {
		return errors.New("automatic retry is disabled when ktctl shared shadow mode is enabled")
	}
	shadowDeployment, _, err := ktctlBoolFlagValue(config.Args, "--useShadowDeployment")
	if err != nil {
		return err
	}
	if shadowDeployment {
		return errors.New("automatic retry is disabled when ktctl shadow deployment mode is enabled")
	}
	useLocalTime, configured, err := ktctlBoolFlagValue(config.Args, "--useLocalTime")
	if err != nil {
		return err
	}
	if configured && !useLocalTime {
		return errors.New("automatic retry is disabled when ktctl local time mode is explicitly disabled because a separate rectifier Pod may own the CREATE EOF")
	}
	skipCleanup, configured, err := ktctlBoolFlagValue(config.Args, "--skipCleanup")
	if err != nil {
		return err
	}
	if configured && !skipCleanup {
		return errors.New("automatic retry is disabled when ktctl namespace-wide cleanup is explicitly enabled")
	}
	return nil
}

func observeKtctlAttemptResources(ctx context.Context, kubectlPath string, config ConnectionConfig, attemptID string, stableEmptyObservations int) ([]kubernetesResource, error) {
	selector := ktctlAttemptMetadataKey + "=" + attemptID
	startedAt := time.Now()
	emptyObservations := 0
	for {
		output, err := runKubernetesCommand(ctx, kubectlPath, kubernetesGlobalArgs(config, "get", "pods,configmaps", "--selector", selector, "-o", "json")...)
		if err != nil {
			return nil, fmt.Errorf("audit attempt-owned Kubernetes resources: %w", err)
		}
		var list kubernetesResourceList
		if err := json.Unmarshal(output, &list); err != nil {
			return nil, fmt.Errorf("audit attempt-owned Kubernetes resources returned malformed JSON: %w", err)
		}
		if list.Items == nil {
			return nil, errors.New("audit attempt-owned Kubernetes resources returned an ambiguous resource list")
		}
		if err := validateKtctlAttemptResources(list.Items, config.Namespace, attemptID); err != nil {
			return nil, err
		}
		if len(list.Items) != 0 {
			return list.Items, nil
		}
		emptyObservations++
		if emptyObservations >= stableEmptyObservations && time.Since(startedAt) >= kubernetesRecoveryObservationWindow {
			return nil, nil
		}
		timer := time.NewTimer(kubernetesProbeInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func validateKtctlAttemptResources(resources []kubernetesResource, namespace string, attemptID string) error {
	return validateKtctlAttemptResourceState(resources, namespace, attemptID, false)
}

func validateKtctlAttemptResourceState(resources []kubernetesResource, namespace string, attemptID string, allowTerminating bool) error {
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if resource.Kind != "Pod" && resource.Kind != "ConfigMap" {
			return fmt.Errorf("attempt audit returned unsupported resource kind %q", resource.Kind)
		}
		if resource.Metadata.Name == "" || resource.Metadata.Namespace == "" || resource.Metadata.UID == "" {
			return errors.New("attempt audit returned a resource without exact name, namespace, and UID")
		}
		if namespace != "" && resource.Metadata.Namespace != namespace {
			return fmt.Errorf("attempt audit returned resource %q in unexpected namespace %q", resource.Metadata.Name, resource.Metadata.Namespace)
		}
		if resource.Metadata.Labels[ktctlAttemptMetadataKey] != attemptID {
			return fmt.Errorf("attempt audit returned resource %q with mismatched ownership metadata", resource.Metadata.Name)
		}
		if resource.Kind == "Pod" && resource.Metadata.Annotations[ktctlAttemptMetadataKey] != attemptID {
			return fmt.Errorf("attempt audit returned Pod %q with mismatched ownership annotation", resource.Metadata.Name)
		}
		if !allowTerminating && len(resource.Metadata.DeletionTimestamp) != 0 && string(resource.Metadata.DeletionTimestamp) != "null" {
			return fmt.Errorf("attempt-owned resource %q is terminating", resource.Metadata.Name)
		}
		key := kubernetesResourceKey(resource)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("attempt audit returned duplicate resource %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}
