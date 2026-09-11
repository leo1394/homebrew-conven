package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeKubectl(t *testing.T, directory string, script string) string {
	t.Helper()
	path := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func writeStableKubectl(t *testing.T, directory string) string {
	t.Helper()
	return writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get --raw=/readyz "*) printf 'ok\n'; exit 0 ;;
  *" get pods,configmaps "*)
    if [ -n "$CONVEN_TEST_RESOURCE_STATE" ] && [ -f "$CONVEN_TEST_RESOURCE_STATE" ]; then
      selector=""
      previous=""
      for argument in "$@"; do
        if [ "$previous" = "--selector" ]; then selector="$argument"; fi
        previous="$argument"
      done
      id=${selector#*=}
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"owned-pod-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*)
    if [ -n "$CONVEN_TEST_RESOURCE_STATE" ] && [ -f "$CONVEN_TEST_RESOURCE_STATE" ]; then
      printf '{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"owned-pod-uid"}}\n'
    fi
    exit 0 ;;
  *" delete "*)
    cat >/dev/null
    if [ -n "$CONVEN_TEST_RESOURCE_STATE" ]; then rm -f "$CONVEN_TEST_RESOURCE_STATE"; fi
    exit 0 ;;
esac
exit 1
`)
}

func writeKubeconfig(t *testing.T, directory string, currentContext string) string {
	t.Helper()
	path := filepath.Join(directory, "kubeconfig")
	content := "apiVersion: v1\nkind: Config\ncurrent-context: " + currentContext + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func closedConnectionEndpoint(t *testing.T) ConnectionEndpoint {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return ConnectionEndpoint{Name: "cluster-api", Address: address}
}

func TestWaitForKubernetesAPIRequiresThreeStableSuccesses(t *testing.T) {
	directory := t.TempDir()
	countPath := filepath.Join(directory, "probe-count")
	argsPath := filepath.Join(directory, "probe-args")
	writeKubectl(t, directory, `#!/bin/sh
count=0
if [ -f "$CONVEN_TEST_PROBE_COUNT" ]; then count=$(cat "$CONVEN_TEST_PROBE_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CONVEN_TEST_PROBE_COUNT"
printf '%s\n' "$*" >> "$CONVEN_TEST_PROBE_ARGS"
if [ "$count" -le 2 ]; then echo transient >&2; exit 1; fi
exit 0
`)
	t.Setenv("CONVEN_TEST_PROBE_COUNT", countPath)
	t.Setenv("CONVEN_TEST_PROBE_ARGS", argsPath)
	var output bytes.Buffer
	if err := waitForKubernetesAPI(context.Background(), ConnectionConfig{Kubeconfig: "/tmp/config", Context: "dev"}, &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "5" {
		t.Fatalf("probe count = %q, want two failures followed by three successes", data)
	}
	data, err = os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, "--kubeconfig /tmp/config --context dev --request-timeout=5s get --raw=/readyz") {
			t.Fatalf("readyz args = %q", line)
		}
	}
	if !strings.Contains(output.String(), "Kubernetes API stable") {
		t.Fatalf("gate output = %q", output.String())
	}
}

func TestWaitForKubernetesAPIRetriesAfterHungProbe(t *testing.T) {
	directory := t.TempDir()
	countPath := filepath.Join(directory, "probe-count")
	childPIDPath := filepath.Join(directory, "child-pid")
	writeKubectl(t, directory, `#!/bin/sh
count=0
if [ -f "$CONVEN_TEST_PROBE_COUNT" ]; then count=$(cat "$CONVEN_TEST_PROBE_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CONVEN_TEST_PROBE_COUNT"
if [ "$count" -eq 1 ]; then
  sleep 20 &
  printf '%s\n' "$!" > "$CONVEN_TEST_CHILD_PID"
  wait
fi
exit 0
`)
	t.Setenv("CONVEN_TEST_PROBE_COUNT", countPath)
	t.Setenv("CONVEN_TEST_CHILD_PID", childPIDPath)
	started := time.Now()
	if err := waitForKubernetesAPI(context.Background(), ConnectionConfig{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if elapsed < kubernetesCommandTimeout || elapsed >= kubernetesAPIGateTimeout {
		t.Fatalf("gate elapsed = %s, want one bounded command timeout followed by recovery", elapsed)
	}
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "4" {
		t.Fatalf("probe count = %q, want one timeout followed by three successes", data)
	}
	data, err = os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if ProcessAlive(childPID) {
		t.Fatalf("timed-out kubectl descendant pid %d is still running", childPID)
	}
}

func TestWaitForKubernetesAPIFailsClosedOnAmbiguousTimeout(t *testing.T) {
	directory := t.TempDir()
	writeKubectl(t, directory, "#!/bin/sh\necho unavailable >&2\nexit 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := waitForKubernetesAPI(ctx, ConnectionConfig{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "0/3") || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("gate error = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate error does not retain context deadline: %v", err)
	}
}

func TestWaitForKubernetesAPIFallsBackToKtctl(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	argsPath := filepath.Join(directory, "birdseye-args")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CONVEN_TEST_BIRDSEYE_ARGS\"\nprintf 'INF ---- Service in namespace rea ----\\n' >&2\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_BIRDSEYE_ARGS", argsPath)
	config := ConnectionConfig{Command: ktctl, Kubeconfig: "/tmp/config", Context: "test", Namespace: "rea"}
	if err := waitForKubernetesAPI(context.Background(), config, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != kubernetesAPIStableProbes {
		t.Fatalf("birdseye probe count = %d, want %d", len(lines), kubernetesAPIStableProbes)
	}
	for _, line := range lines {
		if line != "--kubeconfig=/tmp/config --context=test --namespace=rea --useLocalTime=true birdseye --hideNaturalService" {
			t.Fatalf("birdseye args = %q", line)
		}
	}
}

func TestWaitForKubernetesAPIFallbackRejectsKtctlErrorWithZeroExit(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nprintf 'ERR Exit: failed to reach Kubernetes API\\n' >&2\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := waitForKubernetesAPI(ctx, ConnectionConfig{Command: ktctl}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "reported an error despite returning success") {
		t.Fatalf("ktctl zero-exit error = %v", err)
	}
}

func TestWaitForKubernetesAPIFallbackRequiresAffirmativeResponse(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := waitForKubernetesAPI(ctx, ConnectionConfig{Command: ktctl}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "without an affirmative Kubernetes service response") {
		t.Fatalf("ambiguous ktctl success error = %v", err)
	}
}

func TestWaitForKubernetesAPIRequiresProbeTool(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	err := waitForKubernetesAPI(context.Background(), ConnectionConfig{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requires kubectl or") {
		t.Fatalf("missing Kubernetes probe tool error = %v", err)
	}
}

func TestPinKtctlKubernetesTargetFreezesCurrentContext(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	original := ConnectionConfig{Driver: "ktctl", Kubeconfig: kubeconfig}
	config, err := pinKtctlKubernetesTarget(original)
	if err != nil {
		t.Fatal(err)
	}
	if config.Context != "test-context" || config.Namespace != "default" {
		t.Fatalf("pinned target = context %q namespace %q", config.Context, config.Namespace)
	}
	writeKubeconfig(t, directory, "other-context")
	pinnedAgain, err := pinKtctlKubernetesTarget(config)
	if err != nil {
		t.Fatal(err)
	}
	if pinnedAgain.Context != "test-context" {
		t.Fatalf("pinned context changed after kubeconfig update: %q", pinnedAgain.Context)
	}
	nextInvocation, err := pinKtctlKubernetesTarget(original)
	if err != nil {
		t.Fatal(err)
	}
	if nextInvocation.Context != "other-context" || connectionFingerprint(nextInvocation) == connectionFingerprint(config) {
		t.Fatalf("new invocation did not resolve a distinct current context: %#v", nextInvocation)
	}
}

func TestPinKtctlKubernetesTargetRejectsEmptyCurrentContext(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := writeKubeconfig(t, directory, "")
	_, err := pinKtctlKubernetesTarget(ConnectionConfig{Kubeconfig: kubeconfig})
	if err == nil || !strings.Contains(err.Error(), "current-context is empty") {
		t.Fatalf("empty current-context error = %v", err)
	}
}

func TestPinKtctlKubernetesTargetRequiresExplicitKubeconfig(t *testing.T) {
	_, err := pinKtctlKubernetesTarget(ConnectionConfig{})
	if err == nil || !strings.Contains(err.Error(), "explicit kubeconfig") {
		t.Fatalf("missing kubeconfig error = %v", err)
	}
}

func TestMaterializeKtctlKubeconfigSnapshotFreezesClusterMapping(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := t.TempDir()
	kubeconfig := filepath.Join(directory, "source-kubeconfig")
	first := []byte("apiVersion: v1\nkind: Config\ncurrent-context: test-context\ncontexts:\n- name: test-context\n  context:\n    cluster: test-cluster\nclusters:\n- name: test-cluster\n  cluster:\n    server: https://first.example\n")
	second := []byte("apiVersion: v1\nkind: Config\ncurrent-context: test-context\ncontexts:\n- name: test-context\n  context:\n    cluster: test-cluster\nclusters:\n- name: test-cluster\n  cluster:\n    server: https://second.example\n")
	if err := os.WriteFile(kubeconfig, first, 0600); err != nil {
		t.Fatal(err)
	}
	original := ConnectionConfig{Driver: "ktctl", Kubeconfig: kubeconfig, Context: "test-context", Namespace: "test"}
	pinned, err := pinKtctlKubernetesTarget(original)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := connectionFingerprint(pinned)
	snapshot, err := materializeKtctlKubeconfigSnapshot(pinned, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer removeKtctlKubeconfigSnapshot(fingerprint)
	if snapshot == kubeconfig {
		t.Fatalf("private snapshot reused mutable source path %q", snapshot)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private snapshot permissions = %o, want 600", info.Mode().Perm())
	}
	if err := os.WriteFile(kubeconfig, second, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("https://first.example")) || bytes.Contains(data, []byte("https://second.example")) {
		t.Fatalf("private snapshot changed with source kubeconfig: %q", data)
	}
	fresh, err := pinKtctlKubernetesTarget(original)
	if err != nil {
		t.Fatal(err)
	}
	if connectionFingerprint(fresh) == fingerprint {
		t.Fatal("changed cluster mapping did not produce a new connection fingerprint")
	}
}

func TestPinKtctlKubernetesTargetResolvesRelativeSnapshotPaths(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := filepath.Join(directory, "kubeconfig")
	data := []byte("apiVersion: v1\nkind: Config\ncurrent-context: test-context\nclusters:\n- name: test-cluster\n  cluster:\n    certificate-authority: certs/ca.pem\nusers:\n- name: test-user\n  user:\n    client-certificate: credentials/client.pem\n    client-key: credentials/client-key.pem\n    tokenFile: credentials/token\n    exec:\n      command: plugins/auth-helper\n- name: path-user\n  user:\n    exec:\n      command: aws\n- name: nullable-user\n  user:\n    client-certificate: null\n    client-key:\n    tokenFile: null\n    exec: null\n")
	if err := os.WriteFile(kubeconfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := pinKtctlKubernetesTarget(ConnectionConfig{Driver: "ktctl", Kubeconfig: kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	view := struct {
		Clusters []struct {
			Cluster struct {
				CertificateAuthority string `yaml:"certificate-authority"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			User struct {
				ClientCertificate string `yaml:"client-certificate"`
				ClientKey         string `yaml:"client-key"`
				TokenFile         string `yaml:"tokenFile"`
				Exec              struct {
					Command string `yaml:"command"`
				} `yaml:"exec"`
			} `yaml:"user"`
		} `yaml:"users"`
	}{}
	if err := yaml.Unmarshal(config.kubeconfigSnapshot, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Clusters) != 1 || len(view.Users) != 3 {
		t.Fatalf("resolved kubeconfig shape = %#v", view)
	}
	for actual, relative := range map[string]string{
		view.Clusters[0].Cluster.CertificateAuthority: "certs/ca.pem",
		view.Users[0].User.ClientCertificate:          "credentials/client.pem",
		view.Users[0].User.ClientKey:                  "credentials/client-key.pem",
		view.Users[0].User.TokenFile:                  "credentials/token",
		view.Users[0].User.Exec.Command:               "plugins/auth-helper",
	} {
		expected := filepath.Join(directory, relative)
		if actual != expected {
			t.Fatalf("resolved path = %q, want %q", actual, expected)
		}
	}
	if view.Users[1].User.Exec.Command != "aws" {
		t.Fatalf("PATH-based exec command = %q, want aws", view.Users[1].User.Exec.Command)
	}
	if view.Users[2].User.ClientCertificate != "" || view.Users[2].User.ClientKey != "" || view.Users[2].User.TokenFile != "" || view.Users[2].User.Exec.Command != "" {
		t.Fatalf("nullable user paths were not preserved as absent: %#v", view.Users[2].User)
	}
}

