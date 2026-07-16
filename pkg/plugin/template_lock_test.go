package plugin

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func runTemplateGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- test-owned repository and arguments.
	cmd.Dir = directory
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=sitectl test",
		"GIT_AUTHOR_EMAIL=sitectl@example.invalid",
		"GIT_COMMITTER_NAME=sitectl test",
		"GIT_COMMITTER_EMAIL=sitectl@example.invalid",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func createTemplateFixture(t *testing.T, contract string, contractSymlink bool) (string, string) {
	t.Helper()
	repository := t.TempDir()
	runTemplateGit(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if contract != "" || contractSymlink {
		libops := filepath.Join(repository, ".libops")
		if err := os.Mkdir(libops, 0o750); err != nil {
			t.Fatal(err)
		}
		contractPath := filepath.Join(libops, "template-contract.yaml")
		if contractSymlink {
			outside := filepath.Join(repository, "contract-target.yaml")
			if err := os.WriteFile(outside, []byte(contract), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../contract-target.yaml", contractPath); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(contractPath, []byte(contract), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runTemplateGit(t, repository, "add", ".")
	runTemplateGit(t, repository, "commit", "-m", "template fixture")
	return repository, runTemplateGit(t, repository, "rev-parse", "HEAD")
}

func TestSDKCloneTemplateRepoRetainsExactProvenanceLock(t *testing.T) {
	contract := `apiVersion: sitectl.libops.io/v1alpha1
kind: TemplateContract
schema: 1
spec:
  componentDefaults:
    revision: components-v3
`
	repository, commit := createTemplateFixture(t, contract, false)
	destination := filepath.Join(t.TempDir(), "site")
	t.Setenv(hostVersionEnvironment, "0.39.0")
	t.Setenv(hostRevisionEnvironment, "ABCDEF1234567")
	sdk := NewSDK(Metadata{
		Name:    "omeka-s",
		Version: "1.2.3 (Built on 2026-07-15 from Git SHA FEDCBA7654321)",
	})

	if err := sdk.CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:   repository,
		TemplateBranch: "main",
		ProjectDir:     destination,
		Quiet:          true,
	}); err != nil {
		t.Fatalf("CloneTemplateRepo() error = %v", err)
	}

	if got := runTemplateGit(t, destination, "rev-parse", "--is-inside-work-tree"); got != "true" {
		t.Fatalf("fresh downstream checkout is not a Git repository: %q", got)
	}
	logCmd := exec.Command("git", "-C", destination, "log", "-1")
	if err := logCmd.Run(); err == nil {
		t.Fatal("fresh downstream repository retained template Git history")
	}

	lockPath := filepath.Join(destination, filepath.FromSlash(templateLockPath))
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock templateLock
	if err := yaml.Unmarshal(lockData, &lock); err != nil {
		t.Fatalf("parse retained lock: %v", err)
	}
	if lock.APIVersion != templateLockAPIVersion || lock.Kind != templateLockKind || lock.Schema != templateLockSchema {
		t.Fatalf("unexpected lock envelope: %+v", lock)
	}
	if lock.Template.Repository != repository || lock.Template.Commit != commit {
		t.Fatalf("unexpected template source: %+v", lock.Template)
	}
	digest := sha256.Sum256([]byte(contract))
	if lock.Template.Contract == nil || lock.Template.Contract.Path != templateContractPath || lock.Template.Contract.Digest != fmt.Sprintf("sha256:%x", digest) {
		t.Fatalf("unexpected template contract lock: %+v", lock.Template.Contract)
	}
	if lock.Sitectl == nil || lock.Sitectl.Version != "0.39.0" || lock.Sitectl.Revision != "abcdef1234567" {
		t.Fatalf("unexpected sitectl build identity: %+v", lock.Sitectl)
	}
	wantPlugins := []templateLockPackage{{Package: "sitectl-omeka-s", Version: "1.2.3", Revision: "fedcba7654321"}}
	if !reflect.DeepEqual(lock.Plugins, wantPlugins) {
		t.Fatalf("plugins = %+v, want %+v", lock.Plugins, wantPlugins)
	}
	if lock.ComponentDefaults == nil || lock.ComponentDefaults.Revision != "components-v3" {
		t.Fatalf("unexpected component defaults provenance: %+v", lock.ComponentDefaults)
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("template lock mode = %o, want 644", info.Mode().Perm())
	}
	if strings.Contains(string(lockData), "password") || strings.Contains(string(lockData), "token") {
		t.Fatalf("template lock unexpectedly contains credential material:\n%s", lockData)
	}
}

func TestCloneTemplateRepoFailsBeforeRemovingHistoryForUnsafeContract(t *testing.T) {
	validContract := "apiVersion: sitectl.libops.io/v1alpha1\nkind: TemplateContract\nschema: 1\n"
	tests := []struct {
		name     string
		contract string
		symlink  bool
	}{
		{name: "malformed", contract: "apiVersion: [unterminated\n"},
		{name: "wrong-envelope", contract: "apiVersion: sitectl.libops.io/v1alpha1\nkind: Other\nschema: 1\n"},
		{name: "symlink", contract: validContract, symlink: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, commit := createTemplateFixture(t, test.contract, test.symlink)
			destination := filepath.Join(t.TempDir(), "site")
			err := CloneTemplateRepo(GitTemplateOptions{TemplateRepo: repository, TemplateBranch: "main", ProjectDir: destination, Quiet: true})
			if err == nil {
				t.Fatal("unsafe template contract was accepted")
			}
			if got := runTemplateGit(t, destination, "rev-parse", "HEAD"); got != commit {
				t.Fatalf("template history changed before validation failed: got %s want %s", got, commit)
			}
			if _, statErr := os.Stat(filepath.Join(destination, filepath.FromSlash(templateLockPath))); !os.IsNotExist(statErr) {
				t.Fatalf("lock exists after validation failure: %v", statErr)
			}
		})
	}
}

func TestCloneTemplateRepoRejectsSourceOwnedLockBeforeRemovingHistory(t *testing.T) {
	repository, _ := createTemplateFixture(t, "", false)
	libops := filepath.Join(repository, ".libops")
	if err := os.Mkdir(libops, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libops, "template.lock.yaml"), []byte("kind: TemplateLock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTemplateGit(t, repository, "add", ".libops/template.lock.yaml")
	runTemplateGit(t, repository, "commit", "-m", "add invalid source lock")
	commit := runTemplateGit(t, repository, "rev-parse", "HEAD")
	destination := filepath.Join(t.TempDir(), "site")

	err := CloneTemplateRepo(GitTemplateOptions{
		TemplateRepo:   repository,
		TemplateBranch: "main",
		ProjectDir:     destination,
		Quiet:          true,
	})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("source-owned lock error = %v", err)
	}
	if got := runTemplateGit(t, destination, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("template history changed before source lock rejection: got %s want %s", got, commit)
	}
}

func TestCloneTemplateRepoRejectsCredentialBearingRepository(t *testing.T) {
	for _, repository := range []string{
		"https://user:secret@example.invalid/libops/template.git",
		"https://token@example.invalid/libops/template.git",
		"user:secret@example.invalid:libops/template.git",
		"https://example.invalid/libops/template.git?token=secret",
	} {
		t.Run(repository, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "site")
			err := CloneTemplateRepo(GitTemplateOptions{TemplateRepo: repository, ProjectDir: destination, Quiet: true})
			if err == nil || !strings.Contains(err.Error(), "credential") && !strings.Contains(err.Error(), "query") {
				t.Fatalf("credential-bearing repository error = %v", err)
			}
			if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
				t.Fatalf("clone started before repository validation: %v", statErr)
			}
		})
	}
}

