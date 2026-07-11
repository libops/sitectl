package cmd

import (
	"errors"
	"reflect"
	"strings"
	"testing"

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
		"git",
		"resolve-rollout",
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
		"git",
		"resolve-rollout",
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
	if want := []string{"git", "resolve-rollout"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("deploy events = %#v, want %#v", events, want)
	}
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
	deployResolveRollout = func(string) ([]string, bool, error) {
		t.Fatal("rollout resolved after failed git update")
		return nil, false, nil
	}

	err := runDeployCycle(&cobra.Command{}, "prod", config.Context{}, "app", true, deployCycleOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDeployCycle() error = %v, want %v", err, wantErr)
	}
}

func stubDeployCycle(t *testing.T) func() {
	t.Helper()
	oldGit := deployRunGitUpdate
	oldCompose := deployRunContextCompose
	oldHook := deployRunHook
	oldResolve := deployResolveRollout
	oldRollout := deployRunComposeRollout
	return func() {
		deployRunGitUpdate = oldGit
		deployRunContextCompose = oldCompose
		deployRunHook = oldHook
		deployResolveRollout = oldResolve
		deployRunComposeRollout = oldRollout
	}
}
