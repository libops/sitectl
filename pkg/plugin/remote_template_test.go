package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

type localRemoteTemplateConnection struct {
	commands         [][]string
	run              func([]string) (string, error)
	runContext       func(context.Context, []string) (string, error)
	beforeGitRemoval func(string)
	mkdir            func(string) error
	rename           func(string, string) error
	renameErr        error
	closed           bool
}

func (c *localRemoteTemplateConnection) Close() error {
	c.closed = true
	return nil
}

func (c *localRemoteTemplateConnection) Run(runCtx context.Context, _, _ io.Writer, args ...string) (string, error) {
	command := append([]string(nil), args...)
	c.commands = append(c.commands, command)
	if c.beforeGitRemoval != nil && len(command) == 4 && command[0] == "rm" && command[1] == "-rf" && command[2] == "--" && filepath.Base(command[3]) == ".git" {
		c.beforeGitRemoval(command[3])
	}
	if c.runContext != nil {
		return c.runContext(runCtx, command)
	}
	if c.run == nil {
		return "", nil
	}
	return c.run(command)
}

func (c *localRemoteTemplateConnection) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (c *localRemoteTemplateConnection) ReadLink(name string) (string, error) {
	return os.Readlink(name)
}

func (c *localRemoteTemplateConnection) ReadDirLimit(_ context.Context, name string, maximum int) ([]os.FileInfo, bool, error) {
	return readLocalComposeCreateDirectory(name, maximum)
}

func (c *localRemoteTemplateConnection) DirectoryHasEntries(_ context.Context, name string) (bool, error) {
	return localComposeCreateDirectoryHasEntries(name)
}

func (c *localRemoteTemplateConnection) Open(name string) (remoteTemplateFile, error) {
	return os.Open(name) // #nosec G304 -- test paths are created under t.TempDir.
}

func (c *localRemoteTemplateConnection) OpenFile(name string, flag int) (remoteTemplateFile, error) {
	return os.OpenFile(name, flag, 0o600) // #nosec G304 -- test paths are created under t.TempDir.
}

func (c *localRemoteTemplateConnection) MkdirAll(directory string) error {
	return os.MkdirAll(directory, 0o750)
}

func (c *localRemoteTemplateConnection) Mkdir(directory string) error {
	if c.mkdir != nil {
		return c.mkdir(directory)
	}
	return os.Mkdir(directory, 0o750)
}

func (c *localRemoteTemplateConnection) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func (c *localRemoteTemplateConnection) Remove(name string) error {
	return os.Remove(name)
}

func (c *localRemoteTemplateConnection) Rename(oldName, newName string) error {
	if c.rename != nil {
		return c.rename(oldName, newName)
	}
	if c.renameErr != nil {
		return c.renameErr
	}
	return os.Rename(oldName, newName)
}

func useLocalRemoteTemplateConnection(t *testing.T, connection remoteTemplateConnection) {
	t.Helper()
	original := openRemoteTemplateConnection
	openRemoteTemplateConnection = func(context.Context, *config.Context) (remoteTemplateConnection, error) {
		return connection, nil
	}
	t.Cleanup(func() {
		openRemoteTemplateConnection = original
	})
}

func TestRemoteDirectoryNameCollectorEnforcesLimitAcrossWrites(t *testing.T) {
	collector := newRemoteDirectoryNameCollector(2)
	if _, err := collector.Write([]byte("two\x00on")); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Write([]byte("e\x00three\x00")); err != nil {
		t.Fatal(err)
	}
	names, exceeded, err := collector.result()
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded || !reflect.DeepEqual(names, []string{"two", "one"}) {
		t.Fatalf("bounded remote directory names = %#v, exceeded=%t", names, exceeded)
	}
}

