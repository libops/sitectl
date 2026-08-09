package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestRunDeployCycleUpdatesGitBeforeStoppingSite(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var events []string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		events = append(events, "git")
		return nil
	}
	deployRunHook = func(_ *cobra.Command, _, _, hook string) error {
		events = append(events, "hook:"+hook)
		return nil
	}
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		events = append(events, "compose:"+strings.Join(args, " "))
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		events = append(events, "resolve-rollout")
		return nil, false, nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if err != nil {
		t.Fatalf("runDeployCycle() error = %v", err)
	}
	want := []string{
		"resolve-rollout",
		"git",
		"compose:pull",
		"hook:pre-down",
		"compose:down --remove-orphans",
		"compose:up -d --remove-orphans",
		"hook:post-up",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

func TestRunDeployCycleUsesExactRefUpdater(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var gotRef string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		t.Fatal("branch updater called for an exact ref")
		return nil
	}
	deployRunGitRefUpdate = func(_ *cobra.Command, _ config.Context, ref string) error {
		gotRef = ref
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) { return nil, false, nil }
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error { return nil }

	if err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "core", false, deployCycleOptions{Ref: "refs/pull/42/head"}); err != nil {
		t.Fatalf("runDeployCycle() error = %v", err)
	}
	if gotRef != "refs/pull/42/head" {
		t.Fatalf("exact ref = %q", gotRef)
	}
}

func TestRunDeployCyclePreparesPluginPullAndBuildBeforeStoppingSite(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var events []string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		events = append(events, "git")
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		events = append(events, "resolve-rollout")
		return []string{"docker compose pull --ignore-buildable", "docker compose build --pull", "docker compose up -d"}, true, nil
	}
	deployRunComposeRollout = func(_ *cobra.Command, _ *config.Context, commands []string, _ bool) error {
		events = append(events, "rollout:"+strings.Join(commands, ","))
		return nil
	}
	deployRunHook = func(_ *cobra.Command, _, _, hook string) error {
		events = append(events, "hook:"+hook)
		return nil
	}
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		events = append(events, "compose:"+strings.Join(args, " "))
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if err != nil {
		t.Fatalf("runDeployCycle() error = %v", err)
	}
	want := []string{
		"resolve-rollout",
		"git",
		"rollout:docker compose pull --ignore-buildable,docker compose build --pull",
		"hook:pre-down",
		"compose:down --remove-orphans",
		"rollout:docker compose up -d",
		"hook:post-up",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

func TestRunDeployCycleLeavesNonPrefixBuildAfterStoppingSite(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var events []string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return nil }
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose up -d db", "docker compose build app", "docker compose up -d"}, true, nil
	}
	deployRunComposeRollout = func(_ *cobra.Command, _ *config.Context, commands []string, _ bool) error {
		events = append(events, "rollout:"+strings.Join(commands, ","))
		return nil
	}
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		events = append(events, "compose:"+strings.Join(args, " "))
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", false, deployCycleOptions{})
	if err != nil {
		t.Fatalf("runDeployCycle() error = %v", err)
	}
	want := []string{
		"compose:down --remove-orphans",
		"rollout:docker compose up -d db,docker compose build app,docker compose up -d",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

func TestRunDeployCyclePreparationFailureLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	wantErr := errors.New("build failed")
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return nil }
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose pull", "docker compose build --pull", "docker compose up -d"}, true, nil
	}
	deployRunComposeRollout = func(_ *cobra.Command, _ *config.Context, commands []string, noPull bool) error {
		if noPull {
			t.Fatal("preparation unexpectedly disabled pulls")
		}
		if want := []string{"docker compose pull", "docker compose build --pull"}; !reflect.DeepEqual(commands, want) {
			t.Fatalf("preparation commands = %#v, want %#v", commands, want)
		}
		return wantErr
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after failed compose preparation")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose stopped after failed compose preparation")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
}

