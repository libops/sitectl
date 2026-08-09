package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestRunComposeProjectCommandContextHonorsRemoteComposeFiles(t *testing.T) {
	original := runComposeProjectRemoteShellCommandContext
	t.Cleanup(func() { runComposeProjectRemoteShellCommandContext = original })

	var gotCommand string
	runComposeProjectRemoteShellCommandContext = func(_ context.Context, _ *config.Context, _, _ io.Writer, command string) (string, error) {
		gotCommand = command
		return "", nil
	}
	ctx := &config.Context{
		DockerHostType: config.ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml", "compose production.yaml"},
		EnvFile:        []string{".env"},
	}
	sdk := &SDK{}
	if err := sdk.RunComposeProjectCommandContext(context.Background(), ctx, ctx.ProjectDir, io.Discard, io.Discard, "docker compose ps"); err != nil {
		t.Fatalf("RunComposeProjectCommandContext() error = %v", err)
	}
	for _, expected := range []string{
		"cd '/srv/app' && docker compose",
		"-f /srv/app/compose.yaml",
		"-f '/srv/app/compose production.yaml'",
		"--env-file /srv/app/.env",
		"ps",
	} {
		if !strings.Contains(gotCommand, expected) {
			t.Fatalf("remote command = %q, want %q", gotCommand, expected)
		}
	}
	if strings.Contains(gotCommand, "bash -lc") || strings.Contains(gotCommand, "sh -c") {
		t.Fatalf("remote lifecycle command unexpectedly contains an inline shell program: %q", gotCommand)
	}
}

func TestRunComposeProjectArgvContextDoesNotExposeDynamicArgs(t *testing.T) {
	original := runComposeProjectLocalArgvContext
	t.Cleanup(func() { runComposeProjectLocalArgvContext = original })
	runErr := errors.New("runner failed")
	runComposeProjectLocalArgvContext = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, _ []string, _ []string) error {
		return runErr
	}

	secret := "API_TOKEN=customer-secret-$PWD\nprivate-body"
	argv := DockerComposeExecArgv("app", "tool", "--data", secret)
	operation := dynamicArgvOperation(argv)
	if strings.Contains(operation, "customer-secret") || operation != "docker compose exec" {
		t.Fatalf("dynamic operation log label = %q, want safe operation only", operation)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	err := (&SDK{}).RunComposeProjectArgvContext(context.Background(), ctx, ctx.ProjectDir, nil, io.Discard, io.Discard, argv)
	if !errors.Is(err, runErr) {
		t.Fatalf("RunComposeProjectArgvContext() error = %v, want runner failure", err)
	}
	if strings.Contains(err.Error(), "customer-secret") || strings.Contains(err.Error(), "private-body") {
		t.Fatalf("dynamic argv leaked into error: %v", err)
	}
}

func TestRunComposeProjectArgvContextPreservesDynamicArgsLocallyAndRemotely(t *testing.T) {
	originalLocal := runComposeProjectLocalArgvContext
	originalRemote := runComposeProjectRemoteArgvContext
	t.Cleanup(func() {
		runComposeProjectLocalArgvContext = originalLocal
		runComposeProjectRemoteArgvContext = originalRemote
	})

	want := []string{"curl", "--data", "$5\nsecond", "$PWD", "space here", `quote'\";$(not-code)`}
	for _, hostType := range []config.ContextType{config.ContextLocal, config.ContextRemote} {
		t.Run(string(hostType), func(t *testing.T) {
			var got []string
			runComposeProjectLocalArgvContext = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, _ []string, argv []string) error {
				got = append([]string{}, argv...)
				return nil
			}
			runComposeProjectRemoteArgvContext = func(_ context.Context, _ *config.Context, _ string, _ io.Reader, _, _ io.Writer, argv []string) error {
				got = append([]string{}, argv...)
				return nil
			}
			ctx := &config.Context{DockerHostType: hostType, ProjectDir: "/srv/site with spaces"}
			if err := (&SDK{}).RunComposeProjectArgvContext(context.Background(), ctx, ctx.ProjectDir, strings.NewReader("input"), io.Discard, io.Discard, want); err != nil {
				t.Fatalf("RunComposeProjectArgvContext() error = %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("dynamic argv = %#v, want exact %#v", got, want)
			}
		})
	}
}

func TestRemoteDynamicArgvSerializationRoundTripsLiteralContent(t *testing.T) {
	t.Parallel()
	want := []string{"curl", "--data", "$5\nsecond", "$PWD", "space here", `quote'\";$(not-code)`}
	got, err := shellquote.Split(shellJoin(want))
	if err != nil {
		t.Fatalf("split serialized remote argv: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remote argv round trip = %#v, want %#v", got, want)
	}
}

