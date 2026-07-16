package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
)

// ComposeTemplateCreateOptions configures the SDK's standard Docker Compose
// template create runner.
type ComposeTemplateCreateOptions struct {
	DefaultPath                   string
	DefaultPlugin                 string
	DefaultEnvironment            string
	DefaultDatabaseService        string
	DefaultDatabaseUser           string
	DefaultDatabasePasswordSecret string
	DefaultDatabaseName           string
	DefaultDrupalRootfs           string
	DrupalContainerRoot           string
	ConfirmOverwrite              bool
	ReadyMessage                  string
	Input                         config.InputFunc
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

var runComposeProjectRemoteShellCommandContext = runRemoteShellCommandContext

// StandardComposeTemplateOptions configures the SDK's standard Compose
// template create runner and lifecycle commands from one application spec.
type StandardComposeTemplateOptions struct {
	DefaultPath                   string
	DefaultPlugin                 string
	DefaultEnvironment            string
	DefaultDatabaseService        string
	DefaultDatabaseUser           string
	DefaultDatabasePasswordSecret string
	DefaultDatabaseName           string
	DefaultDrupalRootfs           string
	DrupalContainerRoot           string
	ConfirmOverwrite              bool
	ReadyMessage                  string
	DisplayName                   string
	LogsTail                      int
	Input                         config.InputFunc
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
		DefaultPath:                   opts.DefaultPath,
		DefaultPlugin:                 opts.DefaultPlugin,
		DefaultEnvironment:            opts.DefaultEnvironment,
		DefaultDatabaseService:        opts.DefaultDatabaseService,
		DefaultDatabaseUser:           opts.DefaultDatabaseUser,
		DefaultDatabasePasswordSecret: opts.DefaultDatabasePasswordSecret,
		DefaultDatabaseName:           opts.DefaultDatabaseName,
		DefaultDrupalRootfs:           opts.DefaultDrupalRootfs,
		DrupalContainerRoot:           opts.DrupalContainerRoot,
		ConfirmOverwrite:              opts.ConfirmOverwrite,
		ReadyMessage:                  opts.ReadyMessage,
		Input:                         opts.Input,
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
	pluginName := helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPlugin), r.spec.Plugin, r.sdk.Metadata.Name)
	req, err := r.sdk.ResolveComposeCreateRequest(cmd, input, pluginName, r.drupalRootfs, "", r.spec.DockerComposeRepo, r.spec.DockerComposeBranch)
	if err != nil {
		return err
	}
	defaultPath := helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPath), "./"+helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultPlugin), r.sdk.Metadata.Name))
	defaultBase := filepath.Base(helpers.FirstNonEmpty(req.Path, defaultPath))
	ctx, err := r.sdk.EnsureComposeCreateContext(req, ComposeCreateContextOptions{
		DefaultName:                   defaultBase + "-local",
		DefaultSite:                   defaultBase,
		DefaultPlugin:                 pluginName,
		DefaultProjectDir:             defaultPath,
		DefaultProjectName:            defaultBase,
		DefaultEnvironment:            helpers.FirstNonEmpty(strings.TrimSpace(r.opts.DefaultEnvironment), "local"),
		DefaultDatabaseService:        r.opts.DefaultDatabaseService,
		DefaultDatabaseUser:           r.opts.DefaultDatabaseUser,
		DefaultDatabasePasswordSecret: r.opts.DefaultDatabasePasswordSecret,
		DefaultDatabaseName:           r.opts.DefaultDatabaseName,
		DefaultDrupalRootfs:           r.opts.DefaultDrupalRootfs,
		DrupalContainerRoot:           r.opts.DrupalContainerRoot,
		ConfirmOverwrite:              r.opts.ConfirmOverwrite,
		Input:                         input,
	})
	if err != nil {
		return err
	}
	if err := r.sdk.EnsureRemoteCreatePrerequisitesContext(cmd.Context(), cmd.OutOrStdout(), ctx, RemoteCreatePrerequisitesOptions{
		Yolo:  req.Yolo,
		Input: input,
	}); err != nil {
		return err
	}
	applyRemoteIngressCreateDefaults(ctx, req.Decisions)
	cloned, err := r.sdk.EnsureComposeTemplateCheckoutContext(cmd.Context(), cmd.OutOrStdout(), req, ctx)
	if err != nil {
		return err
	}
	if err := refreshCreateContextComposeIdentity(ctx, req); err != nil {
		return err
	}
	if err := r.sdk.reconcileCreateServiceComponents(cmd.Context(), ctx, req.Decisions); err != nil {
		return err
	}
	if !req.ImageOverrides.Empty() {
		if ctx.DockerHostType == config.ContextRemote {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: modifying remote project files directly; commit and review these changes through version control before promoting them.")
		}
		if err := ApplyComposeImageOverridesContext(ctx, req.ImageOverrides); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", ComposeImageOverrideFile)
	}
	needsInit, err := composeTemplateNeedsInit(ctx, r.spec)
	if err != nil {
		return err
	}
	if cloned || needsInit {
		if err := r.sdk.RunComposeProjectCommandList(cmd, ctx, r.spec.DockerComposeInit); err != nil {
			return err
		}
	}
	if !req.SetupOnly {
		if err := r.sdk.RunComposeProjectCommandList(cmd, ctx, r.spec.DockerComposeBuild); err != nil {
			return err
		}
		if err := r.sdk.RunComposeProjectCommandList(cmd, ctx, r.spec.DockerComposeUp); err != nil {
			return err
		}
	}
	PrintComposeTemplateCreateSummary(cmd.OutOrStdout(), ctx, r.opts.ReadyMessage, req.SetupOnly)
	return nil
}

