package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

func writeConfig(cfg *Config, t *testing.T) {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	err = os.WriteFile(ConfigFilePath(), data, 0644)
	if err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
}

func TestContextExistsAndGetContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Start with an empty config
	cfg := &Config{
		CurrentContext: "",
		Contexts:       []Context{},
	}
	writeConfig(cfg, t)

	// Should not exist
	exists, err := ContextExists("foo")
	if err != nil {
		t.Fatalf("ContextExists error: %v", err)
	}
	if exists {
		t.Fatalf("expected context 'foo' to not exist")
	}

	// Save a context and test retrieval
	ctx := Context{
		Name:           "foo",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "myproject",
		ProjectDir:     tempHome,
	}
	err = SaveContext(&ctx, true)
	if err != nil {
		t.Fatalf("SaveContext error: %v", err)
	}

	exists, err = ContextExists("foo")
	if err != nil {
		t.Fatalf("ContextExists error: %v", err)
	}
	if !exists {
		t.Fatalf("expected context 'foo' to exist")
	}

	gotCtx, err := GetContext("foo")
	if err != nil {
		t.Fatalf("GetContext error: %v", err)
	}
	if !contextsEqual(ctx, gotCtx) {
		t.Fatalf("expected context %+v, got %+v", ctx, gotCtx)
	}
}

func TestGetContextReturnsNotFoundError(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	cfg := &Config{
		CurrentContext: "",
		Contexts:       []Context{},
	}
	writeConfig(cfg, t)

	ctx, err := GetContext("missing")
	if !errors.Is(err, ErrContextNotFound) {
		t.Fatalf("expected ErrContextNotFound, got %v", err)
	}
	if ctx.Name != "missing" {
		t.Fatalf("expected placeholder context name missing, got %q", ctx.Name)
	}
}

func contextsEqual(a, b Context) bool {
	return a.Name == b.Name &&
		a.Site == b.Site &&
		a.Plugin == b.Plugin &&
		a.DockerHostType == b.DockerHostType &&
		a.Environment == b.Environment &&
		a.DockerSocket == b.DockerSocket &&
		a.ProjectName == b.ProjectName &&
		a.ComposeProjectName == b.ComposeProjectName &&
		a.ComposeNetwork == b.ComposeNetwork &&
		a.ProjectDir == b.ProjectDir &&
		a.SSHUser == b.SSHUser &&
		a.SSHHostname == b.SSHHostname &&
		a.SSHPort == b.SSHPort &&
		a.SSHKeyPath == b.SSHKeyPath &&
		len(a.EnvFile) == len(b.EnvFile) &&
		len(a.ComposeFile) == len(b.ComposeFile) &&
		a.RunSudo == b.RunSudo &&
		a.DatabaseService == b.DatabaseService &&
		a.DatabaseUser == b.DatabaseUser &&
		a.DatabasePasswordSecret == b.DatabasePasswordSecret &&
		a.DatabaseName == b.DatabaseName
}

func TestContextString(t *testing.T) {
	ctx := Context{
		Name:               "test",
		Site:               "museum",
		Plugin:             "isle",
		DockerHostType:     ContextLocal,
		DockerSocket:       "/var/run/docker.sock",
		ProjectName:        "project",
		ComposeProjectName: "project-compose",
		ProjectDir:         "/tmp",
	}
	s, err := ctx.String()
	if err != nil {
		t.Fatalf("Context.String error: %v", err)
	}
	// check that YAML output contains expected fields
	if !strings.Contains(s, "test") || !strings.Contains(s, "local") || !strings.Contains(s, "museum") || !strings.Contains(s, "isle") {
		t.Fatalf("unexpected context string: %s", s)
	}
}

func TestSaveContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Initialize empty config.
	cfg := &Config{
		CurrentContext: "",
		Contexts:       []Context{},
	}
	writeConfig(cfg, t)

	ctx := Context{
		Name:           "myctx",
		Site:           "museum",
		Plugin:         "core",
		DockerHostType: ContextLocal,
		ProjectDir:     tempHome,
	}
	// Test adding new context.
	err := SaveContext(&ctx, true)
	if err != nil {
		t.Fatalf("SaveContext error: %v", err)
	}
	loadedCfg, err := Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(loadedCfg.Contexts) != 1 {
		t.Fatalf("expected 1 context, got %d", len(loadedCfg.Contexts))
	}
	if loadedCfg.CurrentContext != "myctx" {
		t.Fatalf("expected current context 'myctx', got %s", loadedCfg.CurrentContext)
	}

	// Test updating context.
	ctx.ProjectName = "updated-project"
	ctx.ComposeProjectName = "updated-compose"
	err = SaveContext(&ctx, false)
	if err != nil {
		t.Fatalf("SaveContext error: %v", err)
	}
	loadedCfg, err = Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loadedCfg.Contexts[0].ProjectName != "updated-project" {
		t.Fatalf("expected updated project name, got %s", loadedCfg.Contexts[0].ProjectName)
	}
	if loadedCfg.Contexts[0].ComposeProjectName != "updated-compose" {
		t.Fatalf("expected updated compose project name, got %s", loadedCfg.Contexts[0].ComposeProjectName)
	}
}

func TestCurrentContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create a config with one context and set it as current.
	cfg := &Config{
		CurrentContext: "default",
		Contexts: []Context{
			{
				Name:           "default",
				DockerHostType: ContextLocal,
				ProjectDir:     tempHome,
			},
		},
	}
	writeConfig(cfg, t)

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("context", "default", "test flag")
	ctx, err := CurrentContext(fs)
	if err != nil {
		t.Fatalf("CurrentContext error: %v", err)
	}
	if ctx.Name != "default" {
		t.Fatalf("expected context name 'default', got %s", ctx.Name)
	}

	// Change flag value to a non-existent context.
	err = fs.Set("context", "nonexistent")
	if err != nil {
		t.Fatalf("error setting context flag: %v", err)
	}

	_, err = CurrentContext(fs)
	if err == nil {
		t.Fatalf("expected error for non-existent context")
	}
}

func TestCurrentContextPrefersExplicitFlagOverAutodiscovery(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "site")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	cfg := &Config{
		CurrentContext: "default-site",
		Contexts: []Context{
			{Name: "default-site", DockerHostType: ContextLocal, ProjectDir: filepath.Join(tempHome, "default")},
			{Name: "cwd-site", DockerHostType: ContextLocal, ProjectDir: projectDir},
			{Name: "flag-site", DockerHostType: ContextLocal, ProjectDir: filepath.Join(tempHome, "flag")},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(cfg) error = %v", err)
	}

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("context", "", "")
	if err := fs.Set("context", "flag-site"); err != nil {
		t.Fatalf("Set(context) error = %v", err)
	}

	ctx, err := CurrentContext(fs)
	if err != nil {
		t.Fatalf("CurrentContext() error = %v", err)
	}
	if ctx.Name != "flag-site" {
		t.Fatalf("expected explicit flag-site context, got %q", ctx.Name)
	}
}

func TestReadSmallFileLocal(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	// Create a temporary file.
	filePath := filepath.Join(tempHome, "test.txt")
	content := "small file content"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	ctx := &Context{
		DockerHostType: ContextLocal,
	}
	readContent := ctx.ReadSmallFile(filePath)
	if readContent != content {
		t.Fatalf("expected %q, got %q", content, readContent)
	}
}

