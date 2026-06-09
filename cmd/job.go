package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"github.com/libops/sitectl/pkg/config"
	corejob "github.com/libops/sitectl/pkg/job"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var jobCmd = &cobra.Command{
	Use:   "job",
	Short: "List and run plugin-defined jobs",
	Long: `Jobs are plugin-defined operations that run against a specific context — backups, imports,
and other maintenance tasks. Plugins register jobs when loaded for the active context.

Use list to see what jobs are available, then run to execute one.`,
}

var jobListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available jobs for the active or selected context",
	Long: `List jobs registered by the plugins associated with the active context.

Each job shows its name, owning plugin, and a short description. Use the name with
job run to execute it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := resolveContextName(cmd)
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		jobs, err := availableJobs(ctx)
		if err != nil {
			return err
		}
		if len(jobs) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No jobs available for context %q\n", ctx.Name)
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPLUGIN\tDESCRIPTION")
		for _, spec := range jobs {
			fmt.Fprintf(w, "%s\t%s\t%s\n", spec.Name, spec.Plugin, spec.Description)
		}
		return w.Flush()
	},
}

var jobExecCmd = &cobra.Command{
	Use:                "run JOB [args...]",
	Short:              "Run a plugin-defined job",
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}
		if len(filteredArgs) == 0 {
			return fmt.Errorf("job name is required")
		}
		jobName := filteredArgs[0]
		jobArgs := filteredArgs[1:]

		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		owner, name, err := resolveJobOwner(ctx, jobName)
		if err != nil {
			return err
		}
		if requestsHelp(jobArgs) {
			return renderJobHelp(cmd, ctx, owner, name, jobArgs)
		}
		output, err := runJobCommand(cmd, contextName, owner, name, jobArgs)
		if strings.TrimSpace(output) != "" {
			if _, printErr := fmt.Fprint(cmd.OutOrStdout(), output); printErr != nil {
				return printErr
			}
		}
		if err != nil {
			return cleanPluginCommandError(err)
		}
		return nil
	},
}

func availableJobs(ctx config.Context) ([]corejob.Spec, error) {
	owners, err := pluginsForContext(ctx.Plugin)
	if err != nil {
		return nil, err
	}
	var combined []corejob.Spec
	for _, owner := range owners {
		req := plugin.NewRPCRequest(plugin.MethodJobList)
		req.Context = ctx.Name
		resp, err := pluginSDK.InvokePluginRPC(owner, req, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
		})
		if err != nil {
			if plugin.IsRPCErrorCode(err, plugin.RPCErrorCodeNotRegistered) {
				continue
			}
			return nil, fmt.Errorf("list jobs from plugin %q: %w", owner, err)
		}
		specs, err := plugin.DecodeRPCResult[[]corejob.Spec](resp)
		if err != nil {
			return nil, fmt.Errorf("parse jobs from plugin %q: %w", owner, err)
		}
		for i := range specs {
			if strings.TrimSpace(specs[i].Plugin) == "" {
				specs[i].Plugin = owner
			}
		}
		combined = append(combined, specs...)
	}
	sort.Slice(combined, func(i, j int) bool {
		if combined[i].Plugin != combined[j].Plugin {
			return combined[i].Plugin < combined[j].Plugin
		}
		return combined[i].Name < combined[j].Name
	})
	return combined, nil
}

func pluginsForContext(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var ordered []string
	var walk func(string) error
	walk = func(name string) error {
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		ordered = append(ordered, name)
		installed, err := installedPluginWithMetadata(name)
		if err != nil {
			return err
		}
		for _, include := range installed.Includes {
			if err := walk(include); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return ordered, nil
}

func resolveJobOwner(ctx config.Context, raw string) (string, string, error) {
	name := strings.TrimSpace(raw)
	if pluginName, jobName, ok := splitNamespacedComponent(name); ok {
		return pluginName, jobName, nil
	}
	jobs, err := availableJobs(ctx)
	if err != nil {
		return "", "", err
	}
	var matches []corejob.Spec
	for _, spec := range jobs {
		if strings.EqualFold(spec.Name, name) {
			matches = append(matches, spec)
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("job %q not found for context %q", name, ctx.Name)
	}
	if len(matches) > 1 {
		owners := make([]string, 0, len(matches))
		for _, spec := range matches {
			owners = append(owners, spec.Plugin)
		}
		sort.Strings(owners)
		return "", "", fmt.Errorf("job %q is ambiguous; qualify it as plugin/job (%s)", name, strings.Join(owners, ", "))
	}
	return matches[0].Plugin, matches[0].Name, nil
}

func init() {
	jobCmd.AddCommand(jobListCmd)
	jobCmd.AddCommand(jobExecCmd)
	jobCmd.GroupID = "ops"
	RootCmd.AddCommand(jobCmd)
}

func runJobCommand(cmd *cobra.Command, contextName, owner, name string, jobArgs []string) (string, error) {
	req, err := plugin.NewJobRunRequest(name, jobArgs...)
	if err != nil {
		return "", err
	}
	req.Context = contextName
	if stderrFile, ok := cmd.ErrOrStderr().(*os.File); ok && term.IsTerminal(int(stderrFile.Fd())) {
		return runJobWithProgress(cmd, owner, name, contextName, req)
	}
	resp, err := pluginSDK.InvokePluginRPC(owner, req, plugin.CommandExecOptions{
		Context:    RootCmd.Context(),
		Stderr:     cmd.ErrOrStderr(),
		LiveStderr: true,
	})
	return resp.Output, err
}

func runJobWithProgress(cmd *cobra.Command, owner, name, contextName string, req plugin.RPCRequest) (string, error) {
	m := newDebugSpinnerModel()
	m.title = "Running Job"
	m.detail = fmt.Sprintf("%s on %s", name, contextName)

	p := tea.NewProgram(m, tea.WithOutput(cmd.ErrOrStderr()))

	go func() {
		resp, err := pluginSDK.InvokePluginRPC(owner, req, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
		})
		p.Send(debugProgressDoneMsg{result: resp.Output, err: err})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("running job progress: %w", err)
	}
	done := finalModel.(debugSpinnerModel)
	return done.result, done.err
}

func requestsHelp(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--help", "-h":
			return true
		}
	}
	return false
}

func renderJobHelp(cmd *cobra.Command, ctx config.Context, owner, name string, jobArgs []string) error {
	req, err := plugin.NewJobRunRequest(name, jobArgs...)
	if err != nil {
		return err
	}
	req.Context = ctx.Name
	resp, err := pluginSDK.InvokePluginRPC(owner, req, plugin.CommandExecOptions{
		Context: RootCmd.Context(),
	})
	if err != nil {
		return cleanPluginCommandError(err)
	}

	replacements := []string{
		fmt.Sprintf("sitectl %s __sitectl-rpc", owner), fmt.Sprintf("sitectl job run %s", name),
		"__sitectl-rpc", fmt.Sprintf("job run %s", name),
		"Internal job execution command", "Run a plugin-defined job",
	}
	rewritten := strings.NewReplacer(replacements...).Replace(resp.Output)
	_, err = fmt.Fprint(cmd.OutOrStdout(), rewritten)
	return err
}

func cleanPluginCommandError(err error) error {
	var processErr *plugin.RPCProcessError
	if errors.As(err, &processErr) && strings.TrimSpace(processErr.Detail) != "" {
		return errors.New(strings.TrimSpace(processErr.Detail))
	}
	return err
}
