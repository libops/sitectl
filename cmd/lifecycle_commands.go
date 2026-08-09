package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/kballard/go-shellquote"
	corelifecycle "github.com/libops/sitectl/internal/lifecycle"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

type lifecycleCommandList struct {
	commands  []string
	operators []string
}

type lifecycleCommandPlan struct {
	argv      []string
	composeUp bool
}

// runLifecycleCommandList executes the deliberately small lifecycle metadata
// grammar without passing a program string to a shell. A metadata entry may be
// one argv command or an &&/|| list of argv commands. Substantive programs must
// live in a checked-in script and be named as a normal command argument.
func runLifecycleCommandList(cmd *cobra.Command, ctx *config.Context, commandText string, baseEnv []string, prepareComposeUp bool) error {
	list, plans, err := planLifecycleCommandList(ctx, commandText)
	if err != nil {
		return err
	}

	lastSucceeded := true
	var lastErr error
	for index := range list.commands {
		if index > 0 {
			switch list.operators[index-1] {
			case "&&":
				if !lastSucceeded {
					continue
				}
			case "||":
				if lastSucceeded {
					continue
				}
			}
		}

		argv := plans[index].argv
		composeUp := plans[index].composeUp
		env := baseEnv
		if len(env) == 0 {
			env = os.Environ()
		}
		if composeUp && prepareComposeUp {
			envValues, messages, err := ctx.PrepareComposeUpPortOverride()
			if err != nil {
				return err
			}
			for _, message := range messages {
				fmt.Fprintln(cmd.ErrOrStderr(), message)
			}
			env = config.AppendEnvOverrides(env, envValues)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", shellquote.Join(argv...))
		command := exec.CommandContext(cmd.Context(), argv[0], argv[1:]...) // #nosec G204 -- lifecycle metadata is parsed into a constrained argv grammar and never evaluated by a shell.
		command.Dir = ctx.ProjectDir
		command.Env = env
		command.Stdin = cmd.InOrStdin()
		command.Stdout = cmd.OutOrStdout()
		command.Stderr = cmd.ErrOrStderr()
		config.LogDockerComposeCommand(ctx, command.String())

		if ctx.DockerHostType == config.ContextLocal {
			lastErr = command.Run()
		} else {
			_, lastErr = ctx.RunCommandContext(cmd.Context(), command)
		}
		lastSucceeded = lastErr == nil
	}
	if !lastSucceeded {
		return fmt.Errorf("run lifecycle command %q: %w", commandText, lastErr)
	}
	return nil
}

func planLifecycleCommandList(ctx *config.Context, commandText string) (lifecycleCommandList, []lifecycleCommandPlan, error) {
	list, err := parseLifecycleCommandList(commandText)
	if err != nil {
		return lifecycleCommandList{}, nil, err
	}
	plans := make([]lifecycleCommandPlan, len(list.commands))
	for index, segment := range list.commands {
		argv, composeUp, err := lifecycleCommandArgv(ctx, segment)
		if err != nil {
			return lifecycleCommandList{}, nil, fmt.Errorf("parse lifecycle command %q: %w", segment, err)
		}
		plans[index] = lifecycleCommandPlan{argv: argv, composeUp: composeUp}
	}
	return list, plans, nil
}

func parseLifecycleCommandList(commandText string) (lifecycleCommandList, error) {
	parsed, err := corelifecycle.Parse(commandText)
	if err != nil {
		return lifecycleCommandList{}, err
	}
	return lifecycleCommandList{commands: parsed.Commands, operators: parsed.Operators}, nil
}

func lifecycleCommandArgv(ctx *config.Context, commandText string) ([]string, bool, error) {
	return corelifecycle.Argv(ctx, commandText)
}

func validateLifecycleProjectScripts(ctx *config.Context, commandText string, plans []lifecycleCommandPlan) error {
	for _, plan := range plans {
		script, resolved, err := corelifecycle.ProjectScriptPath(ctx.ProjectDir, plan.argv)
		if err != nil {
			return fmt.Errorf("validate lifecycle command %q: %w", commandText, err)
		}
		if script == "" {
			continue
		}
		if err := ctx.ValidateProjectRegularFile(ctx.ProjectDir, resolved); err != nil {
			return fmt.Errorf("checked-in lifecycle script %q is invalid for command %q: %w", script, commandText, err)
		}
	}
	return nil
}