func TestPinKtctlKubernetesTargetResolvesAliasedAndMergedPaths(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := filepath.Join(directory, "kubeconfig")
	data := []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: base-cluster\n  cluster: &cluster-settings\n    server: https://cluster.example\n    certificate-authority: certs/ca.pem\n- name: merged-cluster\n  cluster:\n    <<: *cluster-settings\nusers:\n- name: base-user\n  user: &user-settings\n    client-key: credentials/client-key.pem\n- name: aliased-user\n  user: *user-settings\n")
	if err := os.WriteFile(kubeconfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := pinKtctlKubernetesTarget(ConnectionConfig{Driver: "ktctl", Kubeconfig: kubeconfig, Context: "test-context"})
	if err != nil {
		t.Fatal(err)
	}
	view := struct {
		Clusters []struct {
			Cluster struct {
				CertificateAuthority string `yaml:"certificate-authority"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			User struct {
				ClientKey string `yaml:"client-key"`
			} `yaml:"user"`
		} `yaml:"users"`
	}{}
	if err := yaml.Unmarshal(config.kubeconfigSnapshot, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Clusters) != 2 || len(view.Users) != 2 {
		t.Fatalf("resolved aliased kubeconfig shape = %#v", view)
	}
	expectedCA := filepath.Join(directory, "certs/ca.pem")
	for _, cluster := range view.Clusters {
		if cluster.Cluster.CertificateAuthority != expectedCA {
			t.Fatalf("aliased certificate authority = %q, want %q", cluster.Cluster.CertificateAuthority, expectedCA)
		}
	}
	expectedKey := filepath.Join(directory, "credentials/client-key.pem")
	for _, user := range view.Users {
		if user.User.ClientKey != expectedKey {
			t.Fatalf("aliased client key = %q, want %q", user.User.ClientKey, expectedKey)
		}
	}
}

func TestKtctlAttemptMetadataMergeAndCollision(t *testing.T) {
	args, err := ktctlArgsWithAttemptMetadata([]string{
		"--withLabel", "team=rea,tier=dev",
		"--withAnnotation=owner=conven",
		"--debug",
	}, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--withLabel", "team=rea,tier=dev,conven.dev/connection-attempt=abc123",
		"--withAnnotation=owner=conven,conven.dev/connection-attempt=abc123",
		"--debug",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("attempt args = %#v, want %#v", args, want)
	}
	for _, collision := range [][]string{
		{"--withLabel", "conven.dev/connection-attempt=user"},
		{"--withAnnotation=owner=x,conven.dev/connection-attempt=user"},
	} {
		if _, err := ktctlArgsWithAttemptMetadata(collision, "abc123"); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("collision args %#v error = %v", collision, err)
		}
	}
	for _, duplicates := range [][]string{
		{"--withLabel", "team=rea", "--withLabel=region=cn"},
		{"-l", "team=rea", "--withLabel=region=cn"},
		{"--withAnnotation=owner=conven", "--withAnnotation", "trace=on"},
	} {
		if _, err := ktctlArgsWithAttemptMetadata(duplicates, "abc123"); err == nil || !strings.Contains(err.Error(), "only be specified once") {
			t.Fatalf("duplicate metadata args %#v error = %v", duplicates, err)
		}
	}
}

func TestKtctlAttemptArgsAddSafeDefaults(t *testing.T) {
	args, err := ktctlArgsForAttempt(ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
		Args:       []string{"--debug"},
	}, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--shareShadow=false",
		"--useShadowDeployment=false",
		"--useLocalTime=true",
		"--skipCleanup=true",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("attempt args = %#v, missing %q", args, expected)
		}
	}
}

func TestKtctlAttemptArgsPreserveExplicitModesAndForceManagedSafety(t *testing.T) {
	args, err := ktctlArgsForAttempt(ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
		Args: []string{
			"--shareShadow=false",
			"--useShadowDeployment=false",
			"--useLocalTime=true",
			"--skipCleanup",
		},
	}, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if localTime, present, parseErr := ktctlBoolFlagValue(args, "--useLocalTime"); parseErr != nil || !present || !localTime {
		t.Fatalf("managed local time option is not enabled: args=%#v error=%v", args, parseErr)
	}
	if skipCleanup, present, parseErr := ktctlBoolFlagValue(args, "--skipCleanup"); parseErr != nil || !present || !skipCleanup {
		t.Fatalf("managed cleanup isolation is not enabled: args=%#v error=%v", args, parseErr)
	}
	if strings.Contains(joined, "--kubeconfig=") || strings.Contains(joined, "--context=") || strings.Contains(joined, "--namespace=default") {
		t.Fatalf("attempt args duplicated configured Kubernetes target: %#v", args)
	}
}

func TestKtctlConnectionArgsRejectUnsafeManagedOverrides(t *testing.T) {
	for _, argument := range []string{"--useLocalTime=false", "--skipCleanup=false"} {
		err := validateKtctlConnectionArgs([]string{argument})
		if err == nil || !strings.Contains(err.Error(), "isolated recovery") {
			t.Fatalf("unsafe managed argument %q error = %v", argument, err)
		}
	}
}

func TestKtctlArgumentParsingSkipsOptionValues(t *testing.T) {
	input := []string{
		"--image", "--withLabel=fake-image-value",
		"--serviceAccount", "--useLocalTime=true",
		"--nodeSelector", "disk=ssd",
	}
	if err := validateKtctlConnectionArgs(input); err != nil {
		t.Fatal(err)
	}
	if _, present, err := ktctlBoolFlagValue(input, "--useLocalTime"); err != nil || present {
		t.Fatalf("service account value was treated as a local time flag: present=%t error=%v", present, err)
	}
	args, err := ktctlArgsForAttempt(ConnectionConfig{Args: input}, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if localTime, present, parseErr := ktctlBoolFlagValue(args, "--useLocalTime"); parseErr != nil || !present || !localTime {
		t.Fatalf("managed local time flag was not appended after option values: args=%#v error=%v", args, parseErr)
	}
	if args[1] != "--withLabel=fake-image-value" {
		t.Fatalf("image value was modified as ownership metadata: %#v", args)
	}
}

func TestKtctlConnectionArgsRejectKubernetesTargetOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"--kubeconfig", "/other/config"},
		{"-c", "/other/config"},
		{"-c/other/config"},
		{"--context=other"},
		{"--namespace", "other"},
		{"-n", "other"},
		{"-nother"},
	} {
		if err := validateKtctlConnectionArgs(args); err == nil || !strings.Contains(err.Error(), "connection fields") {
			t.Fatalf("target override args %#v error = %v", args, err)
		}
	}
}

func TestKtctlConnectionArgsRejectBundledShortOverrides(t *testing.T) {
	for _, argument := range []string{"-dnother", "-dlother=y", "-fcnfig", "-hnother", "-vnother"} {
		err := validateKtctlConnectionArgs([]string{argument})
		if err == nil || !strings.Contains(err.Error(), "bundled short argument") {
			t.Fatalf("bundled argument %q error = %v", argument, err)
		}
	}
	for _, argument := range []string{"-d", "-f", "-lteam=rea", "-icustom-image"} {
		if err := validateKtctlConnectionArgs([]string{argument}); err != nil {
			t.Fatalf("standalone argument %q error = %v", argument, err)
		}
	}
}

func TestKtctlConnectionArgsRejectMetadataTerminator(t *testing.T) {
	err := validateKtctlConnectionArgs([]string{"--debug", "--"})
	if err == nil || !strings.Contains(err.Error(), "connection-attempt ownership metadata") {
		t.Fatalf("argument terminator error = %v", err)
	}
}

func TestKtctlAttemptMetadataMergesShortLabelForms(t *testing.T) {
	for _, input := range [][]string{{"-l", "team=rea"}, {"-l=team=rea"}, {"-lteam=rea"}} {
		args, err := ktctlArgsWithAttemptMetadata(input, "abc123")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(args, " "), "team=rea,"+ktctlAttemptMetadataKey+"=abc123") {
			t.Fatalf("short label args %#v did not retain ownership metadata: %#v", input, args)
		}
	}
}

func TestRunKubernetesCommandSeparatesSuccessfulStderr(t *testing.T) {
	directory := t.TempDir()
	kubectl := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '{\"items\":[]}\\n'\necho warning >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	resources, err := observeKtctlAttemptResources(context.Background(), kubectl, ConnectionConfig{}, "abc123", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("resources = %#v, want empty", resources)
	}
}

func TestRunKubernetesCommandIncludesSanitizedStderrOnlyOnFailure(t *testing.T) {
	directory := t.TempDir()
	kubectl := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf 'stdout detail\\n'\nprintf '\\033[31mstderr detail\\033[0m\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := runKubernetesCommand(context.Background(), kubectl)
	if err == nil || !strings.Contains(err.Error(), "stderr detail") || strings.Contains(err.Error(), "stdout detail") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("command error = %q", err)
	}
}

func TestObserveKtctlAttemptResourcesCatchesDelayedCreate(t *testing.T) {
	directory := t.TempDir()
	countPath := filepath.Join(directory, "audit-count")
	kubectl := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(kubectl, []byte(`#!/bin/sh
count=0
if [ -f "$CONVEN_TEST_AUDIT_COUNT" ]; then count=$(cat "$CONVEN_TEST_AUDIT_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CONVEN_TEST_AUDIT_COUNT"
if [ "$count" -le 3 ]; then
  printf '{"items":[]}\n'
else
  selector=""
  previous=""
  for argument in "$@"; do
    if [ "$previous" = "--selector" ]; then selector="$argument"; fi
    previous="$argument"
  done
  id=${selector#*=}
  printf '{"items":[{"kind":"ConfigMap","metadata":{"name":"kt-connect-shadow","namespace":"test","uid":"configmap-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"kt-connect.io/heartbeat":"1"}}}]}\n' "$id"
fi
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_AUDIT_COUNT", countPath)
	resources, err := observeKtctlAttemptResources(context.Background(), kubectl, ConnectionConfig{Namespace: "test"}, "attempt", kubernetesRecoveryStableObservations)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].Metadata.UID != "configmap-uid" {
		t.Fatalf("delayed resources = %#v", resources)
	}
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "4" {
		t.Fatalf("audit count = %q, want a fourth observation after the minimum safety window", data)
	}
}

func TestRecoverKtctlAttemptRequiresObservableOwnedPod(t *testing.T) {
	directory := t.TempDir()
	deletePath := filepath.Join(directory, "delete-called")
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get pods,configmaps "*)
    selector=""
    previous=""
    for argument in "$@"; do
      if [ "$previous" = "--selector" ]; then selector="$argument"; fi
      previous="$argument"
    done
    id=${selector#*=}
    printf '{"items":[{"kind":"ConfigMap","metadata":{"name":"kt-connect","namespace":"test","uid":"configmap-uid","labels":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id"
    exit 0 ;;
  *" delete "*) touch "$CONVEN_TEST_DELETE_CALLED"; exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_DELETE_CALLED", deletePath)
	err := recoverKtctlAttempt(context.Background(), ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
	}, "attempt")
	if err == nil || !strings.Contains(err.Error(), "attempt-owned Pod did not become observable") {
		t.Fatalf("ConfigMap-only recovery error = %v", err)
	}
	if _, statErr := os.Stat(deletePath); !os.IsNotExist(statErr) {
		t.Fatalf("ConfigMap-only recovery attempted deletion: %v", statErr)
	}
}

func TestNewKtctlAttemptIDIsUniqueAndKubernetesValid(t *testing.T) {
	first, err := newKtctlAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newKtctlAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("attempt IDs are not unique Kubernetes-safe hex values: %q %q", first, second)
	}
}

func TestEnsureConnectionRetriesPodCreateEOFAfterOwnedResidualCleanup(t *testing.T) {
	directory := t.TempDir()
	writeStableKubectl(t, directory)
	process, attempts := ensureRetryingKtctl(t, directory, nil)
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	if strings.TrimSpace(string(attempts)) != "2" {
		t.Fatalf("connection attempts = %q, want two", attempts)
	}
	command := strings.Join(process.Command, " ")
	if strings.Count(command, ktctlAttemptMetadataKey) != 2 {
		t.Fatalf("successful command does not contain actual attempt metadata: %#v", process.Command)
	}
	data, err := os.ReadFile(filepath.Join(directory, "ktctl-args"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Fatalf("attempt argv did not receive unique identities: %q", data)
	}
	for _, line := range lines {
		for _, expected := range []string{"--kubeconfig ", "--context test-context", "--namespace test", "--shareShadow=false", "--useShadowDeployment=false", "--useLocalTime=true", "--skipCleanup=true"} {
			if !strings.Contains(line, expected) {
				t.Fatalf("managed ktctl args %q do not contain %q", line, expected)
			}
		}
		if strings.Contains(line, "--kubeconfig "+filepath.Join(directory, "kubeconfig")) {
			t.Fatalf("managed ktctl used mutable source kubeconfig: %q", line)
		}
	}
}

func TestEnsureKtctlDoesNotReuseTCPOnlyExternalConnection(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeStableKubectl(t, directory)
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	startedPath := filepath.Join(directory, "ktctl-started")
	ktctl := filepath.Join(directory, "ktctl")
	script := `#!/bin/sh
printf 'started\n' > "$CONVEN_TEST_KTCTL_STARTED"
printf 'INF All looks good, now you can access to resources in the kubernetes cluster\n'
while :; do sleep 1; done
`
	if err := os.WriteFile(ktctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_KTCTL_STARTED", startedPath)
	var output bytes.Buffer
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Kubeconfig: kubeconfig,
		Context:    "test-context",
		Namespace:  "test",
		Timeout:    4 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "apollo", Address: listener.Addr().String()}},
	}, filepath.Join(directory, "connection.log"), "managed-workspace", &output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = releaseConnection(context.Background(), process, "managed-workspace", true, io.Discard)
	}()
	if !process.Owned || !process.Managed {
		t.Fatalf("ktctl process = %#v, want owned managed connection", process)
	}
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("ktctl was not started: %v", err)
	}
	if strings.Contains(output.String(), "reusing the external connection") {
		t.Fatalf("TCP-only endpoint was treated as an external ktctl connection: %s", output.String())
	}
}

