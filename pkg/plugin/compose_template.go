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
	"strconv"
	"strings"
	"sync"

	corelifecycle "github.com/libops/sitectl/internal/lifecycle"
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
var runComposeProjectLocalArgvContext = runLocalComposeProjectArgvContext
var runComposeProjectRemoteArgvContext = runRemoteComposeProjectArgvContext
var composeProjectDockerVisibleLocalPath = config.DockerVisibleLocalPath
var resolveComposeProjectHostIdentity = composeProjectHostIdentity
var resolveLocalComposeProjectHostNumericIdentity = config.LocalComposeHostNumericIdentity
var acquireComposeProjectMutationLock = func(runCtx context.Context, ctx *config.Context) (*config.ProjectMutationLock, error) {
	return ctx.AcquireProjectMutationLock(runCtx)
}
var syncComposeProjectCheckout = func(runCtx context.Context, ctx *config.Context, stdout io.Writer) error {
	return ctx.SyncGitCheckout(runCtx, stdout, "")
}

// ComposeProjectHost describes values that differ between local and remote
// Compose hosts. Use it to build typed argv for tools that need host ownership
// or a project-directory bind mount without shell interpolation.
type ComposeProjectHost struct {
	// ProjectDir is the project path visible to the Docker host for bind mounts.
	// For local sshfs workspaces it can differ from the CLI's working path.
	ProjectDir string
	// UID and GID are numeric POSIX ownership values when HasNumericIdentity is true.
	UID string
	GID string
	// HasNumericIdentity is false for native Windows Compose hosts.
	HasNumericIdentity bool
}

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
	// An empty artifact list means Compose owns the init-state contract. The
	// init commands must remain idempotent so forks can add or rename Compose
	// secrets and volumes without requiring a matching plugin release.
	if len(spec.InitArtifacts) == 0 && len(spec.DockerComposeInit) > 0 {
		return true, nil
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
	if detected := config.DetectContextComposeFile(ctx); detected != "" {
		configuredExists := false
		for _, name := range ctx.ComposeFile {
			exists, err := ctx.FileExists(ctx.ResolveProjectPath(name))
			if err == nil && exists {
				configuredExists = true
				break
			}
		}
		if !configuredExists && (len(ctx.ComposeFile) != 1 || ctx.ComposeFile[0] != detected) {
			ctx.ComposeFile = []string{detected}
			changed = true
		}
	}
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
			return false, cleanupOwnedRemoteTemplateCheckout(connection, ctx.ProjectDir, claimErr)
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
		return false, cleanupOwnedRemoteTemplateCheckout(connection, ctx.ProjectDir, cloneErr)
	}
	metadata, err := inspectRemoteTemplateCheckout(runCtx, connection, ctx.ProjectDir)
	if err != nil {
		if !ownedProjectDir {
			return false, err
		}
		return false, cleanupOwnedRemoteTemplateCheckout(connection, ctx.ProjectDir, err)
	}
	sitectl, plugins := s.templateLockPackages()
	lock, err := buildTemplateLock(templateRepo, metadata, sitectl, plugins)
	if err != nil {
		if !ownedProjectDir {
			return false, err
		}
		return false, cleanupOwnedRemoteTemplateCheckout(connection, ctx.ProjectDir, err)
	}
	if err := finalizeRemoteTemplateCheckout(runCtx, connection, ctx.ProjectDir, req.TemplateBranch, lock); err != nil {
		finalizeErr := fmt.Errorf("finalize remote template checkout: %w", err)
		if !ownedProjectDir {
			return false, finalizeErr
		}
		return false, cleanupOwnedRemoteTemplateCheckout(connection, ctx.ProjectDir, finalizeErr)
	}
	return true, nil
}

// RunComposeProjectCommandList runs a list of constrained lifecycle commands
// in a compose project's directory, skipping empty command strings.
func (s *SDK) RunComposeProjectCommandList(cmd *cobra.Command, ctx *config.Context, commands []string) error {
	commandsToRun, err := validateComposeProjectCommandList(cmd.Context(), ctx, ctx.ProjectDir, commands)
	if err != nil {
		return err
	}
	for _, command := range commandsToRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", command)
		if err := s.RunComposeProjectCommandContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.OutOrStdout(), cmd.ErrOrStderr(), command); err != nil {
			return err
		}
	}
	return nil
}

