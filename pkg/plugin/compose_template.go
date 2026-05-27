package plugin

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
)

// ComposeTemplateCreateOptions configures the SDK's standard Docker Compose
// template create runner.
type ComposeTemplateCreateOptions struct {
	DefaultPath         string
	DefaultPlugin       string
	DefaultEnvironment  string
	DefaultDrupalRootfs string
	DrupalContainerRoot string
	ConfirmOverwrite    bool
	ReadyMessage        string
	Input               config.InputFunc
}

// StandardComposeCommandOptions configures standard Docker Compose helper
// commands registered by the SDK.
type StandardComposeCommandOptions struct {
	DisplayName     string
	LogsTail        int
	BuildCommands   []string
	InitCommands    []string
	UpCommands      []string
	DownCommands    []string
	RolloutCommands []string
	RolloutCommand  string
}

// StandardComposeTemplateOptions configures the SDK's standard Compose
// template create runner and lifecycle commands from one application spec.
type StandardComposeTemplateOptions struct {
	DefaultPath         string
	DefaultPlugin       string
	DefaultEnvironment  string
	DefaultDrupalRootfs string
	DrupalContainerRoot string
	ConfirmOverwrite    bool
	ReadyMessage        string
	DisplayName         string
	LogsTail            int
	Input               config.InputFunc
}

type composeTemplateCreateRunner struct {
	sdk          *SDK
	spec         CreateSpec
	opts         ComposeTemplateCreateOptions
	drupalRootfs string
	bindErr      error
}

// RegisterComposeTemplateCreateRunner registers the SDK's standard Docker
// Compose template create runner for a plugin.
func RegisterComposeTemplateCreateRunner(s *SDK, spec CreateSpec, opts ComposeTemplateCreateOptions) {
	if s == nil {
		return
	}
	s.RegisterCreateRunner(spec, &composeTemplateCreateRunner{
		sdk:  s,
		spec: normalizeCreateSpec(spec),
		opts: opts,
	})
}

// RegisterComposeTemplateCreateRunner registers the SDK's standard Docker
// Compose template create runner for the receiver plugin.
func (s *SDK) RegisterComposeTemplateCreateRunner(spec CreateSpec, opts ComposeTemplateCreateOptions) {
	RegisterComposeTemplateCreateRunner(s, spec, opts)
}

// RegisterStandardComposeTemplate registers the standard create flow and
// lifecycle commands for a Docker Compose template plugin.
func RegisterStandardComposeTemplate(s *SDK, spec CreateSpec, opts StandardComposeTemplateOptions) {
	if s == nil {
		return
	}
	spec = normalizeCreateSpec(spec)
	RegisterComposeTemplateCreateRunner(s, spec, ComposeTemplateCreateOptions{
		DefaultPath:         opts.DefaultPath,
		DefaultPlugin:       opts.DefaultPlugin,
		DefaultEnvironment:  opts.DefaultEnvironment,
		DefaultDrupalRootfs: opts.DefaultDrupalRootfs,
		DrupalContainerRoot: opts.DrupalContainerRoot,
		ConfirmOverwrite:    opts.ConfirmOverwrite,
		ReadyMessage:        opts.ReadyMessage,
		Input:               opts.Input,
	})
	AddStandardComposeCommands(s, StandardComposeCommandOptions{
		DisplayName:     opts.DisplayName,
		LogsTail:        opts.LogsTail,
		BuildCommands:   spec.DockerComposeBuild,
		InitCommands:    spec.DockerComposeInit,
		UpCommands:      spec.DockerComposeUp,
		DownCommands:    spec.DockerComposeDown,
		RolloutCommands: spec.DockerComposeRollout,
	})
}

// RegisterStandardComposeTemplate registers the standard create flow and
// lifecycle commands for the receiver plugin.
func (s *SDK) RegisterStandardComposeTemplate(spec CreateSpec, opts StandardComposeTemplateOptions) {
	RegisterStandardComposeTemplate(s, spec, opts)
}

func (r *composeTemplateCreateRunner) BindFlags(cmd *cobra.Command) {
	if r.sdk == nil {
		r.bindErr = fmt.Errorf("plugin sdk is not initialized")
		return
	}
	var drupalRootfs *string
	if strings.TrimSpace(r.opts.DefaultDrupalRootfs) != "" || strings.TrimSpace(r.opts.DrupalContainerRoot) != "" {
		drupalRootfs = &r.drupalRootfs
	}
	if err := r.sdk.BindComposeCreateFlags(cmd, r.spec, drupalRootfs, r.opts.DefaultDrupalRootfs); err != nil {
		r.bindErr = err
	}
}

