package plugin

import (
	"fmt"
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
}

type gitRunner func(name string, args ...string) error

var runGitCommand gitRunner = defaultRunGitCommand

func (s *SDK) CloneTemplateRepo(opts GitTemplateOptions) error {
	return CloneTemplateRepo(opts)
}

func (s *SDK) ConfigureTemplateRemotes(opts GitTemplateOptions) error {
	return ConfigureTemplateRemotes(opts)
}

func CloneTemplateRepo(opts GitTemplateOptions) error {
	if opts.TemplateRepo == "" {
		return fmt.Errorf("template repo cannot be empty")
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

	args := []string{"clone"}
	if opts.TemplateBranch != "" {
		args = append(args, "--branch", opts.TemplateBranch)
	}
	args = append(args, opts.TemplateRepo, opts.ProjectDir)
	if err := runGitCommand("git", args...); err != nil {
		return fmt.Errorf("clone template repo %q: %w", opts.TemplateRepo, err)
	}

	if opts.GitRemoteURL == "" {
		return nil
	}

	return ConfigureTemplateRemotes(opts)
}

func ConfigureTemplateRemotes(opts GitTemplateOptions) error {
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

	if err := runGitCommand("git", "-C", opts.ProjectDir, "remote", "rename", "origin", opts.TemplateRemoteName); err != nil {
		return fmt.Errorf("rename template remote to %q: %w", opts.TemplateRemoteName, err)
	}
	if err := runGitCommand("git", "-C", opts.ProjectDir, "remote", "add", opts.GitRemoteName, opts.GitRemoteURL); err != nil {
		return fmt.Errorf("add git remote %q: %w", opts.GitRemoteName, err)
	}

	return nil
}

func defaultRunGitCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workingDirFromArgs(args)
	return cmd.Run()
}

func workingDirFromArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-C" {
			return filepath.Clean(args[i+1])
		}
	}
	return ""
}
