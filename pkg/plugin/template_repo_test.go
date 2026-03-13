package plugin

import (
	"reflect"
	"testing"
)

func TestCloneTemplateRepoWithoutUserRemote(t *testing.T) {
	t.Parallel()

	oldRunner := runGitCommand
	t.Cleanup(func() {
		runGitCommand = oldRunner
	})

	var calls [][]string
	runGitCommand = func(name string, args ...string) error {
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
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}

func TestCloneTemplateRepoConfiguresUserRemote(t *testing.T) {
	t.Parallel()

	oldRunner := runGitCommand
	t.Cleanup(func() {
		runGitCommand = oldRunner
	})

	var calls [][]string
	runGitCommand = func(name string, args ...string) error {
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
		{"git", "-C", "/tmp/site", "remote", "rename", "origin", "upstream"},
		{"git", "-C", "/tmp/site", "remote", "add", "origin", "git@github.com:example/site.git"},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}

func TestCloneTemplateRepoRejectsMatchingRemoteNames(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	oldRunner := runGitCommand
	t.Cleanup(func() {
		runGitCommand = oldRunner
	})

	var calls [][]string
	runGitCommand = func(name string, args ...string) error {
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
		{"git", "-C", "/tmp/site", "remote", "rename", "origin", "upstream"},
		{"git", "-C", "/tmp/site", "remote", "add", "origin", "git@github.com:example/site.git"},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("unexpected git calls: %#v", calls)
	}
}