func validateComposeProjectCommandList(runCtx context.Context, ctx *config.Context, projectDir string, commands []string) ([]string, error) {
	commandsToRun := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		if err := validateComposeProjectCommandContext(runCtx, ctx, projectDir, command); err != nil {
			return nil, fmt.Errorf("validate lifecycle command %q: %w", command, err)
		}
		commandsToRun = append(commandsToRun, command)
	}
	return commandsToRun, nil
}

func validateComposeProjectCommandContext(runCtx context.Context, ctx *config.Context, projectDir, command string) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if artifactPath, handled, err := composeProjectHostUIDArtifactPath(projectDir, command); err != nil {
		return err
	} else if handled {
		if err := ctx.ValidateProjectFileWrite(projectDir, artifactPath); err != nil {
			return fmt.Errorf("validate host uid artifact: %w", err)
		}
		_, _, err := composeProjectHostIdentity(runCtx, ctx)
		return err
	}
	expanded, err := expandComposeProjectHostIdentity(runCtx, ctx, command)
	if err != nil {
		return err
	}
	list, err := corelifecycle.Parse(expanded)
	if err != nil {
		return err
	}
	for _, segment := range list.Commands {
		argv, _, err := corelifecycle.ArgvInProject(ctx, projectDir, segment)
		if err != nil {
			return fmt.Errorf("parse lifecycle command %q: %w", segment, err)
		}
		script, resolved, err := corelifecycle.ProjectScriptPath(projectDir, argv)
		if err != nil {
			return err
		}
		if script == "" {
			continue
		}
		if err := ctx.ValidateProjectRegularFile(projectDir, resolved); err != nil {
			return fmt.Errorf("checked-in lifecycle script %q is invalid: %w", script, err)
		}
	}
	return nil
}

// RunComposeProjectCommand runs a constrained lifecycle command in a compose
// project directory, honoring local and remote sitectl contexts.
func (s *SDK) RunComposeProjectCommand(ctx *config.Context, projectDir string, stdout, stderr io.Writer, command string) error {
	return s.RunComposeProjectCommandContext(context.Background(), ctx, projectDir, stdout, stderr, command)
}

// RunComposeProjectCommandContext runs a constrained lifecycle command in a
// compose project directory with cancellation support.
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
	if err := validateComposeProjectCommandContext(runCtx, ctx, projectDir, command); err != nil {
		return fmt.Errorf("validate lifecycle command %q: %w", command, err)
	}
	handled, err := runComposeProjectHostUIDArtifact(runCtx, ctx, projectDir, command)
	if err != nil || handled {
		return err
	}
	command, err = expandComposeProjectHostIdentity(runCtx, ctx, command)
	if err != nil {
		return err
	}
	list, err := corelifecycle.Parse(command)
	if err != nil {
		return err
	}
	type commandPlan struct {
		argv      []string
		composeUp bool
	}
	plans := make([]commandPlan, len(list.Commands))
	for index, segment := range list.Commands {
		argv, composeUp, err := corelifecycle.ArgvInProject(ctx, projectDir, segment)
		if err != nil {
			return fmt.Errorf("parse lifecycle command %q: %w", segment, err)
		}
		plans[index] = commandPlan{argv: argv, composeUp: composeUp}
	}

	lastSucceeded := true
	var lastErr error
	for index := range list.Commands {
		if index > 0 {
			switch list.Operators[index-1] {
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
		loggedCommand := shellJoin(argv)
		if len(argv) >= 2 && argv[0] == "docker" && argv[1] == "compose" {
			loggedCommand = "docker compose"
			if len(argv) > 2 {
				loggedCommand += " " + shellJoin(argv[2:])
			}
		}
		config.LogDockerComposeCommand(ctx, loggedCommand)
		if ctx.DockerHostType == config.ContextRemote {
			remoteCommand := shellJoin(argv)
			if strings.TrimSpace(projectDir) != "" {
				remoteCommand = fmt.Sprintf("cd %s && %s", shellQuote(projectDir), remoteCommand)
			}
			_, lastErr = runComposeProjectRemoteShellCommandContext(runCtx, ctx, stdout, stderr, remoteCommand)
		} else {
			localCmd := exec.CommandContext(runCtx, argv[0], argv[1:]...) // #nosec G204 -- constrained lifecycle metadata is parsed into distinct argv entries.
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
			lastErr = localCmd.Run()
		}
		lastSucceeded = lastErr == nil
	}
	if !lastSucceeded {
		return fmt.Errorf("run lifecycle command %q: %w", command, lastErr)
	}
	return nil
}

// RunComposeProjectArgvContext executes one dynamic argv command without
// serializing and reparsing its arguments as lifecycle metadata. It preserves
// literal dollar signs, line breaks, spaces, and metacharacters in arguments.
func (s *SDK) RunComposeProjectArgvContext(runCtx context.Context, ctx *config.Context, projectDir string, stdin io.Reader, stdout, stderr io.Writer, argv []string) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := validateComposeProjectArgv(argv); err != nil {
		return err
	}

	effectiveContext := *ctx
	effectiveContext.ProjectDir = projectDir
	preparedArgv, composeUp := effectiveContext.DockerComposeArgv(argv)
	env := os.Environ()
	if composeUp {
		envValues, messages, err := effectiveContext.PrepareComposeUpPortOverride()
		if err != nil {
			return err
		}
		for _, message := range messages {
			if stderr != nil {
				fmt.Fprintln(stderr, message)
			}
		}
		env = config.AppendEnvOverrides(env, envValues)
	}
	operation := dynamicArgvOperation(preparedArgv)
	config.LogDockerComposeCommand(&effectiveContext, operation)

	var err error
	if effectiveContext.DockerHostType == config.ContextRemote {
		err = runComposeProjectRemoteArgvContext(runCtx, &effectiveContext, projectDir, stdin, stdout, stderr, preparedArgv)
	} else {
		err = runComposeProjectLocalArgvContext(runCtx, projectDir, stdin, stdout, stderr, env, preparedArgv)
	}
	if err != nil {
		return fmt.Errorf("run compose project operation %q: %w", operation, err)
	}
	return nil
}

