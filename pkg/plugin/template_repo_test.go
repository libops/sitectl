package plugin

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const testTemplateCommit = "0123456789abcdef0123456789abcdef01234567"

func TestCloneTemplateRepoWithoutUserRemote(t *testing.T) {
	oldRunner := runGitCommand
	oldHasRepo := hasGitRepositoryFunc
	oldHasRemote := gitRemoteExistsFunc
	t.Cleanup(func() {
		runGitCommand = oldRunner
		hasGitRepositoryFunc = oldHasRepo
		gitRemoteExistsFunc = oldHasRemote
	})
	hasGitRepositoryFunc = func(projectDir string) (bool, error) { return false, nil }
	gitRemoteExistsFunc = func(projectDir, remoteName string) (bool, error) { return false, nil }

	var calls [][]string
	runGitCommand = func(stdout, stderr io.Writer, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "rev-parse" {
			_, _ = io.WriteString(stdout, testTemplateCommit+"\n")
		}
		return nil
	}
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}

	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:   "https://github.com/islandora-devops/isle-site-template",
		TemplateBranch: "main",
		ProjectDir:     projectDir,
	})
	if err != nil {
		t.Fatalf("CloneTemplateRepo() error = %v", err)
	}

	expected := [][]string{
		{"git", "clone", "--branch", "main", "--", "https://github.com/islandora-devops/isle-site-template", projectDir},
		{"git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}"},
		{"git", "-C", projectDir, "init", "-b", "main"},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}

func TestCloneTemplateRepoConfiguresUserRemote(t *testing.T) {
	oldRunner := runGitCommand
	oldHasRepo := hasGitRepositoryFunc
	oldHasRemote := gitRemoteExistsFunc
	t.Cleanup(func() {
		runGitCommand = oldRunner
		hasGitRepositoryFunc = oldHasRepo
		gitRemoteExistsFunc = oldHasRemote
	})
	hasGitRepositoryFunc = func(projectDir string) (bool, error) { return true, nil }
	gitRemoteExistsFunc = func(projectDir, remoteName string) (bool, error) { return false, nil }

	var calls [][]string
	runGitCommand = func(stdout, stderr io.Writer, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "rev-parse" {
			_, _ = io.WriteString(stdout, testTemplateCommit+"\n")
		}
		return nil
	}
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}

	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:       "https://github.com/islandora-devops/isle-site-template",
		TemplateBranch:     "main",
		ProjectDir:         projectDir,
		GitRemoteURL:       "git@github.com:example/site.git",
		GitRemoteName:      "origin",
		TemplateRemoteName: "upstream",
	})
	if err != nil {
		t.Fatalf("CloneTemplateRepo() error = %v", err)
	}

	expected := [][]string{
		{"git", "clone", "--branch", "main", "--", "https://github.com/islandora-devops/isle-site-template", projectDir},
		{"git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}"},
		{"git", "-C", projectDir, "init", "-b", "main"},
		{"git", "-C", projectDir, "remote", "add", "origin", "git@github.com:example/site.git"},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}

func TestCloneTemplateRepoRejectsMatchingRemoteNames(t *testing.T) {
	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:       "https://github.com/islandora-devops/isle-site-template",
		ProjectDir:         "/tmp/site",
		GitRemoteURL:       "git@github.com:example/site.git",
		GitRemoteName:      "origin",
		TemplateRemoteName: "origin",
	})
	if err == nil {
		t.Fatal("expected remote name validation error")
	}
}

func TestConfigureTemplateRemotes(t *testing.T) {
	oldRunner := runGitCommand
	oldHasRepo := hasGitRepositoryFunc
	oldHasRemote := gitRemoteExistsFunc
	t.Cleanup(func() {
		runGitCommand = oldRunner
		hasGitRepositoryFunc = oldHasRepo
		gitRemoteExistsFunc = oldHasRemote
	})
	hasGitRepositoryFunc = func(projectDir string) (bool, error) { return false, nil }
	gitRemoteExistsFunc = func(projectDir, remoteName string) (bool, error) { return false, nil }

	var calls [][]string
	runGitCommand = func(stdout, stderr io.Writer, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	err := ConfigureTemplateRemotes(GitTemplateOptions{
		ProjectDir:         "/tmp/site",
		GitRemoteURL:       "git@github.com:example/site.git",
		GitRemoteName:      "origin",
		TemplateRemoteName: "upstream",
	})
	if err != nil {
		t.Fatalf("ConfigureTemplateRemotes() error = %v", err)
	}

	expected := [][]string{
		{"git", "-C", "/tmp/site", "init"},
		{"git", "-C", "/tmp/site", "remote", "add", "origin", "git@github.com:example/site.git"},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}