func TestRunDeployCycleNoPullStillPreparesBuildBeforeStoppingSite(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var events []string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return nil }
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose pull", "docker compose build --pull", "docker compose up -d"}, true, nil
	}
	deployRunComposeRollout = func(_ *cobra.Command, _ *config.Context, commands []string, noPull bool) error {
		if noPull {
			events = append(events, "rollout:no-pull:"+strings.Join(commands, ","))
		} else {
			events = append(events, "rollout:"+strings.Join(commands, ","))
		}
		return nil
	}
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		events = append(events, "compose:"+strings.Join(args, " "))
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", false, deployCycleOptions{NoPull: true})
	if err != nil {
		t.Fatalf("runDeployCycle() error = %v", err)
	}
	want := []string{
		"rollout:no-pull:docker compose pull,docker compose build --pull",
		"compose:down --remove-orphans",
		"rollout:no-pull:docker compose up -d",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

func TestRunDeployCyclePullFailureLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	wantErr := errors.New("registry unavailable")
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return nil }
	deployResolveRollout = func(string) ([]string, bool, error) { return nil, false, nil }
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		if reflect.DeepEqual(args, []string{"pull"}) {
			return wantErr
		}
		t.Fatalf("compose ran after failed pull preflight: %v", args)
		return nil
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after failed pull preflight")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
}

func TestRunDeployCycleRolloutResolutionFailureLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	var events []string
	wantErr := errors.New("invalid plugin metadata")
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		events = append(events, "git")
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		events = append(events, "resolve-rollout")
		return nil, false, wantErr
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after failed rollout resolution")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose stopped after failed rollout resolution")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
	if want := []string{"resolve-rollout"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

func TestRunDeployCycleInvalidRolloutLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		t.Fatal("git mutated before invalid rollout validation")
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{`docker compose pull || docker compose exec -T drupal drush php:eval 'print "inline";'`}, true, nil
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after invalid rollout validation")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose stopped after invalid rollout validation")
		return nil
	}
	deployRunComposeRollout = func(*cobra.Command, *config.Context, []string, bool) error {
		t.Fatal("invalid rollout command was executed")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{NoPull: true})
	if err == nil || !strings.Contains(err.Error(), "inline interpreter programs are not supported") {
		t.Fatalf("runDeployCycle() error = %v, want inline-program validation error", err)
	}
}

func TestRunDeployCycleMissingRolloutScriptLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		t.Fatal("git mutated before missing rollout script validation")
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"bash scripts/missing-rollout-preflight.sh"}, true, nil
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after missing rollout script validation")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose stopped after missing rollout script validation")
		return nil
	}
	deployRunComposeRollout = func(*cobra.Command, *config.Context, []string, bool) error {
		t.Fatal("rollout with a missing script was executed")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}, "app", true, deployCycleOptions{})
	if err == nil || !strings.Contains(err.Error(), "checked-in lifecycle script") {
		t.Fatalf("runDeployCycle() error = %v, want missing checked-in script error", err)
	}
}

func TestRunDeployCyclePreparationOnlyRolloutLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose pull", "docker compose build --pull"}, true, nil
	}
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		t.Fatal("git mutated before preparation-only rollout validation")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose ran for preparation-only rollout")
		return nil
	}
	deployRunComposeRollout = func(*cobra.Command, *config.Context, []string, bool) error {
		t.Fatal("preparation-only rollout was executed")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", false, deployCycleOptions{})
	if err == nil || !strings.Contains(err.Error(), "unconditional final start") {
		t.Fatalf("runDeployCycle() error = %v, want final-start validation error", err)
	}
}

func TestRunDeployCycleRevalidatesRolloutScriptAfterGitUpdate(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	projectDir := t.TempDir()
	script := filepath.Join(projectDir, "scripts", "rollout.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"sh scripts/rollout.sh", "docker compose up -d"}, true, nil
	}
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error {
		return os.Remove(script)
	}
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after fetched rollout script failed revalidation")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose ran after fetched rollout script failed revalidation")
		return nil
	}
	deployRunComposeRollout = func(*cobra.Command, *config.Context, []string, bool) error {
		t.Fatal("rollout ran after fetched script failed revalidation")
		return nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}, "app", true, deployCycleOptions{})
	if err == nil || !strings.Contains(err.Error(), "validate fetched compose rollout") || !strings.Contains(err.Error(), "checked-in lifecycle script") {
		t.Fatalf("runDeployCycle() error = %v, want fetched-script revalidation error", err)
	}
}