func validateComposeProjectArgv(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("compose project argv cannot be empty")
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("compose project argv cannot contain NUL")
		}
	}
	return nil
}

func dynamicArgvOperation(argv []string) string {
	if len(argv) >= 2 && argv[0] == "docker" && argv[1] == "compose" {
		for _, argument := range argv[2:] {
			switch argument {
			case "build", "config", "cp", "create", "down", "events", "exec", "images", "kill", "logs", "ls", "pause", "port", "ps", "pull", "push", "restart", "rm", "run", "start", "stop", "top", "unpause", "up", "version", "wait", "watch":
				return "docker compose " + argument
			}
		}
		return "docker compose"
	}
	if len(argv) == 0 {
		return ""
	}
	return filepath.Base(argv[0])
}

// RunComposeProjectArgv executes one dynamic argv command in a Compose project.
func (s *SDK) RunComposeProjectArgv(ctx *config.Context, projectDir string, stdin io.Reader, stdout, stderr io.Writer, argv []string) error {
	return s.RunComposeProjectArgvContext(context.Background(), ctx, projectDir, stdin, stdout, stderr, argv)
}

func runLocalComposeProjectArgvContext(runCtx context.Context, projectDir string, stdin io.Reader, stdout, stderr io.Writer, env, argv []string) error {
	command := exec.CommandContext(runCtx, argv[0], argv[1:]...) // #nosec G204 -- argv is already split and is passed directly without shell evaluation.
	command.Dir = projectDir
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = env
	return command.Run()
}

func runRemoteComposeProjectArgvContext(runCtx context.Context, ctx *config.Context, projectDir string, stdin io.Reader, stdout, stderr io.Writer, argv []string) error {
	remoteCommand := shellJoin(argv)
	if strings.TrimSpace(projectDir) != "" {
		remoteCommand = fmt.Sprintf("cd %s && %s", shellQuote(projectDir), remoteCommand)
	}
	_, err := runRemoteShellCommandInputContext(runCtx, ctx, stdin, stdout, stderr, remoteCommand)
	return err
}

func runComposeProjectHostUIDArtifact(runCtx context.Context, ctx *config.Context, projectDir, command string) (bool, error) {
	artifactPath, handled, err := composeProjectHostUIDArtifactPath(projectDir, command)
	if err != nil || !handled {
		return handled, err
	}
	uid, _, err := composeProjectHostIdentity(runCtx, ctx)
	if err != nil {
		return true, err
	}
	if err := ctx.WriteProjectFile(projectDir, artifactPath, []byte(uid+"\n")); err != nil {
		return true, fmt.Errorf("write host uid artifact %s: %w", filepath.Base(artifactPath), err)
	}
	return true, nil
}