func (r *composeTemplateCreateRunner) Run(cmd *cobra.Command) error {
	if r.sdk == nil {
		return fmt.Errorf("plugin sdk is not initialized")
	}
	if r.bindErr != nil {
		return r.bindErr
	}
	input := r.opts.Input
	if input == nil {
		input = config.GetInput
	}
	req, err := r.sdk.ResolveComposeCreateRequest(cmd, input, r.drupalRootfs, "", r.spec.DockerComposeRepo, r.spec.DockerComposeBranch)
	if err != nil {
		return err
	}
	defaultPath := helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPath), "./"+helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPlugin), r.sdk.Metadata.Name))
	defaultBase := filepath.Base(helpers.FirstNonEmpty(req.Path, defaultPath))
	ctx, err := r.sdk.EnsureComposeCreateContext(req, ComposeCreateContextOptions{
		DefaultName:         defaultBase + "-local",
		DefaultSite:         defaultBase,
		DefaultPlugin:       helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPlugin), r.spec.Plugin, r.sdk.Metadata.Name),
		DefaultProjectDir:   defaultPath,
		DefaultProjectName:  defaultBase,
		DefaultEnvironment:  helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultEnvironment), "local"),
		DefaultDrupalRootfs: r.opts.DefaultDrupalRootfs,
		DrupalContainerRoot: r.opts.DrupalContainerRoot,
		ConfirmOverwrite:    r.opts.ConfirmOverwrite,
		Input:               input,
	})
	if err != nil {
		return err
	}
	cloned, err := r.sdk.EnsureComposeTemplateCheckout(cmd.OutOrStdout(), req, ctx)
	if err != nil {
		return err
	}
	if cloned {
		if err := r.sdk.RunComposeProjectCommandList(cmd, ctx, r.spec.DockerComposeInit); err != nil {
			return err
		}
	}
	if !req.SetupOnly {
		if err := r.sdk.RunComposeProjectCommandList(cmd, ctx, r.spec.DockerComposeUp); err != nil {
			return err
		}
	}
	PrintComposeTemplateCreateSummary(cmd.OutOrStdout(), ctx, r.opts.ReadyMessage, req.SetupOnly)
	return nil
}

// EnsureComposeTemplateCheckout ensures the requested Docker Compose template
// exists for the target context and returns whether a new checkout was cloned.
func (s *SDK) EnsureComposeTemplateCheckout(out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("plugin sdk is not initialized")
	}
	if req.CheckoutSource == CheckoutSourceExisting {
		return false, nil
	}
	if strings.TrimSpace(req.TemplateRepo) == "" {
		return false, fmt.Errorf("template repo cannot be empty")
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return false, fmt.Errorf("project directory cannot be empty")
	}
	if ctx.DockerHostType == config.ContextRemote {
		return s.ensureRemoteComposeTemplateCheckout(out, req, ctx)
	}
	return s.ensureLocalComposeTemplateCheckout(out, req, ctx.ProjectDir)
}

func (s *SDK) ensureLocalComposeTemplateCheckout(out io.Writer, req ComposeCreateRequest, projectDir string) (bool, error) {
	entries, err := os.ReadDir(projectDir)
	if err == nil && len(entries) > 0 {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read project directory %q: %w", projectDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(projectDir), 0o750); err != nil {
		return false, fmt.Errorf("create parent directory for %q: %w", projectDir, err)
	}
	fmt.Fprintf(out, "Cloning %s into %s\n", req.TemplateRepo, projectDir)
	if err := s.CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:   req.TemplateRepo,
		TemplateBranch: req.TemplateBranch,
		ProjectDir:     projectDir,
		Quiet:          true,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SDK) ensureRemoteComposeTemplateCheckout(out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	output, err := runRemoteShellCommand(ctx, nil, nil, fmt.Sprintf("if [ -d %s ] && [ -n \"$(ls -A %s 2>/dev/null)\" ]; then echo present; fi", shellQuote(ctx.ProjectDir), shellQuote(ctx.ProjectDir)))
	if err == nil && strings.TrimSpace(output) == "present" {
		return false, nil
	}
	if _, err := runRemoteShellCommand(ctx, nil, nil, fmt.Sprintf("mkdir -p %s", shellQuote(filepath.Dir(ctx.ProjectDir)))); err != nil {
		return false, fmt.Errorf("prepare remote parent directory: %w", err)
	}
	cloneCmd := fmt.Sprintf("git clone --branch %s %s %s && rm -rf %s/.git && git -C %s init -b %s", shellQuote(req.TemplateBranch), shellQuote(req.TemplateRepo), shellQuote(ctx.ProjectDir), shellQuote(ctx.ProjectDir), shellQuote(ctx.ProjectDir), shellQuote(req.TemplateBranch))
	fmt.Fprintf(out, "Cloning %s into %s on %s\n", req.TemplateRepo, ctx.ProjectDir, ctx.SSHHostname)
	if _, err := runRemoteShellCommand(ctx, io.Discard, io.Discard, cloneCmd); err != nil {
		return false, err
	}
	return true, nil
}