func applyRemoteIngressCreateDefaults(ctx *config.Context, decisions map[string]corecomponent.ReviewDecision) {
	if ctx == nil || ctx.DockerHostType != config.ContextRemote || strings.TrimSpace(ctx.SSHHostname) == "" {
		return
	}
	decision, ok := decisions["ingress"]
	if !ok {
		return
	}
	if decision.Options == nil {
		decision.Options = map[string]string{}
	}
	domain := strings.TrimSpace(decision.Options["domain"])
	if domain != "" && !strings.EqualFold(domain, "localhost") {
		return
	}
	decision.Options["domain"] = strings.TrimSpace(ctx.SSHHostname)
	decisions["ingress"] = decision
}

func composeTemplateNeedsInit(ctx *config.Context, spec CreateSpec) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("context is nil")
	}
	for _, artifact := range spec.InitArtifacts {
		path := strings.TrimSpace(artifact.Path)
		if path == "" {
			continue
		}
		resolved := ctx.ResolveProjectPath(filepath.FromSlash(path))
		exists, err := ctx.FileExists(resolved)
		if err != nil {
			return false, fmt.Errorf("check init artifact %s: %w", path, err)
		}
		if !exists {
			return true, nil
		}
		data, err := ctx.ReadSmallFile(resolved)
		if err != nil {
			return false, fmt.Errorf("read init artifact %s: %w", path, err)
		}
		if strings.TrimSpace(data) == "" {
			return true, nil
		}
	}
	return false, nil
}

// EnsureComposeTemplateCheckout ensures the requested Docker Compose template
// exists for the target context and returns whether a new checkout was cloned.
func (s *SDK) EnsureComposeTemplateCheckout(out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	return s.EnsureComposeTemplateCheckoutContext(context.Background(), out, req, ctx)
}

// EnsureComposeTemplateCheckoutContext ensures the requested Docker Compose
// template exists for the target context with cancellation support.
func (s *SDK) EnsureComposeTemplateCheckoutContext(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("plugin sdk is not initialized")
	}
	if runCtx == nil {
		runCtx = context.Background()
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
		return s.ensureRemoteComposeTemplateCheckout(runCtx, out, req, ctx)
	}
	return s.ensureLocalComposeTemplateCheckout(runCtx, out, req, ctx.ProjectDir)
}

func (s *SDK) ensureLocalComposeTemplateCheckout(runCtx context.Context, out io.Writer, req ComposeCreateRequest, projectDir string) (bool, error) {
	projectDirExisted, notEmpty, err := localProjectDirectoryState(runCtx, projectDir)
	if err != nil {
		return false, fmt.Errorf("inspect project directory: %w", err)
	}
	if notEmpty {
		return false, fmt.Errorf("project directory %q is not empty; choose checkout source %q to use an existing checkout", projectDir, CheckoutSourceExisting)
	}
	if err := os.MkdirAll(filepath.Dir(projectDir), 0o750); err != nil {
		return false, fmt.Errorf("create parent directory for %q: %w", projectDir, err)
	}
	ownedProjectDir := false
	if !projectDirExisted {
		if err := os.Mkdir(projectDir, 0o750); err != nil {
			return false, fmt.Errorf("claim project directory %q for template checkout: %w", projectDir, err)
		}
		ownedProjectDir = true
	}
	fmt.Fprintf(out, "Cloning %s into %s\n", req.TemplateRepo, projectDir)
	if err := s.CloneTemplateRepoContext(runCtx, GitTemplateOptions{
		TemplateRepo:   req.TemplateRepo,
		TemplateBranch: req.TemplateBranch,
		ProjectDir:     projectDir,
		Quiet:          true,
	}); err != nil {
		if !ownedProjectDir {
			return false, err
		}
		if cleanupErr := os.RemoveAll(projectDir); cleanupErr != nil {
			return false, errors.Join(err, fmt.Errorf("clean up failed template checkout %q: %w", projectDir, cleanupErr))
		}
		return false, err
	}
	return true, nil
}