func composeProjectHostUIDArtifactPath(projectDir, command string) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) != 4 || fields[0] != "id" || fields[1] != "-u" || fields[2] != ">" {
		return "", false, nil
	}
	artifact := filepath.Clean(fields[3])
	if artifact == "." || artifact == ".." || filepath.IsAbs(artifact) || strings.HasPrefix(artifact, ".."+string(filepath.Separator)) {
		return "", true, fmt.Errorf("host uid artifact path must stay inside the compose project: %q", fields[3])
	}
	if strings.TrimSpace(projectDir) == "" {
		return "", true, fmt.Errorf("compose project directory cannot be empty for host uid artifact %q", fields[3])
	}
	return filepath.Join(projectDir, artifact), true, nil
}

func expandComposeProjectHostIdentity(runCtx context.Context, ctx *config.Context, command string) (string, error) {
	if !strings.Contains(command, "$(id -u)") && !strings.Contains(command, "$(id -g)") {
		return command, nil
	}
	uid, gid, err := resolveComposeProjectHostIdentity(runCtx, ctx)
	if err != nil {
		return "", err
	}
	return corelifecycle.ExpandHostIdentity(command, uid, gid)
}

func composeProjectHostIdentity(runCtx context.Context, ctx *config.Context) (string, string, error) {
	if ctx.DockerHostType == config.ContextRemote {
		uid, err := ctx.RunQuietCommandContext(runCtx, exec.Command("id", "-u"))
		if err != nil {
			return "", "", fmt.Errorf("resolve remote host uid: %w", err)
		}
		gid, err := ctx.RunQuietCommandContext(runCtx, exec.Command("id", "-g"))
		if err != nil {
			return "", "", fmt.Errorf("resolve remote host gid: %w", err)
		}
		return validateComposeProjectHostID(uid, gid)
	}
	uid, gid, available, err := resolveLocalComposeProjectHostNumericIdentity()
	if err != nil {
		return "", "", err
	}
	if !available {
		return "0", "0", nil
	}
	return validateComposeProjectHostID(uid, gid)
}

func validateComposeProjectHostID(uid, gid string) (string, string, error) {
	uid = strings.TrimSpace(uid)
	gid = strings.TrimSpace(gid)
	if _, err := strconv.ParseUint(uid, 10, 32); err != nil {
		return "", "", fmt.Errorf("invalid host uid %q", uid)
	}
	if _, err := strconv.ParseUint(gid, 10, 32); err != nil {
		return "", "", fmt.Errorf("invalid host gid %q", gid)
	}
	return uid, gid, nil
}

func runRemoteShellCommandContext(runCtx context.Context, ctx *config.Context, stdout, stderr io.Writer, command string) (string, error) {
	return runRemoteShellCommandInputContext(runCtx, ctx, nil, stdout, stderr, command)
}

func runRemoteShellCommandInputContext(runCtx context.Context, ctx *config.Context, stdin io.Reader, stdout, stderr io.Writer, command string) (string, error) {
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
	if stdin != nil {
		session.Stdin = stdin
	}

	if err := session.Run(command); err != nil {
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
			return s.RunActiveComposeProjectArgv(cmd, []string{"docker", "compose", "ps"})
		},
	})
	s.AddCommand(&cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: fmt.Sprintf("Show recent Docker Compose logs for the active %s stack", displayName),
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			argv := []string{"docker", "compose", "logs", fmt.Sprintf("--tail=%d", tail)}
			argv = append(argv, args...)
			return s.RunActiveComposeProjectArgv(cmd, argv)
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

// RunActiveComposeProjectCommandList runs constrained lifecycle commands in
// the active context's compose project directory.
func (s *SDK) RunActiveComposeProjectCommandList(cmd *cobra.Command, commands []string) (returnErr error) {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	return s.RunComposeProjectCommandList(cmd, ctx, commands)
}

// DefaultComposeRolloutCommands returns the generic Compose rollout sequence.
func DefaultComposeRolloutCommands() []string {
	return []string{
		"docker compose pull --ignore-buildable --quiet || docker compose pull --ignore-buildable",
		"docker compose build --pull",
		"docker compose up --remove-orphans --wait --pull missing --quiet-pull -d",
	}
}

// RunActiveComposeProjectRollout syncs the active project from the checkout's
// upstream branch before running rollout commands.
func (s *SDK) RunActiveComposeProjectRollout(cmd *cobra.Command, commands []string) (returnErr error) {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	if _, err := validateComposeProjectCommandList(cmd.Context(), ctx, ctx.ProjectDir, commands); err != nil {
		return err
	}
	if err := syncComposeProjectCheckout(cmd.Context(), ctx, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("sync Git checkout before Compose rollout: %w", err)
	}
	return s.RunComposeProjectCommandList(cmd, ctx, commands)
}

// RunActiveComposeProjectArgv executes one dynamic argv command in the active
// Compose project without reparsing user-controlled arguments.
func (s *SDK) RunActiveComposeProjectArgv(cmd *cobra.Command, argv []string) (returnErr error) {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	return s.RunComposeProjectArgvContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), argv)
}