// RunComposeProjectCommandList runs a list of shell commands in a compose
// project's directory, skipping empty command strings.
func (s *SDK) RunComposeProjectCommandList(cmd *cobra.Command, ctx *config.Context, commands []string) error {
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", command)
		if err := s.RunComposeProjectCommand(ctx, ctx.ProjectDir, cmd.OutOrStdout(), cmd.ErrOrStderr(), command); err != nil {
			return err
		}
	}
	return nil
}

// RunComposeProjectCommand runs a shell command in a compose project directory,
// honoring local and remote sitectl contexts.
func (s *SDK) RunComposeProjectCommand(ctx *config.Context, projectDir string, stdout, stderr io.Writer, command string) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if ctx.DockerHostType == config.ContextRemote {
		remoteCommand := command
		if strings.TrimSpace(projectDir) != "" {
			remoteCommand = fmt.Sprintf("cd %s && %s", shellQuote(projectDir), command)
		}
		_, err := runRemoteShellCommand(ctx, stdout, stderr, remoteCommand)
		return err
	}
	localCmd := exec.Command("bash", "-lc", command) // #nosec G204 -- command text is assembled from template-owned command lists and shell-quoted inputs.
	localCmd.Dir = projectDir
	localCmd.Stdout = stdout
	localCmd.Stderr = stderr
	localCmd.Env = os.Environ()
	return localCmd.Run()
}

func runRemoteShellCommand(ctx *config.Context, stdout, stderr io.Writer, command string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is nil")
	}
	client, err := ctx.DialSSH()
	if err != nil {
		return "", fmt.Errorf("error establishing SSH connection: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("error creating SSH session: %w", err)
	}
	defer session.Close()

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	if stdout != nil {
		session.Stdout = stdout
	} else {
		session.Stdout = &outBuf
	}
	if stderr != nil {
		session.Stderr = stderr
	} else {
		session.Stderr = &errBuf
	}

	remoteCmd := exec.Command("bash", "-lc", command) // #nosec G204 -- command text is assembled from template-owned command lists and shell-quoted inputs.
	remoteCommand := shellJoin(remoteCmd.Args)
	if err := session.Run(remoteCommand); err != nil {
		return strings.TrimRight(outBuf.String()+errBuf.String(), "\n"), err
	}
	return strings.TrimRight(outBuf.String()+errBuf.String(), "\n"), nil
}

// PrintComposeTemplateCreateSummary renders the standard create completion
// summary for compose-template plugins.
func PrintComposeTemplateCreateSummary(out io.Writer, ctx *config.Context, readyMessage string, setupOnly bool) {
	if strings.TrimSpace(readyMessage) == "" {
		readyMessage = "The stack is ready for use through sitectl."
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, corecomponent.RenderSection("Create complete", readyMessage))
	fmt.Fprintln(out)
	if ctx != nil {
		fmt.Fprintf(out, "Checkout: %s\n", ctx.ProjectDir)
		fmt.Fprintf(out, "Context:  %s\n", ctx.Name)
	}
	if setupOnly {
		fmt.Fprintln(out, "The stack was prepared but left stopped because --setup-only was used.")
	}
}