func TestProxyFakeIPRangeIsRejected(t *testing.T) {
	for _, address := range []string{"198.18.0.1", "198.19.255.254"} {
		if !isProxyFakeIP(net.ParseIP(address)) {
			t.Fatalf("proxy Fake-IP %s was accepted", address)
		}
	}
	for _, address := range []string{"198.17.255.255", "198.20.0.1", "10.2.100.92", "::1"} {
		if isProxyFakeIP(net.ParseIP(address)) {
			t.Fatalf("non Fake-IP %s was rejected", address)
		}
	}
	diagnostics, ready := probeConnectionEndpoints(context.Background(), []ConnectionEndpoint{{Name: "apollo", Address: "198.18.67.209:8080", rejectProxyFakeIP: true}})
	if ready || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Detail, "proxy Fake-IP 198.18.67.209") {
		t.Fatalf("Fake-IP readiness diagnostics = %#v, ready=%t", diagnostics, ready)
	}
}

func TestEnsureConnectionDoesNotRetryPodCreateEOFWithoutObservableResidual(t *testing.T) {
	directory := t.TempDir()
	writeStableKubectl(t, directory)
	process, attempts, err := runKtctlScenario(t, directory, nil, true, false)
	if process != nil {
		t.Fatalf("ambiguous CREATE returned residual process: %#v", process)
	}
	if err == nil || !strings.Contains(err.Error(), "no attempt-owned Kubernetes resource became observable") || !strings.Contains(err.Error(), "automatic retry was not safe") {
		t.Fatalf("ambiguous CREATE recovery error = %v", err)
	}
	if strings.TrimSpace(string(attempts)) != "1" {
		t.Fatalf("connection attempts = %q, want one", attempts)
	}
}

