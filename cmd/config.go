package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage sitectl command configuration",
	Long: `
A sitectl config can have multiple contexts.

A sitectl context is a docker compose site running somewhere. "Somewhere" meaning:

- on your laptop (--type local)
- on a remote server (--type remote).

Remote contexts require SSH access to the remote server from where sitectl is being ran from.
When creating a context the remote server DNS name, SSH port, SSH username, and the path to your SSH private key will need to be set in the context configuration.

You can have a default context which will be used when running sitectl commands, unless the context is overridden with the --context flag.`,
}

var viewConfigCmd = &cobra.Command{
	Use:   "view",
	Short: "Print your sitectl config",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.ConfigFilePath()
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(cmd.OutOrStdout(), "File %q does not exist.\n", path)
				return nil
			}
			return fmt.Errorf("error checking file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("error reading file: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	},
}

var currentContextCmd = &cobra.Command{
	Use:   "current-context",
	Short: "Display the current site context",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := config.Current()
		if err != nil {
			return err
		}
		if c == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "No current context is set")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Current context:", c)
		}
		return nil
	},
}

var getContextsCmd = &cobra.Command{
	Use:   "get-contexts",
	Short: "List all site contexts",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if len(cfg.Contexts) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No contexts available")
			return nil
		}
		writeContextTable(cmd.OutOrStdout(), cfg)
		return nil
	},
}

var getSitesCmd = &cobra.Command{
	Use:   "get-sites",
	Short: "List configured sites and their environments",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if len(cfg.Contexts) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No sites available")
			return nil
		}

		sites := map[string][]config.Context{}
		for _, ctx := range cfg.Contexts {
			site := helpers.FirstNonEmpty(ctx.Site, "-")
			sites[site] = append(sites[site], ctx)
		}

		names := make([]string, 0, len(sites))
		for name := range sites {
			names = append(names, name)
		}
		sort.Strings(names)

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SITE\tPLUGIN\tENVIRONMENTS\tCONTEXTS")
		for _, site := range names {
			contexts := sites[site]
			sort.Slice(contexts, func(i, j int) bool {
				return contexts[i].Name < contexts[j].Name
			})
			plugin := helpers.FirstNonEmpty(contexts[0].Plugin, "-")
			envs := uniqueSortedContextValues(contexts, func(ctx config.Context) string {
				return helpers.FirstNonEmpty(ctx.Environment, "-")
			})
			names := make([]string, 0, len(contexts))
			for _, ctx := range contexts {
				label := ctx.Name
				if ctx.Name == cfg.CurrentContext {
					label += " *"
				}
				names = append(names, label)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				site,
				plugin,
				strings.Join(envs, ", "),
				strings.Join(names, ", "),
			)
		}
		_ = w.Flush()
		return nil
	},
}

var getEnvironmentsCmd = &cobra.Command{
	Use:   "get-environments [site]",
	Short: "List environments grouped by site",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if len(cfg.Contexts) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No environments available")
			return nil
		}

		siteFilter := ""
		if len(args) == 1 {
			siteFilter = strings.TrimSpace(args[0])
		}

		contexts := make([]config.Context, 0, len(cfg.Contexts))
		for _, ctx := range cfg.Contexts {
			if siteFilter != "" && !strings.EqualFold(ctx.Site, siteFilter) {
				continue
			}
			contexts = append(contexts, ctx)
		}
		if len(contexts) == 0 {
			if siteFilter == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No environments available")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "No environments found for site %q\n", siteFilter)
			}
			return nil
		}

		sort.Slice(contexts, func(i, j int) bool {
			if contexts[i].Site != contexts[j].Site {
				return contexts[i].Site < contexts[j].Site
			}
			if contexts[i].Environment != contexts[j].Environment {
				return contexts[i].Environment < contexts[j].Environment
			}
			return contexts[i].Name < contexts[j].Name
		})

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SITE\tENVIRONMENT\tCONTEXT\tPLUGIN\tTYPE\tHOST")
		for _, ctx := range contexts {
			host := "-"
			if ctx.DockerHostType == config.ContextRemote {
				host = helpers.FirstNonEmpty(ctx.SSHHostname, "-")
			}
			name := ctx.Name
			if ctx.Name == cfg.CurrentContext {
				name += " *"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				helpers.FirstNonEmpty(ctx.Site, "-"),
				helpers.FirstNonEmpty(ctx.Environment, "-"),
				name,
				helpers.FirstNonEmpty(ctx.Plugin, "-"),
				helpers.FirstNonEmpty(string(ctx.DockerHostType), "-"),
				host,
			)
		}
		_ = w.Flush()
		return nil
	},
}

var (
	configValidateAll    bool
	configValidateSite   string
	configValidateFormat string
)

var validateConfigCmd = &cobra.Command{
	Use:   "validate [context-name]",
	Args:  cobra.MaximumNArgs(1),
	Short: "Validate sitectl context configuration and access",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		contexts, err := resolveValidationContexts(cfg, args)
		if err != nil {
			return err
		}
		if len(contexts) == 0 {
			return fmt.Errorf("no contexts selected for validation")
		}

		reports := make([]sitevalidate.Report, 0, len(contexts))
		valid := true
		for i := range contexts {
			results, err := sitevalidate.Run(&contexts[i], sitevalidate.CoreValidators(cfg)...)
			if err != nil {
				return err
			}
			sitevalidate.SortResults(results)
			report := sitevalidate.NewReport(&contexts[i], results)
			if !report.Valid {
				valid = false
			}
			reports = append(reports, report)
		}

		if err := sitevalidate.WriteReports(cmd.OutOrStdout(), reports, configValidateFormat); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("validation failed")
		}
		return nil
	},
}

