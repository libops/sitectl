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

func TestSplitLeadingComposePreparationCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		commands        []string
		wantPreparation []string
		wantRemaining   []string
	}{
		{
			name: "pull and build prefix",
			commands: []string{
				"",
				"docker compose pull --ignore-buildable",
				"docker compose -f compose.yml build --pull app",
				"docker compose up -d",
			},
			wantPreparation: []string{
				"docker compose pull --ignore-buildable",
				"docker compose -f compose.yml build --pull app",
			},
			wantRemaining: []string{"docker compose up -d"},
		},
		{
			name:            "build only",
			commands:        []string{"docker compose build --pull", "docker compose up -d"},
			wantPreparation: []string{"docker compose build --pull"},
			wantRemaining:   []string{"docker compose up -d"},
		},
		{
			name:            "pull only",
			commands:        []string{"docker compose pull --ignore-buildable"},
			wantPreparation: []string{"docker compose pull --ignore-buildable"},
			wantRemaining:   []string{},
		},
		{
			name:            "pull fallback",
			commands:        []string{"docker compose pull --quiet || docker compose pull", "docker compose up -d"},
			wantPreparation: []string{"docker compose pull --quiet || docker compose pull"},
			wantRemaining:   []string{"docker compose up -d"},
		},
		{
			name:            "non-prefix build remains",
			commands:        []string{"docker compose pull", "docker compose up -d db", "docker compose build app"},
			wantPreparation: []string{"docker compose pull"},
			wantRemaining:   []string{"docker compose up -d db", "docker compose build app"},
		},
		{
			name:            "compound build and up remains",
			commands:        []string{"docker compose build app && docker compose up -d"},
			wantPreparation: nil,
			wantRemaining:   []string{"docker compose build app && docker compose up -d"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			preparation, remaining := splitLeadingComposePreparationCommands(test.commands)
			if !reflect.DeepEqual(preparation, test.wantPreparation) {
				t.Fatalf("preparation commands = %#v, want %#v", preparation, test.wantPreparation)
			}
			if !reflect.DeepEqual(remaining, test.wantRemaining) {
				t.Fatalf("remaining commands = %#v, want %#v", remaining, test.wantRemaining)
			}
		})
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