func TestEnsureConnectionDeletesExactResidualBeforeRetry(t *testing.T) {
	directory := t.TempDir()
	resourceState := filepath.Join(directory, "resource-exists")
	deleteArgs := filepath.Join(directory, "delete-args")
	deleteBodies := filepath.Join(directory, "delete-bodies")
	auditLog := filepath.Join(directory, "audit-log")
	if err := os.WriteFile(resourceState, []byte("exists\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get --raw=/readyz "*) exit 0 ;;
  *" get pods,configmaps "*)
    printf 'get\n' >> "$CONVEN_TEST_AUDIT_LOG"
    selector=""
    previous=""
    for argument in "$@"; do
      if [ "$previous" = "--selector" ]; then selector="$argument"; fi
      previous="$argument"
    done
    id=${selector#*=}
    if [ -f "$CONVEN_TEST_RESOURCE_STATE" ]; then
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}},{"kind":"ConfigMap","metadata":{"name":"kt-connect","namespace":"test","uid":"configmap-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"kt-connect.io/heartbeat":"1"}}}]}\n' "$id" "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*|*" get configmap kt-connect "*) exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/pods/kt-connect -f - "*)
    printf '%s\n' "$*" >> "$CONVEN_TEST_DELETE_ARGS"
    cat >> "$CONVEN_TEST_DELETE_BODIES"
    printf '\n' >> "$CONVEN_TEST_DELETE_BODIES"
    exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/configmaps/kt-connect -f - "*)
    printf '%s\n' "$*" >> "$CONVEN_TEST_DELETE_ARGS"
    cat >> "$CONVEN_TEST_DELETE_BODIES"
    printf '\n' >> "$CONVEN_TEST_DELETE_BODIES"
    rm "$CONVEN_TEST_RESOURCE_STATE"
    exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_RESOURCE_STATE", resourceState)
	t.Setenv("CONVEN_TEST_DELETE_ARGS", deleteArgs)
	t.Setenv("CONVEN_TEST_DELETE_BODIES", deleteBodies)
	t.Setenv("CONVEN_TEST_AUDIT_LOG", auditLog)
	process, _ := ensureRetryingKtctl(t, directory, nil)
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	data, err := os.ReadFile(deleteArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--namespace test --request-timeout=5s delete --raw=/api/v1/namespaces/test/pods/kt-connect -f -") || !strings.Contains(string(data), "--namespace test --request-timeout=5s delete --raw=/api/v1/namespaces/test/configmaps/kt-connect -f -") {
		t.Fatalf("targeted delete args = %q", data)
	}
	data, err = os.ReadFile(deleteBodies)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"pod-uid", "configmap-uid"} {
		if !strings.Contains(string(data), `"uid":"`+uid+`"`) {
			t.Fatalf("delete request bodies = %q, missing UID precondition %q", data, uid)
		}
	}
	data, err = os.ReadFile(auditLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "get\n") != 2 {
		t.Fatalf("resource audit calls = %q, want discovery plus one post-delete observation", data)
	}
}

