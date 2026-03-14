package plugin

import (
	"io"
	"reflect"
	"testing"
)

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
		return nil
	}

	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:   "https://github.com/islandora-devops/isle-site-template",
		TemplateBranch: "main",
		ProjectDir:     "/tmp/site",
	})
	if err != nil {
		t.Fatalf("CloneTemplateRepo() error = %v", err)
	}

	expected := [][]string{
		{"git", "clone", "--branch", "main", "https://github.com/islandora-devops/isle-site-template", "/tmp/site"},
		{"git", "-C", "/tmp/site", "init", "-b", "main"},
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
		return nil
	}

	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:       "https://github.com/islandora-devops/isle-site-template",
		TemplateBranch:     "main",
		ProjectDir:         "/tmp/site",
		GitRemoteURL:       "git@github.com:example/site.git",
		GitRemoteName:      "origin",
		TemplateRemoteName: "upstream",
	})
	if err != nil {
		t.Fatalf("CloneTemplateRepo() error = %v", err)
	}

	expected := [][]string{
		{"git", "clone", "--branch", "main", "https://github.com/islandora-devops/isle-site-template", "/tmp/site"},
		{"git", "-C", "/tmp/site", "init", "-b", "main"},
		{"git", "-C", "/tmp/site", "remote", "add", "origin", "git@github.com:example/site.git"},
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
