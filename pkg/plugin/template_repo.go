package plugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type GitTemplateOptions struct {
	TemplateRepo       string
	TemplateBranch     string
	ProjectDir         string
	GitRemoteURL       string
	GitRemoteName      string
	TemplateRemoteName string
	Quiet              bool
}

type gitRunner func(stdout, stderr io.Writer, name string, args ...string) error
type gitContextRunner func(context.Context, io.Writer, io.Writer, string, ...string) error

var runGitCommand gitRunner = defaultRunGitCommand
var runGitCommandContext gitContextRunner = defaultRunGitCommandContext
var hasGitRepositoryFunc = hasGitRepository
var gitRemoteExistsFunc = gitRemoteExists

func (s *SDK) CloneTemplateRepo(opts GitTemplateOptions) error {
	if s == nil {
		return fmt.Errorf("plugin sdk is not initialized")
	}
	sitectl, plugins := s.templateLockPackages()
	return cloneTemplateRepoWithRunner(context.Background(), opts, sitectl, plugins, runGitCommand)
}

// CloneTemplateRepoContext clones and finalizes a template repository while
// honoring cancellation for every Git subprocess.
func (s *SDK) CloneTemplateRepoContext(runCtx context.Context, opts GitTemplateOptions) error {
	if s == nil {
		return fmt.Errorf("plugin sdk is not initialized")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	sitectl, plugins := s.templateLockPackages()
	runner := func(stdout, stderr io.Writer, name string, args ...string) error {
		return runGitCommandContext(runCtx, stdout, stderr, name, args...)
	}
	return cloneTemplateRepoWithRunner(runCtx, opts, sitectl, plugins, runner)
}

func (s *SDK) ConfigureTemplateRemotes(opts GitTemplateOptions) error {
	return ConfigureTemplateRemotes(opts)
}

func CloneTemplateRepo(opts GitTemplateOptions) error {
	return cloneTemplateRepoWithRunner(context.Background(), opts, nil, nil, runGitCommand)
}

func cloneTemplateRepoWithRunner(runCtx context.Context, opts GitTemplateOptions, sitectl *templateLockPackage, plugins []templateLockPackage, runner gitRunner) error {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	templateRepo, err := validateTemplateRepository(opts.TemplateRepo)
	if err != nil {
		return err
	}
	opts.TemplateRepo = templateRepo
	if opts.ProjectDir == "" {
		return fmt.Errorf("project directory cannot be empty")
	}
	if opts.GitRemoteName == "" {
		opts.GitRemoteName = "origin"
	}
	if opts.TemplateRemoteName == "" {
		opts.TemplateRemoteName = "upstream"
	}
	if opts.GitRemoteName == opts.TemplateRemoteName {
		return fmt.Errorf("git remote name %q cannot match template remote name %q", opts.GitRemoteName, opts.TemplateRemoteName)
	}

	args := []string{"clone"}
	if opts.TemplateBranch != "" {
		args = append(args, "--branch", opts.TemplateBranch)
	}
	args = append(args, "--", opts.TemplateRepo, opts.ProjectDir)
	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if opts.Quiet {
		stdout = io.Discard
		stderr = io.Discard
	}
	if err := runner(stdout, stderr, "git", args...); err != nil {
		return fmt.Errorf("clone template repo %q: %w", opts.TemplateRepo, err)
	}
	metadata, err := inspectLocalTemplateCheckoutWithRunner(opts.ProjectDir, runner)
	if err != nil {
		return err
	}
	lock, err := buildTemplateLock(opts.TemplateRepo, metadata, sitectl, plugins)
	if err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}

	if err := os.RemoveAll(filepath.Join(opts.ProjectDir, ".git")); err != nil {
		return fmt.Errorf("remove template git history: %w", err)
	}
	if err := initializeTemplateRepoWithRunner(opts, stdout, stderr, runner); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := writeTemplateLockAtomic(opts.ProjectDir, lock); err != nil {
		return err
	}
	if opts.GitRemoteURL == "" {
		return nil
	}
	return configureTemplateRemotesWithRunner(opts, runner)
}