func TestRecoverKtctlAttemptWaitsForTerminatingOwnedPod(t *testing.T) {
	directory := t.TempDir()
	queryCount := filepath.Join(directory, "query-count")
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get pods,configmaps "*)
    count=0
    if [ -f "$CONVEN_TEST_QUERY_COUNT" ]; then count=$(cat "$CONVEN_TEST_QUERY_COUNT"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$CONVEN_TEST_QUERY_COUNT"
    selector=""
    previous=""
    for argument in "$@"; do
      if [ "$previous" = "--selector" ]; then selector="$argument"; fi
      previous="$argument"
    done
    id=${selector#*=}
    if [ "$count" -eq 1 ]; then
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    elif [ "$count" -le 3 ]; then
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid","deletionTimestamp":"2026-09-07T12:00:00Z","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*)
    count=0
    if [ -f "$CONVEN_TEST_QUERY_COUNT" ]; then count=$(cat "$CONVEN_TEST_QUERY_COUNT"); fi
    if [ "$count" -le 3 ]; then
      printf '{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid"}}\n'
    fi
    exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/pods/kt-connect -f - "*) cat >/dev/null; exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_QUERY_COUNT", queryCount)
	err := recoverKtctlAttempt(context.Background(), ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
	}, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(queryCount)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "4" {
		t.Fatalf("resource query count = %q, want active, two terminating, then absent", data)
	}
}