// RunActiveComposeProjectArgvList validates and executes an ordered dynamic
// argv sequence while holding one project mutation lock across every command.
// Use it when later commands depend on files or container state created by an
// earlier command and interleaving another operator workflow would be unsafe.
func (s *SDK) RunActiveComposeProjectArgvList(cmd *cobra.Command, commands [][]string) (returnErr error) {
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	for index, argv := range commands {
		if err := validateComposeProjectArgv(argv); err != nil {
			return fmt.Errorf("validate compose project argv %d: %w", index+1, err)
		}
	}
	if len(commands) == 0 {
		return nil
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	for _, argv := range commands {
		if err := s.RunComposeProjectArgvContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), argv); err != nil {
			return err
		}
	}
	return nil
}

// RunActiveComposeProjectHostArgv resolves the active local or remote host's
// UID, GID, and Docker-visible project directory before building and executing
// dynamic argv. This replaces shell placeholders such as $(id -u) and $PWD.
func (s *SDK) RunActiveComposeProjectHostArgv(cmd *cobra.Command, build func(ComposeProjectHost) []string) (returnErr error) {
	if build == nil {
		return fmt.Errorf("compose project argv builder cannot be nil")
	}
	ctx, err := s.ContextFromCommand(cmd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("active context does not define a project directory")
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	host, err := resolveComposeProjectHost(cmd.Context(), ctx)
	if err != nil {
		return err
	}
	argv := build(host)
	return s.RunComposeProjectArgvContext(cmd.Context(), ctx, ctx.ProjectDir, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), argv)
}

func resolveComposeProjectHost(runCtx context.Context, ctx *config.Context) (ComposeProjectHost, error) {
	if ctx == nil {
		return ComposeProjectHost{}, fmt.Errorf("context is nil")
	}
	host := ComposeProjectHost{ProjectDir: composeProjectHostDirectory(ctx)}
	if ctx.DockerHostType == config.ContextLocal {
		uid, gid, available, err := resolveLocalComposeProjectHostNumericIdentity()
		if err != nil {
			return ComposeProjectHost{}, err
		}
		host.UID = uid
		host.GID = gid
		host.HasNumericIdentity = available
		return host, nil
	}
	uid, gid, err := resolveComposeProjectHostIdentity(runCtx, ctx)
	if err != nil {
		return ComposeProjectHost{}, err
	}
	host.UID = uid
	host.GID = gid
	host.HasNumericIdentity = true
	return host, nil
}

func composeProjectHostDirectory(ctx *config.Context) string {
	if ctx == nil || ctx.DockerHostType != config.ContextLocal {
		if ctx == nil {
			return ""
		}
		return ctx.ProjectDir
	}
	if projectDir := composeProjectDockerVisibleLocalPath(ctx.ProjectDir); strings.TrimSpace(projectDir) != "" {
		return projectDir
	}
	return ctx.ProjectDir
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

// DockerComposeExecArgv builds typed argv for a non-TTY Compose exec command.
func DockerComposeExecArgv(service string, args ...string) []string {
	invocation := make([]string, 0, len(args)+5)
	invocation = append(invocation, "docker", "compose", "exec", "-T", service)
	invocation = append(invocation, args...)
	return invocation
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