func localProjectDirectoryState(runCtx context.Context, projectDir string) (bool, bool, error) {
	if err := runCtx.Err(); err != nil {
		return false, false, err
	}
	info, err := os.Lstat(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("inspect project directory %q: %w", projectDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true, false, fmt.Errorf("project directory %q must be a real directory, not a symlink or other file", projectDir)
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return false, false, fmt.Errorf("read project directory %q: %w", projectDir, err)
	}
	return true, len(entries) > 0, nil
}

func refreshCreateContextComposeIdentity(ctx *config.Context, req ComposeCreateRequest) error {
	if ctx == nil {
		return nil
	}
	changed := false
	if strings.TrimSpace(req.ComposeProjectName) == "" {
		if detected := config.DetectContextComposeProjectName(ctx); detected != "" && detected != ctx.ComposeProjectName {
			ctx.ComposeProjectName = detected
			changed = true
		}
	}
	if strings.TrimSpace(req.ComposeNetwork) == "" {
		if detected := config.DetectContextComposeNetwork(ctx); detected != "" && detected != ctx.ComposeNetwork {
			ctx.ComposeNetwork = detected
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return config.SaveContext(ctx, false)
}

func (s *SDK) ensureRemoteComposeTemplateCheckout(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	connection, err := openRemoteTemplateConnection(runCtx, ctx)
	if err != nil {
		return false, fmt.Errorf("open remote template connection: %w", err)
	}
	defer connection.Close()
	projectDirExisted, notEmpty, err := remoteProjectDirectoryState(runCtx, connection, ctx.ProjectDir)
	if err != nil {
		return false, fmt.Errorf("inspect remote project directory: %w", err)
	}
	if notEmpty {
		return false, fmt.Errorf("remote project directory %q is not empty; choose checkout source %q to use an existing checkout", ctx.ProjectDir, CheckoutSourceExisting)
	}
	templateRepo, err := validateTemplateRepository(req.TemplateRepo)
	if err != nil {
		return false, err
	}
	if err := connection.MkdirAll(path.Dir(ctx.ProjectDir)); err != nil {
		return false, fmt.Errorf("prepare remote parent directory: %w", err)
	}
	ownedProjectDir := false
	if !projectDirExisted {
		if err := connection.Mkdir(ctx.ProjectDir); err != nil {
			return false, fmt.Errorf("claim remote project directory %q for template checkout: %w", ctx.ProjectDir, err)
		}
		ownedProjectDir = true
		if err := connection.Chmod(ctx.ProjectDir, 0o750); err != nil {
			claimErr := fmt.Errorf("set remote project directory permissions: %w", err)
			return false, cleanupRemoteTemplateCheckout(connection, ctx.ProjectDir, false, claimErr)
		}
	}
	cloneArgs := []string{"git", "clone"}
	if strings.TrimSpace(req.TemplateBranch) != "" {
		cloneArgs = append(cloneArgs, "--branch", req.TemplateBranch)
	}
	cloneArgs = append(cloneArgs, "--", templateRepo, ctx.ProjectDir)
	fmt.Fprintf(out, "Cloning %s into %s on %s\n", templateRepo, ctx.ProjectDir, ctx.SSHHostname)
	if _, err := connection.Run(runCtx, io.Discard, nil, cloneArgs...); err != nil {
		cloneErr := fmt.Errorf("clone remote template repo %q: %w", templateRepo, err)
		if !ownedProjectDir {
			return false, cloneErr
		}
		return false, cleanupRemoteTemplateCheckout(connection, ctx.ProjectDir, false, cloneErr)
	}
	metadata, err := inspectRemoteTemplateCheckout(runCtx, connection, ctx.ProjectDir)
	if err != nil {
		return false, cleanupRemoteTemplateCheckout(connection, ctx.ProjectDir, !ownedProjectDir, err)
	}
	sitectl, plugins := s.templateLockPackages()
	lock, err := buildTemplateLock(templateRepo, metadata, sitectl, plugins)
	if err != nil {
		return false, cleanupRemoteTemplateCheckout(connection, ctx.ProjectDir, !ownedProjectDir, err)
	}
	if err := finalizeRemoteTemplateCheckout(runCtx, connection, ctx.ProjectDir, req.TemplateBranch, lock); err != nil {
		finalizeErr := fmt.Errorf("finalize remote template checkout: %w", err)
		return false, cleanupRemoteTemplateCheckout(connection, ctx.ProjectDir, !ownedProjectDir, finalizeErr)
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
		if err := s.RunComposeProjectCommandContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.OutOrStdout(), cmd.ErrOrStderr(), command); err != nil {
			return err
		}
	}
	return nil
}

// RunComposeProjectCommand runs a shell command in a compose project directory,
// honoring local and remote sitectl contexts.
func (s *SDK) RunComposeProjectCommand(ctx *config.Context, projectDir string, stdout, stderr io.Writer, command string) error {
	return s.RunComposeProjectCommandContext(context.Background(), ctx, projectDir, stdout, stderr, command)
}

// RunComposeProjectCommandContext runs a shell command in a compose project
// directory with cancellation support.
func (s *SDK) RunComposeProjectCommandContext(runCtx context.Context, ctx *config.Context, projectDir string, stdout, stderr io.Writer, command string) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	composeUp := isComposeProjectUpCommand(command)
	command = ctx.DockerComposeShellCommand(command)
	if ctx.DockerHostType == config.ContextRemote {
		remoteCommand := command
		if strings.TrimSpace(projectDir) != "" {
			remoteCommand = fmt.Sprintf("cd %s && %s", shellQuote(projectDir), command)
		}
		_, err := runComposeProjectRemoteShellCommandContext(runCtx, ctx, stdout, stderr, remoteCommand)
		return err
	}
	localCmd := exec.CommandContext(runCtx, "bash", "-lc", command) // #nosec G204 -- command text is assembled from template-owned command lists and shell-quoted inputs.
	localCmd.Dir = projectDir
	localCmd.Stdout = stdout
	localCmd.Stderr = stderr
	localCmd.Env = os.Environ()
	if composeUp {
		envValues, messages, err := ctx.PrepareComposeUpPortOverride()
		if err != nil {
			return err
		}
		for _, message := range messages {
			if stderr != nil {
				fmt.Fprintln(stderr, message)
			}
		}
		localCmd.Env = config.AppendEnvOverrides(localCmd.Env, envValues)
	}
	return localCmd.Run()
}

func isComposeProjectUpCommand(command string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}
	if fields[0] == "make" {
		for _, field := range fields[1:] {
			if field == "up" {
				return true
			}
		}
		return false
	}
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] == "docker" && fields[i+1] == "compose" && fields[i+2] == "up" {
			return true
		}
	}
	return false
}