func TestRecoverKtctlAttemptConfirmsExactUIDAfterOwnershipLabelDisappears(t *testing.T) {
	directory := t.TempDir()
	selectorCount := filepath.Join(directory, "selector-count")
	exactCount := filepath.Join(directory, "exact-count")
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get pods,configmaps "*)
    count=0
    if [ -f "$CONVEN_TEST_SELECTOR_COUNT" ]; then count=$(cat "$CONVEN_TEST_SELECTOR_COUNT"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$CONVEN_TEST_SELECTOR_COUNT"
    if [ "$count" -eq 1 ]; then
      selector=""
      previous=""
      for argument in "$@"; do
        if [ "$previous" = "--selector" ]; then selector="$argument"; fi
        previous="$argument"
      done
      id=${selector#*=}
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*)
    count=0
    if [ -f "$CONVEN_TEST_EXACT_COUNT" ]; then count=$(cat "$CONVEN_TEST_EXACT_COUNT"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$CONVEN_TEST_EXACT_COUNT"
    if [ "$count" -le 2 ]; then
      printf '{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"pod-uid","deletionTimestamp":"2026-09-07T12:00:00Z"}}\n'
    fi
    exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/pods/kt-connect -f - "*) cat >/dev/null; exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_SELECTOR_COUNT", selectorCount)
	t.Setenv("CONVEN_TEST_EXACT_COUNT", exactCount)
	err := recoverKtctlAttempt(context.Background(), ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
	}, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exactCount)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "3" {
		t.Fatalf("exact UID query count = %q, want two present observations followed by absence", data)
	}
}

func TestRecoverKtctlAttemptRejectsReplacementAfterDelete(t *testing.T) {
	directory := t.TempDir()
	selectorState := filepath.Join(directory, "selector-state")
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get pods,configmaps "*)
    if [ ! -f "$CONVEN_TEST_SELECTOR_STATE" ]; then
      touch "$CONVEN_TEST_SELECTOR_STATE"
      selector=""
      previous=""
      for argument in "$@"; do
        if [ "$previous" = "--selector" ]; then selector="$argument"; fi
        previous="$argument"
      done
      id=${selector#*=}
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"original-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*)
    printf '{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"replacement-uid"}}\n'
    exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/pods/kt-connect -f - "*) cat >/dev/null; exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_SELECTOR_STATE", selectorState)
	err := recoverKtctlAttempt(context.Background(), ConnectionConfig{
		Kubeconfig: "/tmp/config",
		Context:    "test-context",
		Namespace:  "test",
	}, "attempt")
	if err == nil || !strings.Contains(err.Error(), "was replaced while waiting for deletion") {
		t.Fatalf("replacement recovery error = %v", err)
	}
}

func TestEnsureConnectionDoesNotRetryWhenUIDPreconditionDeleteFails(t *testing.T) {
	directory := t.TempDir()
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get --raw=/readyz "*) exit 0 ;;
  *" get pods,configmaps "*)
    selector=""
    previous=""
    for argument in "$@"; do
      if [ "$previous" = "--selector" ]; then selector="$argument"; fi
      previous="$argument"
    done
    id=${selector#*=}
    printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"original-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    exit 0 ;;
  *" delete --raw=/api/v1/namespaces/test/pods/kt-connect -f - "*)
    cat >/dev/null
    printf 'UID precondition failed: object was replaced\n' >&2
    exit 1 ;;
esac
exit 1
`)
	_, attempts, err := runFailingKtctl(t, directory, nil)
	if err == nil || !strings.Contains(err.Error(), "UID precondition failed") || !strings.Contains(err.Error(), "automatic retry was not safe") {
		t.Fatalf("UID conflict recovery error = %v", err)
	}
	if strings.TrimSpace(string(attempts)) != "1" {
		t.Fatalf("connection attempts = %q, want one", attempts)
	}
}

func TestEnsureConnectionGatesKubernetesImmediatelyBeforeRetry(t *testing.T) {
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events")
	writeKubectl(t, directory, `#!/bin/sh
case " $* " in
  *" get --raw=/readyz "*) printf 'probe\n' >> "$CONVEN_TEST_EVENTS"; exit 0 ;;
  *" get pods,configmaps "*)
    printf 'audit\n' >> "$CONVEN_TEST_EVENTS"
    if [ -f "$CONVEN_TEST_RESOURCE_STATE" ]; then
      selector=""
      previous=""
      for argument in "$@"; do
        if [ "$previous" = "--selector" ]; then selector="$argument"; fi
        previous="$argument"
      done
      id=${selector#*=}
      printf '{"items":[{"kind":"Pod","metadata":{"name":"kt-connect","namespace":"test","uid":"owned-pod-uid","labels":{"conven.dev/connection-attempt":"%s"},"annotations":{"conven.dev/connection-attempt":"%s"}}}]}\n' "$id" "$id"
    else
      printf '{"items":[]}\n'
    fi
    exit 0 ;;
  *" get pod kt-connect "*) exit 0 ;;
  *" delete "*) printf 'delete\n' >> "$CONVEN_TEST_EVENTS"; cat >/dev/null; rm "$CONVEN_TEST_RESOURCE_STATE"; exit 0 ;;
