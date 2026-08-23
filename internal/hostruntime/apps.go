package hostruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type Apps struct {
	Manifest          Manifest
	LifecycleExecutor string
	ApplicationEnv    string
	StateDir          string
	SitectlExecutable string
	Stdout            io.Writer
	Stderr            io.Writer
}

func (a Apps) PrepareSources(ctx context.Context, names []string) error {
	for _, name := range names {
		app, ok := a.Manifest[name]
		if !ok {
			return fmt.Errorf("application %q is not in the manifest", name)
		}
		if err := a.prepareSource(ctx, app, "init"); err != nil {
			return err
		}
	}
	return nil
}

func (a Apps) RunLifecycle(ctx context.Context, name, lifecycle string) error {
	app, ok := a.Manifest[name]
	if !ok {
		return fmt.Errorf("application %q is not in the manifest", name)
	}
	commands, err := app.Commands(lifecycle)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		if isDefaultLifecycleCommand(command, lifecycle) {
			continue
		}
		if err := a.run(ctx, app, "--validate", lifecycle, command); err != nil {
			return fmt.Errorf("validate %s lifecycle for %q: %w", lifecycle, name, err)
		}
	}
	if err := a.prepareSource(ctx, app, lifecycle); err != nil {
		return err
	}
	if lifecycle == "init" {
		if err := scaffoldApplicationDefaults(app); err != nil {
			return fmt.Errorf("scaffold application defaults for %q: %w", app.Name, err)
		}
		if err := a.prepareEnvironment(app); err != nil {
			return err
		}
	}
	if lifecycle != "down" {
		if err := a.rejectHostNetwork(ctx, app); err != nil {
			return err
		}
	}
	if lifecycle == "rollout" {
		if err := a.restoreManagedDiff(ctx, app); err != nil {
			return err
		}
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		if isDefaultLifecycleCommand(command, lifecycle) {
			if err := a.runDefaultLifecycle(ctx, app, lifecycle); err != nil {
				return fmt.Errorf("run default %s lifecycle for %q: %w", lifecycle, name, err)
			}
			continue
		}
		if err := a.run(ctx, app, lifecycle, command); err != nil {
			return fmt.Errorf("run %s lifecycle for %q: %w", lifecycle, name, err)
		}
	}
	if lifecycle != "down" {
		if err := a.recordHead(ctx, app); err != nil {
			return err
		}
	}
	if lifecycle == "init" || lifecycle == "rollout" {
		if err := a.recordManagedDiff(ctx, app); err != nil {
			return err
		}
	}
	return nil
}

func isDefaultLifecycleCommand(command, lifecycle string) bool {
	return strings.TrimSpace(command) == "sitectl:default "+lifecycle
}