var setContextCmd = &cobra.Command{
	Use:   "set-context [context-name]",
	Short: "Set or update properties of a context. Creates a new context if it does not exist.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		context, err := config.GetContext(args[0])
		if err != nil {
			if !errors.Is(err, config.ErrContextNotFound) {
				return err
			}
			context = config.Context{Name: args[0]}
		}

		f := cmd.Flags()
		cc, err := config.LoadFromFlags(f, context)
		if err != nil {
			return err
		}
		cc.Name = args[0]

		defaultContext, err := f.GetBool("default")
		if err != nil {
			return err
		}

		// override local defaults for remote environments
		switch cc.DockerHostType {
		case config.ContextRemote:
			err = cc.VerifyRemoteInput(true)
			if err != nil {
				return err
			}
		case config.ContextLocal:
			cc.SSHKeyPath = ""
			cc.DockerSocket = config.GetDefaultLocalDockerSocket(cc.DockerSocket)
		default:
			return fmt.Errorf("unknown context type %q", cc.DockerHostType)
		}

		if err = config.SaveContext(cc, defaultContext); err != nil {
			return err
		}

		return nil
	},
}

var useContextCmd = &cobra.Command{
	Use:   "use-context [context-name]",
	Short: "Switch to the specified context",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		found := false
		for _, ctx := range cfg.Contexts {
			if ctx.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("context %q not found", name)
		}
		cfg.CurrentContext = name
		if err = config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Switched to context:", name)
		return nil
	},
}

var deleteContextCmd = &cobra.Command{
	Use:   "delete-context [context-name]",
	Short: "Delete a site context",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.CurrentContext == name {
			return fmt.Errorf("cannot delete the current context; switch to another context first")
		}
		found := false
		var newContexts []config.Context
		for _, ctx := range cfg.Contexts {
			if ctx.Name == name {
				found = true
				continue
			}
			newContexts = append(newContexts, ctx)
		}
		if !found {
			return fmt.Errorf("context %q not found", name)
		}
		cfg.Contexts = newContexts
		if err = config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted context: %s\n", name)
		return nil
	},
}

func writeContextTable(out io.Writer, cfg *config.Config) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CURRENT\tCONTEXT\tSITE\tPLUGIN\tENVIRONMENT\tTYPE\tPROJECT")
	for _, ctx := range cfg.Contexts {
		activeMark := ""
		if ctx.Name == cfg.CurrentContext {
			activeMark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			activeMark,
			ctx.Name,
			helpers.FirstNonEmpty(ctx.Site, "-"),
			helpers.FirstNonEmpty(ctx.Plugin, "-"),
			helpers.FirstNonEmpty(ctx.Environment, "-"),
			helpers.FirstNonEmpty(string(ctx.DockerHostType), "-"),
			helpers.FirstNonEmpty(ctx.ProjectName, "-"),
		)
	}
	_ = w.Flush()
}

func resolveValidationContexts(cfg *config.Config, args []string) ([]config.Context, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if configValidateAll {
		return filterValidationContexts(cfg.Contexts), nil
	}
	if strings.TrimSpace(configValidateSite) != "" {
		selected := []config.Context{}
		for _, ctx := range cfg.Contexts {
			if strings.EqualFold(ctx.Site, configValidateSite) {
				selected = append(selected, ctx)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("no contexts found for site %q", configValidateSite)
		}
		return selected, nil
	}
	if len(args) == 1 {
		ctx, err := config.GetContext(args[0])
		if err != nil {
			return nil, err
		}
		return []config.Context{ctx}, nil
	}
	current, err := config.Current()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(current) == "" {
		return nil, fmt.Errorf("no current context is set")
	}
	ctx, err := config.GetContext(current)
	if err != nil {
		return nil, err
	}
	return []config.Context{ctx}, nil
}

func filterValidationContexts(contexts []config.Context) []config.Context {
	selected := append([]config.Context{}, contexts...)
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Site != selected[j].Site {
			return selected[i].Site < selected[j].Site
		}
		if selected[i].Environment != selected[j].Environment {
			return selected[i].Environment < selected[j].Environment
		}
		return selected[i].Name < selected[j].Name
	})
	return selected
}

func uniqueSortedContextValues(contexts []config.Context, getter func(config.Context) string) []string {
	values := map[string]struct{}{}
	for _, ctx := range contexts {
		value := strings.TrimSpace(getter(ctx))
		if value == "" {
			continue
		}
		values[value] = struct{}{}
	}
	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return ordered
}

func init() {
	setFlags := setContextCmd.Flags()
	config.SetCommandFlags(setFlags)
	setFlags.Bool("default", false, "set to default context")

	validateConfigCmd.Flags().BoolVar(&configValidateAll, "all", false, "Validate all configured contexts")
	validateConfigCmd.Flags().StringVar(&configValidateSite, "site", "", "Validate all contexts for a specific site")
	corecomponent.AddReportFlags(validateConfigCmd, nil, &configValidateFormat)

	configCmd.AddCommand(viewConfigCmd)
	configCmd.AddCommand(currentContextCmd)
	configCmd.AddCommand(getContextsCmd)
	configCmd.AddCommand(getSitesCmd)
	configCmd.AddCommand(getEnvironmentsCmd)
	configCmd.AddCommand(validateConfigCmd)
	configCmd.AddCommand(setContextCmd)
	configCmd.AddCommand(useContextCmd)
	configCmd.AddCommand(deleteContextCmd)
	configCmd.GroupID = "setup"
	RootCmd.AddCommand(configCmd)
}