esac
exit 1
`)
	t.Setenv("CONVEN_TEST_EVENTS", eventsPath)
	process, _ := ensureRetryingKtctl(t, directory, nil)
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Fields(string(data))
	secondConnect := -1
	for index, event := range events {
		if event == "connect-2" {
			secondConnect = index
			break
		}
	}
	if secondConnect < kubernetesAPIStableProbes {
		t.Fatalf("retry event order = %#v", events)
	}
	for _, event := range events[secondConnect-kubernetesAPIStableProbes : secondConnect] {
		if event != "probe" {
			t.Fatalf("retry was not immediately preceded by stable API probes: %#v", events)
		}
	}
	if !strings.Contains(strings.Join(events[:secondConnect-kubernetesAPIStableProbes], " "), "audit") {
		t.Fatalf("retry preflight occurred before attempt resource audit: %#v", events)
	}
}

func TestEnsureConnectionDoesNotRetryUnsupportedKtctlMode(t *testing.T) {
	tests := []struct {
		argument string
		message  string
	}{
		{argument: "--shareShadow", message: "shared shadow mode"},
		{argument: "--useShadowDeployment=true", message: "shadow deployment mode"},
	}
	for _, test := range tests {
		t.Run(test.argument, func(t *testing.T) {
			directory := t.TempDir()
			writeStableKubectl(t, directory)
			_, attempts, err := runFailingKtctl(t, directory, []string{test.argument})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("unsupported mode error = %v", err)
			}
			if strings.TrimSpace(string(attempts)) != "1" {
				t.Fatalf("connection attempts = %q, want one", attempts)
			}
		})
	}
}

func TestEnsureConnectionDoesNotRetryPodCreateEOFWithoutKubectl(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	if err := os.Symlink("/bin/ps", filepath.Join(directory, "ps")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	attemptsPath := filepath.Join(directory, "attempts")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte(`#!/bin/sh
case " $* " in
  *" birdseye --hideNaturalService "*) printf 'INF ---- Service in namespace test ----\n' >&2; exit 0 ;;
  *" connect "*)
    attempts=0
    if [ -f "$CONVEN_TEST_ATTEMPTS" ]; then attempts=$(cat "$CONVEN_TEST_ATTEMPTS"); fi
    attempts=$((attempts + 1))
    printf '%s\n' "$attempts" > "$CONVEN_TEST_ATTEMPTS"
    printf 'ERR Exit: Post "https://cluster/api/v1/namespaces/test/pods": EOF\n'
    exit 7 ;;
esac
exit 1
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONVEN_TEST_ATTEMPTS", attemptsPath)
	endpoint := closedConnectionEndpoint(t)
	config := ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Kubeconfig: kubeconfig,
		Context:    "test-context",
		Namespace:  "test",
		Timeout:    4 * time.Second,
		Readiness:  []ConnectionEndpoint{endpoint},
	}
	process, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "connection.log"), "no-kubectl-workspace", io.Discard)
	if process != nil {
		t.Fatalf("failed recovery returned residual process: %#v", process)
	}
	if err == nil || !strings.Contains(err.Error(), "Kubernetes Pod CREATE EOF") || !strings.Contains(err.Error(), "kubectl is required") {
		t.Fatalf("missing kubectl recovery error = %v", err)
	}
	data, readErr := os.ReadFile(attemptsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("connection attempts = %q, want one", data)
	}
}

func TestEnsureConnectionBoundsRepeatedPodCreateEOFToTwoLaunches(t *testing.T) {
	directory := t.TempDir()
	writeStableKubectl(t, directory)
	_, attempts, err := runFailingKtctl(t, directory, nil)
	if err == nil || !strings.Contains(err.Error(), "automatic retry failed") {
		t.Fatalf("repeated EOF error = %v", err)
	}
	if strings.TrimSpace(string(attempts)) != "2" {
		t.Fatalf("connection attempts = %q, want two", attempts)
	}
}

func TestEnsureConnectionDoesNotRetryTimeoutContainingPodCreateEOF(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	writeStableKubectl(t, directory)
	attemptsPath := filepath.Join(directory, "attempts")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte(`#!/bin/sh
attempts=0
if [ -f "$CONVEN_TEST_ATTEMPTS" ]; then attempts=$(cat "$CONVEN_TEST_ATTEMPTS"); fi
attempts=$((attempts + 1))
printf '%s\n' "$attempts" > "$CONVEN_TEST_ATTEMPTS"
printf 'ERR Exit: Post "https://cluster/api/v1/namespaces/test/pods": EOF\n'
sleep 20
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONVEN_TEST_ATTEMPTS", attemptsPath)
	endpoint := closedConnectionEndpoint(t)
	config := ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Kubeconfig: kubeconfig,
		Context:    "test-context",
		Namespace:  "test",
		Timeout:    time.Second,
		Readiness:  []ConnectionEndpoint{endpoint},
	}
	process, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "connection.log"), "timeout-workspace", io.Discard)
	if process != nil {
		t.Fatalf("timed-out connection returned residual process: %#v", process)
	}
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "automatic retry") {
		t.Fatalf("timed-out connection error = %v", err)
	}
	data, readErr := os.ReadFile(attemptsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("connection attempts = %q, want one", data)
	}
}

func TestEnsureConnectionRetainsOriginalEOFWhenSecondLaunchFails(t *testing.T) {
	directory := t.TempDir()
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	writeStableKubectl(t, directory)
	resourceState := filepath.Join(directory, "owned-resource")
	if err := os.WriteFile(resourceState, []byte("exists\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_RESOURCE_STATE", resourceState)
	attemptsPath := filepath.Join(directory, "attempts")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte(`#!/bin/sh
printf '1\n' > "$CONVEN_TEST_ATTEMPTS"
rm "$0"
printf 'ERR Exit: Post "https://cluster/api/v1/namespaces/test/pods": EOF\n'
exit 7
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONVEN_TEST_ATTEMPTS", attemptsPath)
	endpoint := closedConnectionEndpoint(t)
	config := ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Kubeconfig: kubeconfig,
		Context:    "test-context",
		Namespace:  "test",
		Timeout:    4 * time.Second,
		Readiness:  []ConnectionEndpoint{endpoint},
	}
	process, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "connection.log"), "launch-failure-workspace", io.Discard)
	if process != nil {
		t.Fatalf("failed retry returned residual process: %#v", process)
	}
	if err == nil || !strings.Contains(err.Error(), "Kubernetes Pod CREATE EOF") || !strings.Contains(err.Error(), "automatic retry failed") || !strings.Contains(err.Error(), "connection command") {
		t.Fatalf("second-launch error = %v", err)
	}
	data, readErr := os.ReadFile(attemptsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("connection launches = %q, want only the first launch to start", data)
	}
}