func TestRunDeployCycleStopsAfterRolloutGateFailure(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	exitErr := &testDeployExitError{status: 130}
	var events []string
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return nil }
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose exec -T app migrate", "docker compose up -d"}, true, nil
	}
	deployRunHook = func(_ *cobra.Command, _, _, hook string) error {
		events = append(events, "hook:"+hook)
		if hook == "post-up" {
			t.Fatal("post-up hook ran after failed rollout gate")
		}
		return nil
	}
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		events = append(events, "compose:"+strings.Join(args, " "))
		return nil
	}
	deployRunRecoveryCompose = func(recoveryCommand *cobra.Command, _ config.Context, args []string) error {
		if _, ok := recoveryCommand.Context().Deadline(); !ok {
			t.Fatal("deploy recovery context does not have a deadline")
		}
		events = append(events, "recovery:"+strings.Join(args, " "))
		return nil
	}
	deployRunComposeRollout = func(_ *cobra.Command, _ *config.Context, commands []string, _ bool) error {
		events = append(events, "rollout:"+strings.Join(commands, ","))
		return exitErr
	}

	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)
	err := runDeployCycle(cmd, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, exitErr) {
		t.Fatalf("runDeployCycle() error = %v, want wrapped exit status 130", err)
	}
	want := []string{
		"hook:pre-down",
		"compose:down --remove-orphans",
		"rollout:docker compose exec -T app migrate,docker compose up -d",
		"recovery:up -d --remove-orphans --wait --wait-timeout 540",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
}

type deployRecoveryContextKey struct{}