func (a Apps) runDefaultLifecycle(ctx context.Context, app Application, lifecycle string) error {
	run := func(args ...string) error {
		executable := a.SitectlExecutable
		if executable == "" {
			executable = "sitectl"
		}
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir = app.ProjectDir
		command.Stdout, command.Stderr = a.Stdout, a.Stderr
		command.Env = append(os.Environ(), app.Environment()...)
		if err := command.Run(); err != nil {
			return fmt.Errorf("sitectl %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	contextName := app.SitectlContextName
	switch lifecycle {
	case "init":
		if err := run("config", "set-context", contextName,
			"--type", "local", "--project-dir", app.ProjectDir,
			"--site", hostSiteName(app.Name), "--plugin", app.SitectlPlugin,
			"--environment", app.SitectlEnvironment,
			"--compose-project-name", app.ComposeProjectName,
			"--docker-socket", "/var/run/docker.sock", "--env-file", ".env",
			"--yolo", "--default"); err != nil {
			return err
		}
		if app.SitectlPlugin == "" || app.SitectlPlugin == "core" {
			return nil
		}
		ingress := []string{"set", "ingress", "enabled", "--context", contextName, "--yolo"}
		mode := app.Ingress.Mode
		if app.Ingress.LetsEncrypt && mode == "" {
			mode = "https-letsencrypt"
		}
		for _, pair := range [][2]string{{"--mode", mode}, {"--domain", app.Ingress.Domain}, {"--acme-email", app.Ingress.ACMEEmail}, {"--max-upload-size", app.Ingress.MaxUploadSize}, {"--upload-timeout", app.Ingress.UploadTimeout}} {
			if pair[1] != "" {
				ingress = append(ingress, pair[0], pair[1])
			}
		}
		for _, address := range app.Ingress.TrustedIPs {
			if address != "" {
				ingress = append(ingress, "--trusted-ip", address)
			}
		}
		if err := run(ingress...); err != nil {
			return err
		}
		if app.Ingress.BotMitigation {
			if err := run("set", "bot-mitigation", "on", "--context", contextName, "--yolo"); err != nil {
				return err
			}
		}
		return run("converge", "--context", contextName, "--yolo")
	case "up":
		if err := run("compose", "--context", contextName, "up", "-d", "--remove-orphans"); err != nil {
			return err
		}
		if err := run("healthcheck", "--context", contextName, "--persist"); err != nil {
			return err
		}
		return a.verifyNonProduction(ctx, app)
	case "down":
		return run("compose", "--context", contextName, "down")
	case "rollout":
		args := []string{"deploy", "--context", contextName}
		if ref := rolloutRef(); ref != "" {
			args = append(args, "--ref", ref)
		} else {
			args = append(args, "--skip-git")
		}
		if err := run(args...); err != nil {
			return err
		}
		if err := run("healthcheck", "--context", contextName, "--persist"); err != nil {
			return err
		}
		return a.verifyNonProduction(ctx, app)
	default:
		return fmt.Errorf("unsupported default lifecycle %q", lifecycle)
	}
}

func (a Apps) prepareEnvironment(app Application) error {
	envPath := filepath.Join(app.ProjectDir, ".env")
	applicationEnv := a.ApplicationEnv
	if applicationEnv == "" {
		applicationEnv = "/home/cloud-compose/application-env.json"
	}
	if err := SyncComposeEnv(envPath, applicationEnv); err != nil {
		return fmt.Errorf("sync application environment for %q: %w", app.Name, err)
	}
	for _, setting := range [][2]string{
		{"COMPOSE_PROJECT_NAME", app.ComposeProjectName},
		{"SITE_NAME", hostSiteName(app.Name)},
		{"COMPOSE_BIND_PORT", fmt.Sprintf("%d", app.IngressPort)},
	} {
		if err := SetComposeEnv(envPath, setting[0], setting[1], "# cloud-compose managed: "); err != nil {
			return fmt.Errorf("set %s for %q: %w", setting[0], app.Name, err)
		}
	}
	return nil
}

func (a Apps) rejectHostNetwork(ctx context.Context, app Application) error {
	if os.Getenv("CLOUD_COMPOSE_PROVIDER") != "gcp" {
		return nil
	}
	command := exec.CommandContext(ctx, "docker", "compose", "config", "--format", "json")
	command.Dir = app.ProjectDir
	command.Env = append(os.Environ(), app.Environment()...)
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("render Compose network configuration for %q: %w", app.Name, err)
	}
	var config struct {
		Services map[string]struct {
			NetworkMode string `json:"network_mode"`
			Build       struct {
				Network      string   `json:"network"`
				Entitlements []string `json:"entitlements"`
			} `json:"build"`
		} `json:"services"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&config); err != nil || config.Services == nil {
		return fmt.Errorf("decode Compose network configuration for %q", app.Name)
	}
	for service, settings := range config.Services {
		if settings.NetworkMode == "host" || settings.Build.Network == "host" || slices.Contains(settings.Build.Entitlements, "network.host") || slices.Contains(settings.Build.Entitlements, "security.insecure") {
			return fmt.Errorf("application %q service %q uses forbidden host networking or build entitlement", app.Name, service)
		}
	}
	return nil
}

func (a Apps) statePath(app Application, suffix string) string {
	stateDir := a.StateDir
	if stateDir == "" {
		stateDir = "/home/cloud-compose/state"
	}
	return filepath.Join(stateDir, app.Name+suffix)
}

func (a Apps) recordHead(ctx context.Context, app Application) error {
	command := exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "rev-parse", "--verify", "HEAD")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("record application source revision for %q: %w", app.Name, err)
	}
	head := strings.TrimSpace(string(output))
	if !commitPattern.MatchString(head) {
		return fmt.Errorf("application %q resolved an invalid source revision", app.Name)
	}
	path := a.statePath(app, ".deployed-head")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create managed application state: %w", err)
	}
	return writeAtomic(path, []byte(strings.ToLower(head)+"\n"), 0o640)
}

func (a Apps) managedDiff(ctx context.Context, app Application) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read managed application changes for %q: %w", app.Name, err)
	}
	sum := sha256.Sum256(output)
	return []byte(fmt.Sprintf("%x\n", sum)), nil
}

func (a Apps) recordManagedDiff(ctx context.Context, app Application) error {
	digest, err := a.managedDiff(ctx, app)
	if err != nil {
		return err
	}
	path := a.statePath(app, ".managed-diff")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create managed application state: %w", err)
	}
	return writeAtomic(path, digest, 0o640)
}

func (a Apps) restoreManagedDiff(ctx context.Context, app Application) error {
	digest, err := a.managedDiff(ctx, app)
	if err != nil {
		return err
	}
	if string(digest) == fmt.Sprintf("%x\n", sha256.Sum256(nil)) {
		return nil
	}
	recorded, err := os.ReadFile(a.statePath(app, ".managed-diff"))
	if err != nil || !bytes.Equal(recorded, digest) {
		return fmt.Errorf("application %q contains unrecorded tracked changes", app.Name)
	}
	command := exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "restore", "--source=HEAD", "--staged", "--worktree", "--", ".")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("restore managed application changes for %q: %w: %s", app.Name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a Apps) verifyNonProduction(ctx context.Context, app Application) error {
	if app.SitectlEnvironment == "production" {
		return nil
	}
	args := append([]string{"verify", "--context", app.SitectlContextName}, app.SitectlVerifyArgs...)
	executable := a.SitectlExecutable
	if executable == "" {
		executable = "sitectl"
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = app.ProjectDir
	command.Stdout, command.Stderr = a.Stdout, a.Stderr
	command.Env = append(os.Environ(), app.Environment()...)
	return command.Run()
}

func rolloutRef() string {
	if commit := strings.TrimSpace(os.Getenv("GIT_COMMIT_SHA")); commitPattern.MatchString(commit) {
		return commit
	}
	if ref := strings.TrimSpace(os.Getenv("GIT_REF")); ref != "" {
		return ref
	}
	return strings.TrimSpace(os.Getenv("GIT_BRANCH"))
}

func hostSiteName(fallback string) string {
	for _, name := range []string{"CLOUD_COMPOSE_INSTANCE_NAME", "GCP_INSTANCE_NAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
}

func (a Apps) run(ctx context.Context, app Application, args ...string) error {
	executor := a.LifecycleExecutor
	if executor == "" {
		executor = "/etc/cloud-compose/libexec/run-lifecycle-program.sh"
	}
	command := exec.CommandContext(ctx, executor, args...)
	command.Dir = app.ProjectDir
	command.Stdout = a.Stdout
	command.Stderr = a.Stderr
	command.Env = append(os.Environ(), app.Environment()...)
	return command.Run()
}

func (a Apps) prepareSource(ctx context.Context, app Application, lifecycle string) error {
	if !commitPattern.MatchString(app.DockerComposeBranch) {
		check := exec.CommandContext(ctx, "git", "check-ref-format", "refs/cloud-compose/"+app.DockerComposeBranch)
		if err := check.Run(); err != nil {
			return fmt.Errorf("invalid application repository ref %q", app.DockerComposeBranch)
		}
	}
	gitDir := filepath.Join(app.ProjectDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		if err := verifyOrigin(ctx, app); err != nil {
			return err
		}
		if err := a.verifyCleanSource(ctx, app); err != nil {
			return err
		}
		if lifecycle != "init" {
			return nil
		}
		if err := a.restoreManagedDiff(ctx, app); err != nil {
			return err
		}
		return a.updateSource(ctx, app)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect application source: %w", err)
	}
	if lifecycle == "down" {
		return fmt.Errorf("application source is missing for down lifecycle: %s", app.ProjectDir)
	}
	if err := os.MkdirAll(filepath.Dir(app.ProjectDir), 0o775); err != nil {
		return fmt.Errorf("create application source parent: %w", err)
	}
	var command *exec.Cmd
	if commitPattern.MatchString(app.DockerComposeBranch) {
		if err := os.MkdirAll(app.ProjectDir, 0o775); err != nil {
			return fmt.Errorf("create application source: %w", err)
		}
		command = exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "init")
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("initialize application source: %w: %s", err, strings.TrimSpace(string(output)))
		}
		for _, args := range [][]string{{"remote", "add", "--", "origin", app.DockerComposeRepo}, {"fetch", "--force", "--no-tags", "--", "origin", app.DockerComposeBranch}, {"cat-file", "-e", app.DockerComposeBranch + "^{commit}"}, {"checkout", "--detach", app.DockerComposeBranch}} {
			command = exec.CommandContext(ctx, "git", append([]string{"-C", app.ProjectDir}, args...)...)
			command.Stdout, command.Stderr = a.Stdout, a.Stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("prepare pinned application source: %w", err)
			}
		}
		return nil
	}
	command = exec.CommandContext(ctx, "git", "clone", "--branch", app.DockerComposeBranch, "--", app.DockerComposeRepo, app.ProjectDir)
	command.Stdout = a.Stdout
	command.Stderr = a.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("clone application source: %w", err)
	}
	return a.verifyCleanSource(ctx, app)
}

func (a Apps) updateSource(ctx context.Context, app Application) error {
	if commitPattern.MatchString(app.DockerComposeBranch) {
		for _, args := range [][]string{{"fetch", "--force", "--no-tags", "--", "origin", app.DockerComposeBranch}, {"cat-file", "-e", app.DockerComposeBranch + "^{commit}"}, {"checkout", "--detach", app.DockerComposeBranch}} {
			if err := runGit(ctx, app.ProjectDir, a.Stdout, a.Stderr, args...); err != nil {
				return fmt.Errorf("converge pinned application source for %q: %w", app.Name, err)
			}
		}
		return a.verifyExpectedHead(ctx, app, strings.ToLower(app.DockerComposeBranch))
	}
	if err := runGit(ctx, app.ProjectDir, a.Stdout, a.Stderr, "fetch", "--prune", "--", "origin", app.DockerComposeBranch); err != nil {
		return fmt.Errorf("fetch application source for %q: %w", app.Name, err)
	}
	localHead, err := gitOutput(ctx, app.ProjectDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	fastForward := exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "merge-base", "--is-ancestor", "HEAD", "FETCH_HEAD").Run() == nil
	if !fastForward {
		recorded, readErr := os.ReadFile(a.statePath(app, ".deployed-head"))
		if readErr != nil || strings.TrimSpace(string(recorded)) != localHead {
			return fmt.Errorf("application %q source diverged from its configured ref", app.Name)
		}
		if err := runGit(ctx, app.ProjectDir, a.Stdout, a.Stderr, "checkout", "--detach", "FETCH_HEAD"); err != nil {
			return err
		}
	} else if err := runGit(ctx, app.ProjectDir, a.Stdout, a.Stderr, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		return err
	}
	fetched, err := gitOutput(ctx, app.ProjectDir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return err
	}
	return a.verifyExpectedHead(ctx, app, fetched)
}

func (a Apps) verifyExpectedHead(ctx context.Context, app Application, expected string) error {
	head, err := gitOutput(ctx, app.ProjectDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if !commitPattern.MatchString(head) || !strings.EqualFold(head, expected) {
		return fmt.Errorf("application %q source did not converge to %s", app.Name, expected)
	}
	return a.verifyCleanSource(ctx, app)
}

func (a Apps) verifyCleanSource(ctx context.Context, app Application) error {
	digest, err := a.managedDiff(ctx, app)
	if err != nil {
		return err
	}
	if string(digest) != fmt.Sprintf("%x\n", sha256.Sum256(nil)) {
		recorded, readErr := os.ReadFile(a.statePath(app, ".managed-diff"))
		if readErr != nil || !bytes.Equal(recorded, digest) {
			return fmt.Errorf("application %q contains unrecorded tracked changes", app.Name)
		}
	}
	untracked, err := gitOutput(ctx, app.ProjectDir, "ls-files", "--others", "--exclude-standard", "--", ":(glob)**/compose*.yml", ":(glob)**/compose*.yaml", ":(glob)**/docker-compose*.yml", ":(glob)**/docker-compose*.yaml")
	if err != nil {
		return err
	}
	if untracked != "" {
		return fmt.Errorf("application %q contains an untracked Compose control file: %s", app.Name, untracked)
	}
	return nil
}

func runGit(ctx context.Context, directory string, stdout, stderr io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Stdout, command.Stderr = stdout, stderr
	return command.Run()
}

func gitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func verifyOrigin(ctx context.Context, app Application) error {
	command := exec.CommandContext(ctx, "git", "-C", app.ProjectDir, "remote", "get-url", "origin")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("read application origin: %w", err)
	}
	if strings.TrimSpace(string(output)) != app.DockerComposeRepo {
		return fmt.Errorf("application source origin does not match manifest")
	}
	return nil
}

func (a Application) Environment() []string {
	verifyArgs, _ := json.Marshal(a.SitectlVerifyArgs)
	return []string{
		"APP_NAME=" + a.Name,
		"DOCKER_COMPOSE_REPO=" + a.DockerComposeRepo,
		"DOCKER_COMPOSE_BRANCH=" + a.DockerComposeBranch,
		"DOCKER_COMPOSE_DIR=" + a.ProjectDir,
		"COMPOSE_PROJECT_NAME=" + a.ComposeProjectName,
		fmt.Sprintf("COMPOSE_BIND_PORT=%d", a.IngressPort),
		"SITECTL_CONTEXT_NAME=" + a.SitectlContextName,
		"SITECTL_PLUGIN=" + a.SitectlPlugin,
		"SITECTL_ENVIRONMENT=" + a.SitectlEnvironment,
		"SITECTL_VERIFY_ARGS_JSON=" + string(verifyArgs),
	}
}