func TestValidateKtctlAttemptResourcesRequiresExactOwnershipAndUID(t *testing.T) {
	validPod := kubernetesResource{
		Kind: "Pod",
		Metadata: kubernetesResourceMetadata{
			Name:        "kt-connect-shadow",
			Namespace:   "test",
			UID:         "pod-uid",
			Labels:      map[string]string{ktctlAttemptMetadataKey: "attempt"},
			Annotations: map[string]string{ktctlAttemptMetadataKey: "attempt"},
		},
	}
	validConfigMap := kubernetesResource{
		Kind: "ConfigMap",
		Metadata: kubernetesResourceMetadata{
			Name:        "kt-connect-shadow",
			Namespace:   "test",
			UID:         "configmap-uid",
			Labels:      map[string]string{ktctlAttemptMetadataKey: "attempt"},
			Annotations: map[string]string{"kt-connect.io/heartbeat": "1"},
		},
	}
	if err := validateKtctlAttemptResources([]kubernetesResource{validPod, validConfigMap}, "test", "attempt"); err != nil {
		t.Fatal(err)
	}
	missingUID := validPod
	missingUID.Metadata.UID = ""
	if err := validateKtctlAttemptResources([]kubernetesResource{missingUID}, "test", "attempt"); err == nil || !strings.Contains(err.Error(), "UID") {
		t.Fatalf("missing UID error = %v", err)
	}
	mismatchedAnnotation := validPod
	mismatchedAnnotation.Metadata.Annotations = map[string]string{ktctlAttemptMetadataKey: "other"}
	if err := validateKtctlAttemptResources([]kubernetesResource{mismatchedAnnotation}, "test", "attempt"); err == nil || !strings.Contains(err.Error(), "ownership annotation") {
		t.Fatalf("mismatched Pod annotation error = %v", err)
	}
	terminating := validPod
	terminating.Metadata.DeletionTimestamp = json.RawMessage(`"2026-09-07T10:00:00Z"`)
	if err := validateKtctlAttemptResources([]kubernetesResource{terminating}, "test", "attempt"); err == nil || !strings.Contains(err.Error(), "terminating") {
		t.Fatalf("terminating resource error = %v", err)
	}
}

func ensureRetryingKtctl(t *testing.T, directory string, args []string) (*ConnectionProcess, []byte) {
	t.Helper()
	process, attempts, err := runKtctlScenario(t, directory, args, true, true)
	if err != nil {
		t.Fatal(err)
	}
	return process, attempts
}

func runFailingKtctl(t *testing.T, directory string, args []string) (*ConnectionProcess, []byte, error) {
	t.Helper()
	return runKtctlScenario(t, directory, args, false, true)
}

func runKtctlScenario(t *testing.T, directory string, args []string, succeedSecond bool, withResidual bool) (*ConnectionProcess, []byte, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	kubeconfig := writeKubeconfig(t, directory, "test-context")
	attemptsPath := filepath.Join(directory, "attempts")
	argsPath := filepath.Join(directory, "ktctl-args")
	if withResidual && os.Getenv("CONVEN_TEST_RESOURCE_STATE") == "" {
		resourceState := filepath.Join(directory, "owned-resource")
		if err := os.WriteFile(resourceState, []byte("exists\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONVEN_TEST_RESOURCE_STATE", resourceState)
	}
	ktctl := filepath.Join(directory, "ktctl")
	script := `#!/bin/sh
attempts=0
if [ -f "$CONVEN_TEST_ATTEMPTS" ]; then attempts=$(cat "$CONVEN_TEST_ATTEMPTS"); fi
attempts=$((attempts + 1))
printf '%s\n' "$attempts" > "$CONVEN_TEST_ATTEMPTS"
printf '%s\n' "$*" >> "$CONVEN_TEST_KTCTL_ARGS"
if [ -n "$CONVEN_TEST_EVENTS" ]; then printf 'connect-%s\n' "$attempts" >> "$CONVEN_TEST_EVENTS"; fi
if [ "$attempts" -eq 1 ] || [ "$CONVEN_TEST_SUCCEED_SECOND" != "1" ]; then
  printf 'ERR Exit: Post "https://cluster/api/v1/namespaces/test/pods": EOF\n'
  exit 7
fi
printf 'INF All looks good, now you can access to resources in the kubernetes cluster\n'
exec "$CONVEN_TEST_BINARY" -test.run=^TestConnectionHelperProcess$
`
	if err := os.WriteFile(ktctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	endpoint := closedConnectionEndpoint(t)
	t.Setenv("CONVEN_TEST_ATTEMPTS", attemptsPath)
	t.Setenv("CONVEN_TEST_KTCTL_ARGS", argsPath)
	t.Setenv("CONVEN_TEST_BINARY", os.Args[0])
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", endpoint.Address)
	if succeedSecond {
		t.Setenv("CONVEN_TEST_SUCCEED_SECOND", "1")
	} else {
		t.Setenv("CONVEN_TEST_SUCCEED_SECOND", "0")
	}
	config := ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Args:       args,
		Kubeconfig: kubeconfig,
		Context:    "test-context",
		Namespace:  "test",
		Timeout:    4 * time.Second,
		Readiness:  []ConnectionEndpoint{endpoint},
	}
	process, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "connection.log"), "retry-workspace", io.Discard)
	attempts, readErr := os.ReadFile(attemptsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pinnedConfig, pinErr := pinKtctlKubernetesTarget(config)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if process != nil && process.Fingerprint != connectionFingerprint(pinnedConfig) {
		t.Fatalf("attempt identity changed fingerprint: %q", process.Fingerprint)
	}
	return process, attempts, err
}