func TestBuildTemplateLockIsDeterministicAndOmitsUnknownBuildValues(t *testing.T) {
	metadata := templateCheckoutMetadata{Commit: testTemplateCommit}
	plugins := []templateLockPackage{
		{Package: "sitectl-z", Version: "dev", Revision: "none"},
		{Package: "sitectl-a", Version: "1.0.0", Revision: "abcdef1"},
	}
	first, err := buildTemplateLock("git@github.com:libops/template.git", metadata, nil, plugins)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildTemplateLock("git@github.com:libops/template.git", metadata, nil, plugins)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("lock serialization changed between identical inputs:\n%s\n%s", first, second)
	}
	if strings.Index(string(first), "sitectl-a") > strings.Index(string(first), "sitectl-z") {
		t.Fatalf("plugin packages are not sorted:\n%s", first)
	}
	if strings.Contains(string(first), "version: dev") || strings.Contains(string(first), "revision: none") {
		t.Fatalf("unknown build values were retained:\n%s", first)
	}
	if got := parseFormattedBuildIdentity("dev (Built on unknown from Git SHA none)"); got != (buildIdentity{}) {
		t.Fatalf("unknown build identity was retained: %+v", got)
	}
}

func TestHostBuildEnvironmentCannotBeOverriddenByInheritedValues(t *testing.T) {
	SetHostBuildInfo("1.2.3", "ABCDEF1")
	t.Cleanup(func() { SetHostBuildInfo("", "") })
	filtered := filterHostBuildEnvironment([]string{
		"PATH=/test/bin",
		hostVersionEnvironment + "=9.9.9",
		hostRevisionEnvironment + "=deadbee",
	})
	want := []string{"PATH=/test/bin", hostVersionEnvironment + "=1.2.3", hostRevisionEnvironment + "=abcdef1"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filterHostBuildEnvironment() = %v, want %v", filtered, want)
	}
}
