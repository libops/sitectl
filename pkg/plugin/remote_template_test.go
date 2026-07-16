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
	beforeGitRemoval func(string)
	renameErr        error
	closed           bool
}

func (c *localRemoteTemplateConnection) Close() error {
	c.closed = true
	return nil
}

func (c *localRemoteTemplateConnection) Run(_ context.Context, _, _ io.Writer, args ...string) (string, error) {
	command := append([]string(nil), args...)
	c.commands = append(c.commands, command)
	if c.beforeGitRemoval != nil && len(command) == 4 && command[0] == "rm" && command[1] == "-rf" && command[2] == "--" && filepath.Base(command[3]) == ".git" {
		c.beforeGitRemoval(command[3])
	}
	if c.run == nil {
		return "", nil
	}
	return c.run(command)
}

func (c *localRemoteTemplateConnection) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (c *localRemoteTemplateConnection) ReadDir(name string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
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
	return os.Mkdir(directory, 0o750)
}

func (c *localRemoteTemplateConnection) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func (c *localRemoteTemplateConnection) Remove(name string) error {
	return os.Remove(name)
}

func (c *localRemoteTemplateConnection) Rename(oldName, newName string) error {
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

func TestRemoteTemplateCheckoutCleansRejectedCloneAndPreservesExistingRoot(t *testing.T) {
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
			case len(args) > 1 && args[0] == "find" && args[1] == projectDir:
				return "", removeDirectoryContents(projectDir)
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
	if len(entries) != 0 {
		t.Fatalf("rejected checkout contents remain and could bypass validation on retry: %v", entries)
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
	if lock.Template.Repository != repository || lock.Template.Commit != testTemplateCommit {
		t.Fatalf("template lock source = %+v", lock.Template)
	}
}

func TestRemoteTemplateCheckoutIsCleanedWhenGitInitFails(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	repository := "git@github.com:libops/omeka-s.git"
	initErr := errors.New("git init failed")
	connection := &localRemoteTemplateConnection{
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				createRemoteGitDirectory(t, projectDir)
				if err := os.WriteFile(filepath.Join(projectDir, ".git", "template-history"), []byte("template\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", nil
			case len(args) > 3 && args[3] == "rev-parse":
				return testTemplateCommit, nil
			case len(args) == 4 && args[0] == "rm" && args[3] == filepath.Join(projectDir, ".git"):
				return "", os.RemoveAll(args[3])
			case len(args) > 3 && args[3] == "init":
				if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projectDir, ".git", "partial"), []byte("partial\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", initErr
			case len(args) == 4 && args[0] == "rm" && args[3] == projectDir:
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

	_, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), initErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if _, err := os.Lstat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("failed checkout remains after cleanup: %v", err)
	}
}

func TestRemoteTemplateCheckoutIsCleanedWhenLockPublishFails(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	repository := "git@github.com:libops/omeka-s.git"
	publishErr := errors.New("rename failed")
	connection := &localRemoteTemplateConnection{
		renameErr: publishErr,
		run: func(args []string) (string, error) {
			switch {
			case len(args) > 1 && args[1] == "clone":
				createRemoteGitDirectory(t, projectDir)
				return "", nil
			case len(args) > 3 && args[3] == "rev-parse":
				return testTemplateCommit, nil
			case len(args) == 4 && args[0] == "rm" && args[3] == filepath.Join(projectDir, ".git"):
				return "", os.RemoveAll(args[3])
			case len(args) > 3 && args[3] == "init":
				return "", os.Mkdir(filepath.Join(projectDir, ".git"), 0o750)
			case len(args) == 4 && args[0] == "rm" && args[3] == projectDir:
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

	_, err := sdk.ensureRemoteComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo:   repository,
		TemplateBranch: "main",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), publishErr.Error()) {
		t.Fatalf("ensureRemoteComposeTemplateCheckout() error = %v", err)
	}
	if _, err := os.Lstat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("failed checkout remains after cleanup: %v", err)
	}
}

func createRemoteGitDirectory(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
}

func removeDirectoryContents(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