// AddStandardComposeCommands registers standard lifecycle, status, logs, and
// rollout commands for a Docker Compose-backed plugin.
func AddStandardComposeCommands(s *SDK, opts StandardComposeCommandOptions) {
	if s == nil {
		return
	}
	displayName := helpers.FirstNonEmpty(strings.TrimSpace(opts.DisplayName), s.Metadata.Name)
	tail := opts.LogsTail
	if tail <= 0 {
		tail = 200
	}
	rolloutCommands := opts.RolloutCommands
	if len(rolloutCommands) == 0 {
		rolloutCommands = []string{helpers.FirstNonEmpty(strings.TrimSpace(opts.RolloutCommand), "make rollout")}
	}

	if len(opts.BuildCommands) > 0 {
		s.AddCommand(&cobra.Command{
			Use:   "build",
			Short: fmt.Sprintf("Build Docker Compose images for the active %s stack", displayName),
			RunE: func(cmd *cobra.Command, args []string) error {
				return s.RunActiveComposeProjectCommandList(cmd, opts.BuildCommands)
			},
		})
	}
	if len(opts.InitCommands) > 0 {
		s.AddCommand(&cobra.Command{
			Use:   "init",
			Short: fmt.Sprintf("Initialize the active %s stack", displayName),
			RunE: func(cmd *cobra.Command, args []string) error {
				return s.RunActiveComposeProjectCommandList(cmd, opts.InitCommands)
			},
		})
	}
	if len(opts.UpCommands) > 0 {
		s.AddCommand(&cobra.Command{
			Use:   "up",
			Short: fmt.Sprintf("Start the active %s stack", displayName),
			RunE: func(cmd *cobra.Command, args []string) error {
				return s.RunActiveComposeProjectCommandList(cmd, opts.UpCommands)
			},
		})
	}
	if len(opts.DownCommands) > 0 {
		s.AddCommand(&cobra.Command{
			Use:   "down",
			Short: fmt.Sprintf("Stop the active %s stack", displayName),
			RunE: func(cmd *cobra.Command, args []string) error {
				return s.RunActiveComposeProjectCommandList(cmd, opts.DownCommands)
			},
		})
	}

	s.AddCommand(&cobra.Command{
		Use:   "status",
		Short: fmt.Sprintf("Show Docker Compose service status for the active %s stack", displayName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.RunActiveComposeProjectCommand(cmd, "docker compose ps")
		},
	})
	s.AddCommand(&cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: fmt.Sprintf("Show recent Docker Compose logs for the active %s stack", displayName),
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			command := fmt.Sprintf("docker compose logs --tail=%d", tail)
			if len(args) > 0 {
				command += " " + shellJoin(args)
			}
			return s.RunActiveComposeProjectCommand(cmd, command)
		},
	})
	s.AddCommand(&cobra.Command{
		Use:   "rollout",
		Short: fmt.Sprintf("Run the template rollout script for the active %s stack", displayName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.RunActiveComposeProjectCommandList(cmd, rolloutCommands)
		},
	})
}

// AddStandardComposeCommands registers standard lifecycle, status, logs, and
// rollout commands for the receiver plugin.
func (s *SDK) AddStandardComposeCommands(opts StandardComposeCommandOptions) {
	AddStandardComposeCommands(s, opts)
}

// RunActiveComposeProjectCommandList runs shell commands in the active
// context's compose project directory.
func (s *SDK) RunActiveComposeProjectCommandList(cmd *cobra.Command, commands []string) error {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	return s.RunComposeProjectCommandList(cmd, ctx, commands)
}

// RunActiveComposeProjectCommand runs a shell command in the active context's
// compose project directory.
func (s *SDK) RunActiveComposeProjectCommand(cmd *cobra.Command, command string) error {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	return s.RunComposeProjectCommand(ctx, ctx.ProjectDir, cmd.OutOrStdout(), cmd.ErrOrStderr(), command)
}

// ContextFromCommand loads the sitectl context selected by a Cobra command.
func (s *SDK) ContextFromCommand(cmd *cobra.Command) (*config.Context, error) {
	if s == nil {
		return nil, fmt.Errorf("plugin sdk is not initialized")
	}
	contextName := ""
	if cmd != nil {
		if flag := cmd.Flags().Lookup("context"); flag != nil {
			contextName = strings.TrimSpace(flag.Value.String())
		}
		if contextName == "" {
			if flag := cmd.InheritedFlags().Lookup("context"); flag != nil {
				contextName = strings.TrimSpace(flag.Value.String())
			}
		}
		if contextName == "" && cmd.Root() != nil {
			if flag := cmd.Root().PersistentFlags().Lookup("context"); flag != nil {
				contextName = strings.TrimSpace(flag.Value.String())
			}
		}
	}
	s.Config.Context = contextName
	return s.GetContext()
}

// DockerComposeExecCommand builds a shell-safe "docker compose exec -T" command
// for a service and argv-style command.
func DockerComposeExecCommand(service string, args ...string) string {
	invocation := make([]string, 0, len(args)+5)
	invocation = append(invocation, "docker", "compose", "exec", "-T", service)
	invocation = append(invocation, args...)
	return ShellJoin(invocation)
}

// ShellJoin shell-quotes and joins argv-style command arguments.
func ShellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, ShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// ShellQuote quotes a value for POSIX shell command construction.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shellJoin(args []string) string {
	return ShellJoin(args)
}

func shellQuote(value string) string {
	return ShellQuote(value)
}