func runRemoteShellCommandContext(runCtx context.Context, ctx *config.Context, stdout, stderr io.Writer, command string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	client, err := ctx.DialSSH()
	if err != nil {
		return "", fmt.Errorf("error establishing SSH connection: %w", err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return "", fmt.Errorf("error creating SSH session: %w", err)
	}

	var closeOnce sync.Once
	closeResources := func() {
		_ = session.Close()
		_ = client.Close()
	}
	done := make(chan struct{})
	defer func() {
		close(done)
		closeOnce.Do(closeResources)
	}()
	go func() {
		select {
		case <-runCtx.Done():
			closeOnce.Do(closeResources)
		case <-done:
		}
	}()

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
		if runCtx.Err() != nil {
			return strings.TrimRight(outBuf.String()+errBuf.String(), "\n"), runCtx.Err()
		}
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
		if command := strings.TrimSpace(opts.RolloutCommand); command != "" {
			rolloutCommands = []string{command}
		} else {
			rolloutCommands = DefaultComposeRolloutCommands()
		}
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
		Short: fmt.Sprintf("Roll out the active %s stack", displayName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.RunActiveComposeProjectRollout(cmd, rolloutCommands)
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

// DefaultComposeRolloutCommands returns the generic Compose rollout sequence.
func DefaultComposeRolloutCommands() []string {
	return []string{
		"docker compose pull --ignore-buildable --quiet || docker compose pull --ignore-buildable || true",
		"docker compose build --pull",
		"docker compose up --remove-orphans --wait --pull missing --quiet-pull -d",
	}
}

// RunActiveComposeProjectRollout syncs the active project from the checkout's
// upstream branch before running rollout commands.
func (s *SDK) RunActiveComposeProjectRollout(cmd *cobra.Command, commands []string) error {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	if syncCommand := ctx.GitSyncShellCommand(""); strings.TrimSpace(syncCommand) != "" {
		commands = append([]string{syncCommand}, commands...)
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
	return s.RunComposeProjectCommandContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.OutOrStdout(), cmd.ErrOrStderr(), command)
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
