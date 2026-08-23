package hostruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLifecycleValidatesBeforeExecution(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(root, "git")
	writeExecutable(t, git, "#!/bin/sh\ncase \"$*\" in\n  *'remote get-url origin'*) printf '%s\\n' 'https://example.com/app' ;;\n  *'rev-parse --verify HEAD'*) printf '%040d\\n' 0 ;;\nesac\n")
	executor := filepath.Join(root, "executor")
	log := filepath.Join(root, "calls")
	writeExecutable(t, executor, "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$CALL_LOG\"\n")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALL_LOG", log)
	apps := Apps{
		Manifest: Manifest{"app": {
			Name: "app", DockerComposeRepo: "https://example.com/app", DockerComposeBranch: "main",
			ProjectDir: project, ComposeProjectName: "app", SitectlContextName: "app",
			UpCommands: []string{"sitectl compose up", "sitectl healthcheck"},
		}},
		LifecycleExecutor: executor,
		StateDir:          filepath.Join(root, "state"),
	}
	if err := apps.RunLifecycle(context.Background(), "app", "up"); err != nil {
		t.Fatalf("RunLifecycle() error = %v", err)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := []string{
		"--validate up sitectl compose up", "--validate up sitectl healthcheck",
		"up sitectl compose up", "up sitectl healthcheck",
	}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", lines, want)
	}
}

func TestEnvironmentUsesTypedManifestValues(t *testing.T) {
	environment := (Application{Name: "app", IngressPort: 8080, SitectlVerifyArgs: []string{"--one", "two words"}}).Environment()
	if !contains(environment, "APP_NAME=app") || !contains(environment, "COMPOSE_BIND_PORT=8080") {
		t.Fatalf("Environment() = %v", environment)
	}
}

func TestPrepareSourceRejectsUnrecordedTrackedChanges(t *testing.T) {
	project, remote := testGitProject(t)
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("operator edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := Application{Name: "app", DockerComposeRepo: remote, DockerComposeBranch: "main", ProjectDir: project}
	err := (Apps{StateDir: filepath.Join(t.TempDir(), "state")}).prepareSource(context.Background(), app, "up")
	if err == nil || !strings.Contains(err.Error(), "unrecorded tracked changes") {
		t.Fatalf("prepareSource() error = %v", err)
	}
}

func TestPrepareSourceRestoresRecordedDeploymentToBaseline(t *testing.T) {
	project, remote := testGitProject(t)
	if err := os.WriteFile(filepath.Join(project, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, project, "add", "feature.txt")
	runTestGit(t, project, "commit", "-m", "feature")
	feature := runTestGit(t, project, "rev-parse", "HEAD")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "app.deployed-head"), []byte(feature+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	app := Application{Name: "app", DockerComposeRepo: remote, DockerComposeBranch: "main", ProjectDir: project}
	if err := (Apps{StateDir: state}).prepareSource(context.Background(), app, "init"); err != nil {
		t.Fatalf("prepareSource() error = %v", err)
	}
	want := runTestGit(t, remote, "rev-parse", "refs/heads/main")
	if got := runTestGit(t, project, "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD = %s, want baseline %s", got, want)
	}
}

func TestRejectHostNetworkRejectsBuildEntitlement(t *testing.T) {
	root := t.TempDir()
	docker := filepath.Join(root, "docker")
	writeExecutable(t, docker, "#!/bin/sh\nprintf '%s' '{\"services\":{\"web\":{\"build\":{\"entitlements\":[\"security.insecure\"]}}}}'\n")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLOUD_COMPOSE_PROVIDER", "gcp")
	err := (Apps{}).rejectHostNetwork(context.Background(), Application{Name: "app", ProjectDir: root})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("rejectHostNetwork() error = %v", err)
	}
}

func testGitProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runTestGit(t, root, "init", "--bare", remote)
	seed := filepath.Join(root, "seed")
	runTestGit(t, root, "clone", remote, seed)
	runTestGit(t, seed, "config", "user.email", "test@example.com")
	runTestGit(t, seed, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, seed, "add", "README.md")
	runTestGit(t, seed, "commit", "-m", "baseline")
	runTestGit(t, seed, "branch", "-M", "main")
	runTestGit(t, seed, "push", "origin", "main")
	project := filepath.Join(root, "project")
	runTestGit(t, root, "clone", "--branch", "main", remote, project)
	runTestGit(t, project, "config", "user.email", "test@example.com")
	runTestGit(t, project, "config", "user.name", "Test")
	return project, remote
}

func runTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