func TestRunDeployCycleRecoveryContinuesAfterCancellation(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	ctx := config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	deployAcquireProjectLock = func(runCtx context.Context, ctx *config.Context) (*config.ProjectMutationLock, error) {
		return ctx.AcquireProjectMutationLock(runCtx)
	}
	deployResolveRollout = func(string) ([]string, bool, error) { return nil, false, nil }
	wantErr := errors.New("compose interrupted")
	baseContext := context.WithValue(context.Background(), deployRecoveryContextKey{}, "preserved")
	runContext, cancel := context.WithCancel(baseContext)
	deployRunContextCompose = func(_ *cobra.Command, _ config.Context, args []string) error {
		if !reflect.DeepEqual(args, []string{"down", "--remove-orphans"}) {
			t.Fatalf("unexpected Compose operation before recovery: %v", args)
		}
		cancel()
		return wantErr
	}
	recovered := false
	deployRunRecoveryCompose = func(recoveryCommand *cobra.Command, _ config.Context, args []string) error {
		recovered = true
		if err := recoveryCommand.Context().Err(); err != nil {
			t.Fatalf("recovery inherited deploy cancellation: %v", err)
		}
		if _, ok := recoveryCommand.Context().Deadline(); !ok {
			t.Fatal("recovery context is not bounded by a deadline")
		}
		if got := recoveryCommand.Context().Value(deployRecoveryContextKey{}); got != "preserved" {
			t.Fatalf("recovery lost project-lock context values: got %v", got)
		}
		if !reflect.DeepEqual(args, []string{"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "540"}) {
			t.Fatalf("recovery Compose args = %v", args)
		}
		return nil
	}

	cmd := &cobra.Command{}
	cmd.SetContext(runContext)
	cmd.SetErr(io.Discard)
	err := runDeployCycle(cmd, "prod", ctx, "core", false, deployCycleOptions{SkipGit: true, NoPull: true})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
	if !recovered {
		t.Fatal("deploy cancellation did not trigger recovery")
	}
}

func TestDeployRecoveryWaitTimeoutFitsOuterBudget(t *testing.T) {
	t.Parallel()
	if deployRecoveryWaitTimeout >= deployRecoveryTimeout {
		t.Fatalf("Compose wait timeout %s must be shorter than recovery context timeout %s", deployRecoveryWaitTimeout, deployRecoveryTimeout)
	}
}

func TestRunDeployCycleJoinsRecoveryFailure(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	deployErr := errors.New("rollout failed")
	recoveryErr := errors.New("recovery failed")
	deployResolveRollout = func(string) ([]string, bool, error) {
		return []string{"docker compose up -d"}, true, nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error { return nil }
	deployRunComposeRollout = func(*cobra.Command, *config.Context, []string, bool) error { return deployErr }
	deployRunRecoveryCompose = func(*cobra.Command, config.Context, []string) error { return recoveryErr }

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", false, deployCycleOptions{SkipGit: true})
	if !errors.Is(err, deployErr) || !errors.Is(err, recoveryErr) {
		t.Fatalf("runDeployCycle() error = %v, want joined deploy and recovery failures", err)
	}
	if !strings.Contains(err.Error(), "no automatic Git or application-data rollback was attempted") {
		t.Fatalf("runDeployCycle() error = %v, want explicit no-rollback diagnostic", err)
	}
}

type testDeployExitError struct {
	status int
}

func (e *testDeployExitError) Error() string {
	return fmt.Sprintf("Process exited with status %d", e.status)
}

func (e *testDeployExitError) ExitStatus() int {
	return e.status
}

func TestRunDeployCycleGitFailureLeavesSiteRunning(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()

	wantErr := errors.New("network unavailable")
	deployRunGitUpdate = func(*cobra.Command, config.Context, string) error { return wantErr }
	deployRunHook = func(*cobra.Command, string, string, string) error {
		t.Fatal("hook ran after failed git update")
		return nil
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error {
		t.Fatal("compose stopped after failed git update")
		return nil
	}
	deployResolveRollout = func(string) ([]string, bool, error) { return nil, false, nil }

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
}

func TestRunDeployCycleSerializesConcurrentProjectMutations(t *testing.T) {
	restore := stubDeployCycle(t)
	defer restore()
	deployAcquireProjectLock = func(runCtx context.Context, ctx *config.Context) (*config.ProjectMutationLock, error) {
		return ctx.AcquireProjectMutationLock(runCtx)
	}
	deployRunContextCompose = func(*cobra.Command, config.Context, []string) error { return nil }

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var calls atomic.Int32
	deployResolveRollout = func(string) ([]string, bool, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return nil, false, nil
	}
	ctx := config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	done := make(chan error, 2)
	go func() {
		done <- runDeployCycle(&cobra.Command{}, "prod", ctx, "core", false, deployCycleOptions{SkipGit: true, NoPull: true})
	}()
	<-firstEntered
	go func() {
		done <- runDeployCycle(&cobra.Command{}, "prod", ctx, "core", false, deployCycleOptions{SkipGit: true, NoPull: true})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second deploy entered while the first project mutation lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second deploy did not enter after the first released the project lock")
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("runDeployCycle() error = %v", err)
		}
	}
}

func stubDeployCycle(t *testing.T) func() {
	t.Helper()
	oldGit := deployRunGitUpdate
	oldGitRef := deployRunGitRefUpdate
	oldCompose := deployRunContextCompose
	oldRecoveryCompose := deployRunRecoveryCompose
	oldHook := deployRunHook
	oldResolve := deployResolveRollout
	oldRollout := deployRunComposeRollout
	oldAcquireLock := deployAcquireProjectLock
	oldValidateContext := deployValidateContext
	deployAcquireProjectLock = func(context.Context, *config.Context) (*config.ProjectMutationLock, error) {
		return &config.ProjectMutationLock{}, nil
	}
	deployValidateContext = func(*config.Context) error { return nil }
	deployRunRecoveryCompose = func(*cobra.Command, config.Context, []string) error { return nil }
	return func() {
		deployRunGitUpdate = oldGit
		deployRunGitRefUpdate = oldGitRef
		deployRunContextCompose = oldCompose
		deployRunRecoveryCompose = oldRecoveryCompose
		deployRunHook = oldHook
		deployResolveRollout = oldResolve
		deployRunComposeRollout = oldRollout
		deployAcquireProjectLock = oldAcquireLock
		deployValidateContext = oldValidateContext
	}
}