func TestCleanupOwnedRemoteTemplateCheckoutIgnoresOrdinaryCallerCancellation(t *testing.T) {
	t.Parallel()

	_, stagingPath := createRemoteTemplateCleanupStaging(t)
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupCalled := false
	connection := &localRemoteTemplateConnection{
		runContext: func(cleanupCtx context.Context, args []string) (string, error) {
			cleanupCalled = true
			if err := cleanupCtx.Err(); err != nil {
				t.Fatalf("ordinary caller cancellation reached recovery context: %v", err)
			}
			if len(args) != 4 || args[0] != "rm" || args[1] != "-rf" || args[2] != "--" || args[3] != stagingPath {
				t.Fatalf("unexpected cleanup command: %v", args)
			}
			return "", os.RemoveAll(stagingPath)
		},
	}
	err := cleanupOwnedRemoteTemplateCheckout(runCtx, connection, stagingPath, context.Canceled)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errComposeCreateRecoveryRequired) {
		t.Fatalf("ordinary cancellation cleanup error = %v", err)
	}
	if !cleanupCalled {
		t.Fatal("ordinary caller cancellation skipped staging recovery")
	}
	if _, err := os.Lstat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("ordinary cancellation left staging path behind: %v", err)
	}
}

func TestCleanupOwnedRemoteTemplateCheckoutPreservesRecordedLockLossBeforeCleanup(t *testing.T) {
	t.Parallel()

	_, stagingPath := createRemoteTemplateCleanupStaging(t)
	runCtx, cancel := context.WithCancelCause(context.Background())
	cancel(config.ErrProjectMutationLockLost)
	cause := errors.New("clone failed")
	connection := &localRemoteTemplateConnection{
		runContext: func(context.Context, []string) (string, error) {
			t.Fatal("destructive cleanup ran after lock loss was recorded")
			return "", nil
		},
	}
	err := cleanupOwnedRemoteTemplateCheckout(runCtx, connection, stagingPath, cause)
	if !errors.Is(err, cause) || !errors.Is(err, config.ErrProjectMutationLockLost) || !errors.Is(err, errComposeCreateRecoveryRequired) {
		t.Fatalf("recorded lock-loss cleanup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingPath, "partial")); err != nil {
		t.Fatalf("recorded lock loss did not preserve staging: %v", err)
	}
}

func TestCleanupOwnedRemoteTemplateCheckoutPreservesLockLossDuringCleanup(t *testing.T) {
	t.Parallel()

	_, stagingPath := createRemoteTemplateCleanupStaging(t)
	runCtx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("clone failed")
	connection := &localRemoteTemplateConnection{
		runContext: func(cleanupCtx context.Context, args []string) (string, error) {
			if err := cleanupCtx.Err(); err != nil {
				t.Fatalf("cleanup context was cancelled before lock loss: %v", err)
			}
			if len(args) != 4 || args[3] != stagingPath {
				t.Fatalf("unexpected cleanup command: %v", args)
			}
			cancel(config.ErrProjectMutationLockLost)
			return "", context.Canceled
		},
	}
	err := cleanupOwnedRemoteTemplateCheckout(runCtx, connection, stagingPath, cause)
	if !errors.Is(err, cause) || !errors.Is(err, config.ErrProjectMutationLockLost) || !errors.Is(err, errComposeCreateRecoveryRequired) {
		t.Fatalf("during-cleanup lock-loss error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingPath, "partial")); err != nil {
		t.Fatalf("lock loss during cleanup did not preserve staging: %v", err)
	}
}

func TestInspectRemoteTemplateCheckoutReadsValidatedMetadata(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	libops := filepath.Join(projectDir, ".libops")
	if err := os.Mkdir(libops, 0o750); err != nil {
		t.Fatal(err)
	}
	contract := []byte(`apiVersion: sitectl.libops.io/v1alpha1
kind: TemplateContract
schema: 1
spec:
  componentDefaults:
    revision: components-v4
`)
	if err := os.WriteFile(filepath.Join(libops, "template-contract.yaml"), contract, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libops, "component-defaults.revision"), []byte("components-v4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			want := []string{"git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}"}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("remote Git args = %v, want %v", args, want)
			}
			return strings.ToUpper(testTemplateCommit), nil
		},
	}

	metadata, err := inspectRemoteTemplateCheckout(context.Background(), connection, projectDir)
	if err != nil {
		t.Fatalf("inspectRemoteTemplateCheckout() error = %v", err)
	}
	if metadata.Commit != testTemplateCommit || !reflect.DeepEqual(metadata.Contract, contract) || metadata.ComponentDefaultsRevision != "components-v4" {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestRemoteComposeCreateObservationRejectsNestedTemplateMutation(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".libops", "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	lock, err := buildTemplateLock("https://example.org/template.git", templateCheckoutMetadata{
		Commit: testTemplateCommit,
		Ref:    "main",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, filepath.FromSlash(templateLockPath)), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	nestedPath := filepath.Join(projectDir, ".libops", "config", "settings.yaml")
	if err := os.WriteFile(nestedPath, []byte("value: one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := &localRemoteTemplateConnection{}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	req := ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
		TemplateBranch: "main",
	}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), req, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if err := os.WriteFile(nestedPath, []byte("value: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = (&SDK{}).RevalidateComposeCreateTargetContext(context.Background(), req, ctx, observation)
	if err == nil || !strings.Contains(err.Error(), "changed while create waited") {
		t.Fatalf("remote nested template mutation revalidation error = %v, want compare-and-swap refusal", err)
	}
}

func TestInspectRemoteTemplateCheckoutRejectsUnsafeMetadataBeforeFinalization(t *testing.T) {
	validContract := []byte("apiVersion: sitectl.libops.io/v1alpha1\nkind: TemplateContract\nschema: 1\n")
	tests := []struct {
		name      string
		setup     func(*testing.T, string)
		wantError string
	}{
		{
			name: "missing Git history",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				if err := os.Mkdir(projectDir, 0o750); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "no Git history",
		},
		{
			name: "Git history symlink",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				if err := os.Mkdir(projectDir, 0o750); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "git")
				if err := os.Mkdir(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(projectDir, ".git")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "no Git history",
		},
		{
			name: "metadata directory symlink",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				createRemoteGitDirectory(t, projectDir)
				target := filepath.Join(t.TempDir(), "metadata")
				if err := os.Mkdir(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(projectDir, ".libops")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "real directory",
		},
		{
			name: "source lock",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				createRemoteGitDirectory(t, projectDir)
				libops := filepath.Join(projectDir, ".libops")
				if err := os.Mkdir(libops, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(libops, "template.lock.yaml"), []byte("kind: TemplateLock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "must not contain",
		},
		{
			name: "contract symlink",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				createRemoteGitDirectory(t, projectDir)
				libops := filepath.Join(projectDir, ".libops")
				if err := os.Mkdir(libops, 0o750); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(projectDir, "contract-target.yaml")
				if err := os.WriteFile(target, validContract, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../contract-target.yaml", filepath.Join(libops, "template-contract.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "regular file",
		},
		{
			name: "oversized contract",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				createRemoteGitDirectory(t, projectDir)
				libops := filepath.Join(projectDir, ".libops")
				if err := os.Mkdir(libops, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(libops, "template-contract.yaml"), make([]byte, maxTemplateContractBytes+1), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "exceeds",
		},
		{
			name: "component revision mismatch",
			setup: func(t *testing.T, projectDir string) {
				t.Helper()
				createRemoteGitDirectory(t, projectDir)
				libops := filepath.Join(projectDir, ".libops")
				if err := os.Mkdir(libops, 0o750); err != nil {
					t.Fatal(err)
				}
				contract := []byte(`apiVersion: sitectl.libops.io/v1alpha1
kind: TemplateContract
schema: 1
spec:
  componentDefaults:
    revision: components-v4
`)
				if err := os.WriteFile(filepath.Join(libops, "template-contract.yaml"), contract, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(libops, "component-defaults.revision"), []byte("components-v5\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "differs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "site")
			test.setup(t, projectDir)
			connection := &localRemoteTemplateConnection{
				run: func([]string) (string, error) {
					return testTemplateCommit, nil
				},
			}
			if _, err := inspectRemoteTemplateCheckout(context.Background(), connection, projectDir); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("inspectRemoteTemplateCheckout() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestRemoteTemplateCheckoutRejectsNonEmptyProject(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(filepath.Join(projectDir, "empty-directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	connection := &localRemoteTemplateConnection{}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	created, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), string(CheckoutSourceExisting)) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureRemoteComposeTemplateCheckout() created = true, want false")
	}
	if len(connection.commands) != 0 {
		t.Fatalf("remote commands = %v, want none", connection.commands)
	}
	if !connection.closed {
		t.Fatal("remote template connection was not closed")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "empty-directory")); err != nil {
		t.Fatalf("existing project was modified: %v", err)
	}
}

func TestRemoteTemplateCheckoutDoesNotCleanRejectedCloneFromExistingRoot(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	repository := "git@github.com:libops/omeka-s.git"
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				createRemoteGitDirectory(t, projectDir)
				libops := filepath.Join(projectDir, ".libops")
				if err := os.Mkdir(libops, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(libops, "template.lock.yaml"), []byte("kind: TemplateLock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", nil
			case len(args) > 3 && args[3] == "rev-parse":
				return testTemplateCommit, nil
			default:
				t.Fatalf("unexpected remote command args: %v", args)
				return "", nil
			}
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	_, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("pre-existing project root was removed: %v", readErr)
	}
	if len(entries) == 0 {
		t.Fatal("rejected checkout contents in a pre-existing root were removed")
	}
	if _, statErr := os.Stat(filepath.Join(projectDir, ".libops", "template.lock.yaml")); statErr != nil {
		t.Fatalf("rejected checkout contents in a pre-existing root were modified: %v", statErr)
	}
}

func TestRemoteTemplateCloneFailureCleansNewProjectDirectory(t *testing.T) {
	useNoopComposeCreateProjectMutationLock(t)
	projectDir := filepath.Join(t.TempDir(), "site")
	cloneErr := errors.New("clone failed")
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				stagingPath := args[len(args)-1]
				if err := os.WriteFile(filepath.Join(stagingPath, "partial"), []byte("partial\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", cloneErr
			case len(args) == 4 && args[0] == "rm" && args[1] == "-rf" && args[2] == "--":
				return "", os.RemoveAll(args[3])
			default:
				t.Fatalf("unexpected remote command args: %v", args)
				return "", nil
			}
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	created, err := sdk.EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "git@github.com:libops/omeka-s.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), cloneErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureRemoteComposeTemplateCheckout() created = true, want false")
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("claimed remote project root was removed after clone failure: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial checkout remains after clone failure: %v", entries)
	}
}

func TestRemoteTemplateDirectoryClaimDoesNotDeleteConcurrentWinner(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	marker := filepath.Join(projectDir, "concurrent")
	connection := &localRemoteTemplateConnection{
		mkdir: func(directory string) error {
			if directory != projectDir {
				t.Fatalf("claimed directory = %q, want %q", directory, projectDir)
			}
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return os.ErrExist
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	created, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), "claim remote project directory") {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureRemoteComposeTemplateCheckout() created = true, want false")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("concurrent winner was deleted: %v", err)
	}
	if len(connection.commands) != 0 {
		t.Fatalf("remote commands = %v, want none", connection.commands)
	}
}

func TestRemoteTemplateCloneFailureDoesNotDeletePreexistingDirectory(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(projectDir, "concurrent-file")
	cloneErr := errors.New("clone failed")
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			if len(args) > 1 && args[1] == "clone" {
				if err := os.WriteFile(marker, []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", cloneErr
			}
			t.Fatalf("unexpected remote command args: %v", args)
			return "", nil
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	_, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), cloneErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pre-existing project contents were removed after clone failure: %v", err)
	}
}

func TestEnsureRemoteComposeTemplateCheckoutUsesSFTPAndArgvGit(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	repository := "git@github.com:libops/omeka-s.git"
	connection := &localRemoteTemplateConnection{}
	connection.run = func(args []string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"git", "clone", "--branch", "main", "--", repository, projectDir}):
			createRemoteGitDirectory(t, projectDir)
			if err := os.WriteFile(filepath.Join(projectDir, ".git", "template-history"), []byte("template\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "", nil
		case reflect.DeepEqual(args, []string{"git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}"}):
			return testTemplateCommit, nil
		case reflect.DeepEqual(args, []string{"rm", "-rf", "--", filepath.Join(projectDir, ".git")}):
			return "", os.RemoveAll(filepath.Join(projectDir, ".git"))
		case reflect.DeepEqual(args, []string{"git", "-C", projectDir, "init", "-b", "main"}):
			if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, ".git", "fresh"), []byte("fresh\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "", nil
		default:
			t.Fatalf("unexpected remote command args: %v", args)
			return "", nil
		}
	}
	connection.beforeGitRemoval = func(_ string) {
		matches, err := filepath.Glob(filepath.Join(projectDir, ".libops", ".template.lock.yaml.tmp-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("temporary locks before history removal = %v, want one", matches)
		}
		info, err := os.Stat(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("temporary lock mode = %o, want 644", info.Mode().Perm())
		}
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{
		DockerHostType: config.ContextRemote,
		ProjectDir:     projectDir,
		SSHHostname:    "example.invalid",
	}

	created, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err != nil {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if !created {
		t.Fatal("ensureRemoteComposeTemplateCheckout() created = false, want true")
	}
	if len(connection.commands) != 4 {
		t.Fatalf("remote command count = %d, want 4: %v", len(connection.commands), connection.commands)
	}
	if !connection.closed {
		t.Fatal("remote template connection was not closed")
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git", "fresh")); err != nil {
		t.Fatalf("fresh Git repository missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git", "template-history")); !os.IsNotExist(err) {
		t.Fatalf("template Git history remains: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(projectDir, ".libops", ".template.lock.yaml.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary locks = %v, err = %v", matches, err)
	}
	libopsInfo, err := os.Stat(filepath.Join(projectDir, ".libops"))
	if err != nil {
		t.Fatal(err)
	}
	if libopsInfo.Mode().Perm() != 0o750 {
		t.Fatalf(".libops mode = %o, want 750", libopsInfo.Mode().Perm())
	}
	lockPath := filepath.Join(projectDir, filepath.FromSlash(templateLockPath))
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0o644 {
		t.Fatalf("template lock mode = %o, want 644", lockInfo.Mode().Perm())
	}
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock templateLock
	if err := yaml.Unmarshal(lockData, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Template.Repository != repository || lock.Template.Ref != "main" || lock.Template.Commit != testTemplateCommit {
		t.Fatalf("template lock source = %+v", lock.Template)
	}
}

func TestRemoteTemplateCheckoutIsCleanedWhenGitInitFails(t *testing.T) {
	useNoopComposeCreateProjectMutationLock(t)
	projectDir := filepath.Join(t.TempDir(), "site")
	repository := "git@github.com:libops/omeka-s.git"
	initErr := errors.New("git init failed")
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				stagingPath := args[len(args)-1]
				createRemoteGitDirectory(t, stagingPath)
				if err := os.WriteFile(filepath.Join(stagingPath, ".git", "template-history"), []byte("template\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", nil
			case len(args) > 3 && args[3] == "rev-parse":
				return testTemplateCommit, nil
			case len(args) == 4 && args[0] == "rm" && filepath.Base(args[3]) == ".git":
				return "", os.RemoveAll(args[3])
			case len(args) > 3 && args[3] == "init":
				stagingPath := args[2]
				if err := os.Mkdir(filepath.Join(stagingPath, ".git"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stagingPath, ".git", "partial"), []byte("partial\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", initErr
			case len(args) == 4 && args[0] == "rm" && args[1] == "-rf" && args[2] == "--":
				return "", os.RemoveAll(args[3])
			default:
				t.Fatalf("unexpected remote command args: %v", args)
				return "", nil
			}
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	_, err := sdk.EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), initErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("claimed remote project root was removed after init failure: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed checkout remains after cleanup: %v", entries)
	}
}

func TestRemoteTemplateCheckoutIsCleanedWhenLockPublishFails(t *testing.T) {
	useNoopComposeCreateProjectMutationLock(t)
	projectDir := filepath.Join(t.TempDir(), "site")
	repository := "git@github.com:libops/omeka-s.git"
	publishErr := errors.New("rename failed")
	connection := &localRemoteTemplateConnection{
		renameErr: publishErr,
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				createRemoteGitDirectory(t, args[len(args)-1])
				return "", nil
			case len(args) > 3 && args[3] == "rev-parse":
				return testTemplateCommit, nil
			case len(args) == 4 && args[0] == "rm" && filepath.Base(args[3]) == ".git":
				return "", os.RemoveAll(args[3])
			case len(args) > 3 && args[3] == "init":
				return "", os.Mkdir(filepath.Join(args[2], ".git"), 0o750)
			case len(args) == 4 && args[0] == "rm" && args[1] == "-rf" && args[2] == "--":
				return "", os.RemoveAll(args[3])
			default:
				t.Fatalf("unexpected remote command args: %v", args)
				return "", nil
			}
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}

	_, err := sdk.EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), publishErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("claimed remote project root was removed after lock publish failure: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed checkout remains after cleanup: %v", entries)
	}
}

func createRemoteGitDirectory(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
}

func createRemoteTemplateCleanupStaging(t *testing.T) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "partial"), []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return projectDir, stagingPath
}