func ConfigureTemplateRemotes(opts GitTemplateOptions) error {
	return configureTemplateRemotesWithRunner(opts, runGitCommand)
}

func configureTemplateRemotesWithRunner(opts GitTemplateOptions, runner gitRunner) error {
	if opts.GitRemoteURL == "" {
		return nil
	}
	if opts.ProjectDir == "" {
		return fmt.Errorf("project directory cannot be empty")
	}
	if opts.GitRemoteName == "" {
		opts.GitRemoteName = "origin"
	}
	if opts.TemplateRemoteName == "" {
		opts.TemplateRemoteName = "upstream"
	}
	if opts.GitRemoteName == opts.TemplateRemoteName {
		return fmt.Errorf("git remote name %q cannot match template remote name %q", opts.GitRemoteName, opts.TemplateRemoteName)
	}

	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if opts.Quiet {
		stdout = io.Discard
		stderr = io.Discard
	}
	hasGitDir, err := hasGitRepositoryFunc(opts.ProjectDir)
	if err != nil {
		return err
	}
	if !hasGitDir {
		if err := initializeTemplateRepo(opts, stdout, stderr); err != nil {
			return err
		}
	}

	hasOrigin, err := gitRemoteExistsFunc(opts.ProjectDir, "origin")
	if err != nil {
		return err
	}
	if hasOrigin && opts.TemplateRemoteName != "" && opts.TemplateRemoteName != "origin" {
		if err := runner(stdout, stderr, "git", "-C", opts.ProjectDir, "remote", "rename", "origin", opts.TemplateRemoteName); err != nil {
			return fmt.Errorf("rename template remote to %q: %w", opts.TemplateRemoteName, err)
		}
	}
	if err := runner(stdout, stderr, "git", "-C", opts.ProjectDir, "remote", "add", opts.GitRemoteName, opts.GitRemoteURL); err != nil {
		return fmt.Errorf("add git remote %q: %w", opts.GitRemoteName, err)
	}

	return nil
}

func initializeTemplateRepo(opts GitTemplateOptions, stdout, stderr io.Writer) error {
	return initializeTemplateRepoWithRunner(opts, stdout, stderr, runGitCommand)
}

func initializeTemplateRepoWithRunner(opts GitTemplateOptions, stdout, stderr io.Writer, runner gitRunner) error {
	args := []string{"-C", opts.ProjectDir, "init"}
	if opts.TemplateBranch != "" {
		args = append(args, "-b", opts.TemplateBranch)
	}
	if err := runner(stdout, stderr, "git", args...); err != nil {
		return fmt.Errorf("initialize fresh git repository in %q: %w", opts.ProjectDir, err)
	}
	return nil
}

func defaultRunGitCommand(stdout, stderr io.Writer, name string, args ...string) error {
	return runGitCommandWithIO(stdout, stderr, name, args...)
}

func defaultRunGitCommandContext(runCtx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	return runGitCommandWithIOContext(runCtx, stdout, stderr, name, args...)
}

func runGitCommandWithIO(stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...) // #nosec G204 -- helper is used for fixed git invocations assembled by sitectl.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runGitCommandWithIOContext(runCtx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	if runCtx == nil {
		runCtx = context.Background()
	}
	cmd := exec.CommandContext(runCtx, name, args...) // #nosec G204 -- helper is used for fixed git invocations assembled by sitectl.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func hasGitRepository(projectDir string) (bool, error) {
	info, err := os.Stat(filepath.Join(projectDir, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat git directory in %q: %w", projectDir, err)
	}
	return info.IsDir(), nil
}

func gitRemoteExists(projectDir, remoteName string) (bool, error) {
	cmd := exec.Command("git", "-C", projectDir, "remote", "get-url", remoteName) // #nosec G204 -- projectDir and remoteName are passed as git arguments without a shell.
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			return false, nil
		}
		return false, fmt.Errorf("check git remote %q in %q: %w", remoteName, projectDir, err)
	}
	return true, nil
}