func TestDialSSHError(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	// Set SSHKeyPath to a non-existent file to force an error.
	ctx := &Context{
		Name:        "ssh-test",
		SSHUser:     "user",
		SSHHostname: "localhost",
		SSHPort:     22,
		SSHKeyPath:  filepath.Join(tempHome, "nonexistent_key"),
	}
	_, err := ctx.DialSSH()
	if err == nil {
		t.Fatalf("expected error from DialSSH with bad SSHKeyPath")
	}
	if !strings.Contains(err.Error(), "error reading SSH key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProjectDirExistsLocal(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create a temporary directory.
	projectDir := filepath.Join(tempHome, "project")
	err := os.Mkdir(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}
	ctx := &Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	exists, err := ctx.ProjectDirExists()
	if err != nil {
		t.Fatalf("ProjectDirExists error: %v", err)
	}
	if !exists {
		t.Fatalf("expected project dir %s to exist", projectDir)
	}

	// Test non-existent directory.
	ctx.ProjectDir = filepath.Join(tempHome, "nonexistent")
	exists, err = ctx.ProjectDirExists()
	if err != nil {
		t.Fatalf("ProjectDirExists error: %v", err)
	}
	if exists {
		t.Fatalf("expected project dir %s to not exist", ctx.ProjectDir)
	}
}

func TestUploadFileError(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// For this test, we simulate an error by forcing DialSSH to fail.
	ctx := &Context{
		SSHUser:     "user",
		SSHHostname: "localhost",
		SSHPort:     22,
		// Point to a non-existent key to force DialSSH error.
		SSHKeyPath: filepath.Join(tempHome, "nonexistent_key"),
	}
	// Create a temporary local file.
	sourcePath := filepath.Join(tempHome, "source.txt")
	err := os.WriteFile(sourcePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}
	err = ctx.UploadFile(sourcePath, "/remote/path/dest.txt")
	if err == nil {
		t.Fatalf("expected error from UploadFile due to SSH dial failure")
	}
}

func TestVerifyRemoteInputExistingConfig(t *testing.T) {
	// Simulate empty input for SSH hostname prompt.
	input := "\n"
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	_, err = inW.WriteString(input)
	if err != nil {
		t.Fatalf("failed to write input: %v", err)
	}
	inW.Close()
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()
	os.Stdin = inR

	original := Context{
		SSHHostname: "foo.example.com.dev",
		SSHUser:     "bar",
		SSHPort:     123,
		SSHKeyPath:  "/assuming/we/already/checked",
		ProjectName: "baz",
	}
	cc := original

	err = cc.VerifyRemoteInput(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contextsEqual(cc, original) {
		t.Fatalf("expected context %+v, got %+v", original, cc)
	}
}

func TestGetSshUri(t *testing.T) {
	tests := []struct {
		name     string
		context  Context
		expected string
	}{
		{
			name: "local context returns empty string",
			context: Context{
				DockerHostType: ContextLocal,
			},
			expected: "",
		},
		{
			name: "remote context with default port",
			context: Context{
				DockerHostType: ContextRemote,
				SSHHostname:    "example.com",
				SSHUser:        "testuser",
				SSHPort:        0, // Should default to 22
			},
			expected: "ssh_host=example.com&ssh_port=22&ssh_user=testuser",
		},
		{
			name: "remote context with custom port",
			context: Context{
				DockerHostType: ContextRemote,
				SSHHostname:    "example.com",
				SSHUser:        "testuser",
				SSHPort:        2222,
			},
			expected: "ssh_host=example.com&ssh_port=2222&ssh_user=testuser",
		},
		{
			name: "remote context with SSH key path",
			context: Context{
				DockerHostType: ContextRemote,
				SSHHostname:    "example.com",
				SSHUser:        "testuser",
				SSHPort:        22,
				SSHKeyPath:     "/home/user/.ssh/id_rsa",
			},
			expected: "ssh_host=example.com&ssh_keyLocation=%2Fhome%2Fuser%2F.ssh%2Fid_rsa&ssh_keyLocationEnabled=1&ssh_port=22&ssh_user=testuser",
		},
		{
			name: "remote context without SSH key path",
			context: Context{
				DockerHostType: ContextRemote,
				SSHHostname:    "server.example.com",
				SSHUser:        "admin",
				SSHPort:        22,
				SSHKeyPath:     "",
			},
			expected: "ssh_host=server.example.com&ssh_port=22&ssh_user=admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.context.GetSshUri()
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
