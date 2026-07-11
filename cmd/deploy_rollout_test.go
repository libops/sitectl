package cmd

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestIsDockerComposeSubcommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		command    string
		subcommand string
		want       bool
	}{
		{name: "pull", command: "docker compose pull --ignore-buildable", subcommand: "pull", want: true},
		{name: "pull fallback", command: "docker compose pull --quiet || docker compose pull", subcommand: "pull", want: true},
		{name: "global options", command: "docker compose -f compose.yml --ansi never pull", subcommand: "pull", want: true},
		{name: "build pull option", command: "docker compose build --pull", subcommand: "pull", want: false},
		{name: "up pull option", command: "docker compose up --pull missing -d", subcommand: "pull", want: false},
		{name: "unrelated shell", command: "echo docker compose pull", subcommand: "pull", want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isDockerComposeSubcommand(test.command, test.subcommand); got != test.want {
				t.Fatalf("isDockerComposeSubcommand(%q, %q) = %v, want %v", test.command, test.subcommand, got, test.want)
			}
		})
	}
}

func TestRunDeployComposeRolloutNoPullSkipsOnlyComposePull(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	commands := []string{
		"docker compose pull --ignore-buildable --quiet || docker compose pull --ignore-buildable",
		"true",
	}
	if err := runDeployComposeRollout(cmd, ctx, commands, true); err != nil {
		t.Fatalf("runDeployComposeRollout() error = %v", err)
	}
}

func TestSplitLeadingComposePullCommands(t *testing.T) {
	t.Parallel()
	commands := []string{
		"",
		"docker compose pull --ignore-buildable",
		"docker compose -f compose.yml pull db",
		"docker compose build --pull",
		"docker compose pull app",
	}
	pulls, remaining := splitLeadingComposePullCommands(commands)
	if want := []string{"docker compose pull --ignore-buildable", "docker compose -f compose.yml pull db"}; !reflect.DeepEqual(pulls, want) {
		t.Fatalf("pull commands = %#v, want %#v", pulls, want)
	}
	if want := commands[3:]; !reflect.DeepEqual(remaining, want) {
		t.Fatalf("remaining commands = %#v, want %#v", remaining, want)
	}
}

func TestRunDeployComposeRolloutPropagatesCommandFailure(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: t.TempDir()}
	err := runDeployComposeRollout(cmd, ctx, []string{"exit 23"}, false)
	if err == nil || !strings.Contains(err.Error(), "exit 23") {
		t.Fatalf("runDeployComposeRollout() error = %v, want command failure", err)
	}
}