func TestRunComposeProjectCommandContextWritesHostUIDArtifactWithoutRedirection(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	sdk := &SDK{}
	if err := sdk.RunComposeProjectCommandContext(context.Background(), ctx, projectDir, io.Discard, io.Discard, "id -u > ./certs/UID"); err != nil {
		t.Fatalf("RunComposeProjectCommandContext() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "certs", "UID"))
	if err != nil {
		t.Fatalf("ReadFile(certs/UID) error = %v", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		t.Fatal("host UID artifact is empty")
	}
}

func TestComposeProjectLifecycleUsesDockerCompatibleIdentityWhenNumericIdentityUnavailable(t *testing.T) {
	originalLocalIdentity := resolveLocalComposeProjectHostNumericIdentity
	t.Cleanup(func() { resolveLocalComposeProjectHostNumericIdentity = originalLocalIdentity })
	resolveLocalComposeProjectHostNumericIdentity = func() (string, string, bool, error) {
		return "", "", false, nil
	}

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	expanded, err := expandComposeProjectHostIdentity(context.Background(), ctx, "tool --user $(id -u):$(id -g)")
	if err != nil {
		t.Fatalf("expandComposeProjectHostIdentity() error = %v", err)
	}
	if expanded != "tool --user 0:0" {
		t.Fatalf("expanded lifecycle identity = %q, want Docker-compatible 0:0", expanded)
	}
	if err := (&SDK{}).RunComposeProjectCommandContext(context.Background(), ctx, projectDir, io.Discard, io.Discard, "id -u > ./certs/UID"); err != nil {
		t.Fatalf("RunComposeProjectCommandContext() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "certs", "UID"))
	if err != nil {
		t.Fatalf("ReadFile(certs/UID) error = %v", err)
	}
	if strings.TrimSpace(string(data)) != "0" {
		t.Fatalf("native identity fallback artifact = %q, want 0", strings.TrimSpace(string(data)))
	}
}

func TestRunComposeProjectCommandContextRejectsInlineInterpreterProgram(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	sdk := &SDK{}
	err := sdk.RunComposeProjectCommandContext(
		context.Background(),
		ctx,
		projectDir,
		io.Discard,
		io.Discard,
		`docker compose exec -T drupal /var/www/drupal/vendor/bin/drush php:eval '$scheme = compute();'`,
	)
	if err == nil || !strings.Contains(err.Error(), "checked-in script") {
		t.Fatalf("RunComposeProjectCommandContext() error = %v, want checked-in script guidance", err)
	}
}

func TestRunComposeProjectCommandContextRejectsSymlinkedCheckedScriptBeforeExecution(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	scriptsDir := filepath.Join(projectDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	externalDir := t.TempDir()
	externalScript := filepath.Join(externalDir, "run.sh")
	if err := os.WriteFile(externalScript, []byte("#!/bin/sh\n: > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalScript, filepath.Join(scriptsDir, "run.sh")); err != nil {
		t.Skipf("create script symlink: %v", err)
	}
	marker := filepath.Join(externalDir, "executed")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	err := (&SDK{}).RunComposeProjectCommandContext(
		context.Background(),
		ctx,
		projectDir,
		io.Discard,
		io.Discard,
		shellJoin([]string{"sh", "scripts/run.sh", marker}),
	)
	if err == nil || !strings.Contains(err.Error(), "checked-in lifecycle script") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RunComposeProjectCommandContext() error = %v, want checked-script symlink rejection", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked lifecycle script executed before validation, stat error = %v", statErr)
	}
}

func TestRunComposeProjectCommandListValidatesEveryEntryBeforeExecution(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := (&SDK{}).RunComposeProjectCommandList(cmd, ctx, []string{
		"mkdir side-effect",
		"bash scripts/missing-preflight.sh",
	})
	if err == nil || !strings.Contains(err.Error(), "checked-in lifecycle script") {
		t.Fatalf("RunComposeProjectCommandList() error = %v, want missing-script validation error", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "side-effect")); !os.IsNotExist(err) {
		t.Fatalf("earlier lifecycle entry ran before full-list validation, err=%v", err)
	}
}

func TestRunActiveComposeProjectRolloutValidatesBeforeGitMutation(t *testing.T) {
	originalAcquire := acquireComposeProjectMutationLock
	originalSync := syncComposeProjectCheckout
	t.Cleanup(func() {
		acquireComposeProjectMutationLock = originalAcquire
		syncComposeProjectCheckout = originalSync
	})
	acquireComposeProjectMutationLock = func(context.Context, *config.Context) (*config.ProjectMutationLock, error) {
		return &config.ProjectMutationLock{}, nil
	}
	gitMutated := false
	syncComposeProjectCheckout = func(context.Context, *config.Context, io.Writer) error {
		gitMutated = true
		return nil
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	sdk := &SDK{contextCache: ctx}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := sdk.RunActiveComposeProjectRollout(cmd, []string{
		"docker compose pull",
		"bash scripts/missing-after-pull.sh",
	})
	if err == nil || !strings.Contains(err.Error(), "checked-in lifecycle script") {
		t.Fatalf("RunActiveComposeProjectRollout() error = %v, want checked-program validation", err)
	}
	if gitMutated {
		t.Fatal("Git checkout mutated before the complete rollout list was validated")
	}
}

func TestRunActiveComposeProjectArgvListValidatesBeforeLockOrExecution(t *testing.T) {
	originalAcquire := acquireComposeProjectMutationLock
	originalLocal := runComposeProjectLocalArgvContext
	t.Cleanup(func() {
		acquireComposeProjectMutationLock = originalAcquire
		runComposeProjectLocalArgvContext = originalLocal
	})

	acquired := false
	executed := false
	acquireComposeProjectMutationLock = func(context.Context, *config.Context) (*config.ProjectMutationLock, error) {
		acquired = true
		return &config.ProjectMutationLock{}, nil
	}
	runComposeProjectLocalArgvContext = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string, []string) error {
		executed = true
		return nil
	}

	sdk := &SDK{contextCache: &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := sdk.RunActiveComposeProjectArgvList(cmd, [][]string{{"mkdir", "backup"}, {}})
	if err == nil || !strings.Contains(err.Error(), "argv 2") {
		t.Fatalf("RunActiveComposeProjectArgvList() error = %v, want second-argv validation failure", err)
	}
	if acquired || executed {
		t.Fatalf("invalid argv list acquired=%t executed=%t; want no mutation", acquired, executed)
	}
}

func TestRunActiveComposeProjectArgvListUsesOneLockAndPreservesOrder(t *testing.T) {
	originalAcquire := acquireComposeProjectMutationLock
	originalLocal := runComposeProjectLocalArgvContext
	t.Cleanup(func() {
		acquireComposeProjectMutationLock = originalAcquire
		runComposeProjectLocalArgvContext = originalLocal
	})

	acquisitions := 0
	acquireComposeProjectMutationLock = func(context.Context, *config.Context) (*config.ProjectMutationLock, error) {
		acquisitions++
		return &config.ProjectMutationLock{}, nil
	}
	var got [][]string
	runComposeProjectLocalArgvContext = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, _ []string, argv []string) error {
		got = append(got, append([]string{}, argv...))
		return nil
	}

	want := [][]string{
		{"mkdir", "-p", "backup path"},
		DockerComposeExecArgv("wp", "wp", "db", "export", "backup path/site.sql"),
		{"docker", "compose", "cp", "wp:/tmp/site.sql", "backup path/site.sql"},
	}
	sdk := &SDK{contextCache: &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := sdk.RunActiveComposeProjectArgvList(cmd, want); err != nil {
		t.Fatalf("RunActiveComposeProjectArgvList() error = %v", err)
	}
	if acquisitions != 1 {
		t.Fatalf("project lock acquisitions = %d, want 1", acquisitions)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed argv = %#v, want ordered %#v", got, want)
	}
}

func TestRunActiveComposeProjectHostArgvUsesDockerVisibleProjectDirectory(t *testing.T) {
	originalAcquire := acquireComposeProjectMutationLock
	originalLocal := runComposeProjectLocalArgvContext
	originalVisiblePath := composeProjectDockerVisibleLocalPath
	originalLocalIdentity := resolveLocalComposeProjectHostNumericIdentity
	t.Cleanup(func() {
		acquireComposeProjectMutationLock = originalAcquire
		runComposeProjectLocalArgvContext = originalLocal
		composeProjectDockerVisibleLocalPath = originalVisiblePath
		resolveLocalComposeProjectHostNumericIdentity = originalLocalIdentity
	})

	clientProjectDir := t.TempDir()
	dockerProjectDir := "/host/customer/site"
	visiblePathCalls := 0
	composeProjectDockerVisibleLocalPath = func(projectDir string) string {
		visiblePathCalls++
		if projectDir != clientProjectDir {
			t.Fatalf("DockerVisibleLocalPath input = %q, want %q", projectDir, clientProjectDir)
		}
		return dockerProjectDir
	}
	resolveLocalComposeProjectHostNumericIdentity = func() (string, string, bool, error) {
		return "1000", "1001", true, nil
	}
	acquireComposeProjectMutationLock = func(context.Context, *config.Context) (*config.ProjectMutationLock, error) {
		return &config.ProjectMutationLock{}, nil
	}
	var executionDir string
	var executedArgv []string
	runComposeProjectLocalArgvContext = func(_ context.Context, projectDir string, _ io.Reader, _, _ io.Writer, _ []string, argv []string) error {
		executionDir = projectDir
		executedArgv = append([]string{}, argv...)
		return nil
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: clientProjectDir}
	sdk := &SDK{contextCache: ctx}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var gotHost ComposeProjectHost
	err := sdk.RunActiveComposeProjectHostArgv(cmd, func(host ComposeProjectHost) []string {
		gotHost = host
		return []string{"task-agent", "--bind", host.ProjectDir}
	})
	if err != nil {
		t.Fatalf("RunActiveComposeProjectHostArgv() error = %v", err)
	}
	if gotHost.ProjectDir != dockerProjectDir || gotHost.UID != "1000" || gotHost.GID != "1001" || !gotHost.HasNumericIdentity {
		t.Fatalf("ComposeProjectHost = %#v, want Docker-visible path and numeric identity", gotHost)
	}
	if executionDir != clientProjectDir {
		t.Fatalf("local execution directory = %q, want client checkout %q", executionDir, clientProjectDir)
	}
	if want := []string{"task-agent", "--bind", dockerProjectDir}; !reflect.DeepEqual(executedArgv, want) {
		t.Fatalf("executed argv = %#v, want %#v", executedArgv, want)
	}
	remoteProjectDir := "/srv/customer/site"
	if got := composeProjectHostDirectory(&config.Context{DockerHostType: config.ContextRemote, ProjectDir: remoteProjectDir}); got != remoteProjectDir {
		t.Fatalf("remote Docker-visible project directory = %q, want %q", got, remoteProjectDir)
	}
	if visiblePathCalls != 1 {
		t.Fatalf("DockerVisibleLocalPath calls = %d, want local context only", visiblePathCalls)
	}
}

func TestResolveComposeProjectHostRepresentsWindowsNumericIdentityAbsence(t *testing.T) {
	originalIdentity := resolveComposeProjectHostIdentity
	originalLocalIdentity := resolveLocalComposeProjectHostNumericIdentity
	originalVisiblePath := composeProjectDockerVisibleLocalPath
	t.Cleanup(func() {
		resolveComposeProjectHostIdentity = originalIdentity
		resolveLocalComposeProjectHostNumericIdentity = originalLocalIdentity
		composeProjectDockerVisibleLocalPath = originalVisiblePath
	})
	composeProjectDockerVisibleLocalPath = func(projectDir string) string { return projectDir }
	localIdentityCalls := 0
	resolveLocalComposeProjectHostNumericIdentity = func() (string, string, bool, error) {
		localIdentityCalls++
		return "", "", false, nil
	}
	remoteIdentityCalls := 0
	resolveComposeProjectHostIdentity = func(context.Context, *config.Context) (string, string, error) {
		remoteIdentityCalls++
		return "2000", "2001", nil
	}

	local, err := resolveComposeProjectHost(context.Background(), &config.Context{DockerHostType: config.ContextLocal, ProjectDir: `C:\sites\museum`})
	if err != nil {
		t.Fatalf("resolveComposeProjectHost(local Windows) error = %v", err)
	}
	if local.HasNumericIdentity || local.UID != "" || local.GID != "" {
		t.Fatalf("local Windows ComposeProjectHost = %#v, want absent numeric identity", local)
	}
	remote, err := resolveComposeProjectHost(context.Background(), &config.Context{DockerHostType: config.ContextRemote, ProjectDir: "/srv/museum"})
	if err != nil {
		t.Fatalf("resolveComposeProjectHost(remote from Windows) error = %v", err)
	}
	if !remote.HasNumericIdentity || remote.UID != "2000" || remote.GID != "2001" {
		t.Fatalf("remote ComposeProjectHost = %#v, want remote numeric identity", remote)
	}
	if localIdentityCalls != 1 || remoteIdentityCalls != 1 {
		t.Fatalf("numeric identity resolver calls = local %d remote %d, want one each", localIdentityCalls, remoteIdentityCalls)
	}
}
