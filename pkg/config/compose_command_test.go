package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDockerVisibleLocalPathFromSSHMountinfo(t *testing.T) {
	mountinfo := `329 320 0:43 /libops/templates /workspace rw,nosuid,nodev,relatime - fuse.sshfs :/Users/jcorall rw,user_id=501,group_id=1000,allow_other`

	got := dockerVisibleLocalPathFromMountinfo("/workspace/wp", mountinfo)
	want := "/Users/jcorall/libops/templates/wp"
	if got != want {
		t.Fatalf("dockerVisibleLocalPathFromMountinfo() = %q, want %q", got, want)
	}
}

func TestDockerComposeGlobalArgsForTranslatedProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yaml"), []byte("services:\n  app:\n    image: app:test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.override.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("COMPOSE_PROJECT_NAME=test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := Context{DockerHostType: ContextLocal, ProjectDir: projectDir}
	gotFiles := ctx.composeCommandFiles()
	wantFiles := []string{
		filepath.Join(projectDir, "docker-compose.yaml"),
		filepath.Join(projectDir, "docker-compose.override.yml"),
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("composeCommandFiles() = %#v, want %#v", gotFiles, wantFiles)
	}
	gotEnv := ctx.composeCommandEnvFiles()
	wantEnv := []string{filepath.Join(projectDir, ".env")}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("composeCommandEnvFiles() = %#v, want %#v", gotEnv, wantEnv)
	}
}

func TestDockerComposeSubcommandArgsAddsNoBuildForTranslatedUp(t *testing.T) {
	got := dockerComposeSubcommandArgs([]string{"up", "--remove-orphans", "-d"}, true)
	want := []string{"up", "--remove-orphans", "-d", "--no-build"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerComposeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestDockerComposeSubcommandArgsKeepsExplicitBuild(t *testing.T) {
	got := dockerComposeSubcommandArgs([]string{"up", "--build", "-d"}, true)
	want := []string{"up", "--build", "-d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerComposeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestDockerComposeSubcommandArgsLeavesUntranslatedUpAlone(t *testing.T) {
	got := dockerComposeSubcommandArgs([]string{"up", "-d"}, false)
	want := []string{"up", "-d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerComposeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestRewriteDockerComposeShellCommandPreservesShellExpansion(t *testing.T) {
	got := rewriteDockerComposeShellCommand(
		"",
		`run --rm -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" init`,
		[]string{"--project-directory", "/host/drupal", "-f", "/workspace/drupal/docker-compose.yml"},
		false,
	)
	want := `docker compose --project-directory /host/drupal -f /workspace/drupal/docker-compose.yml run --rm -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" init`
	if got != want {
		t.Fatalf("rewriteDockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestRewriteDockerComposeShellCommandAppendsNoBuild(t *testing.T) {
	got := rewriteDockerComposeShellCommand(
		"",
		"up --remove-orphans -d",
		[]string{"--project-directory", "/host/drupal"},
		true,
	)
	want := "docker compose --project-directory /host/drupal up --remove-orphans -d --no-build"
	if got != want {
		t.Fatalf("rewriteDockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestDockerComposeShellCommandHonorsRemoteContextFilesInEveryShellBranch(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/omeka",
		ComposeFile:    []string{"compose.yaml", "compose production.yaml"},
		EnvFile:        []string{".env", "production.env"},
	}
	got := ctx.DockerComposeShellCommand("docker compose pull --quiet || docker compose pull")
	for _, expected := range []string{
		"-f /srv/omeka/compose.yaml",
		"-f '/srv/omeka/compose production.yaml'",
		"--env-file /srv/omeka/.env",
		"--env-file /srv/omeka/production.env",
	} {
		if count := strings.Count(got, expected); count != 2 {
			t.Fatalf("DockerComposeShellCommand() = %q, want %q in both fallback branches (count %d)", got, expected, count)
		}
	}
}

func TestDockerComposeShellCommandHonorsContextFilesForBuild(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
		EnvFile:        []string{"build.env"},
	}
	got := ctx.DockerComposeShellCommand("docker compose build --pull app")
	want := "docker compose -f /srv/app/compose.yaml --env-file /srv/app/build.env build --pull app"
	if got != want {
		t.Fatalf("DockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestDockerComposeShellCommandSupportsSafeShellPrefixes(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
		EnvFile:        []string{".env"},
	}
	got := ctx.DockerComposeShellCommand("SITECTL_MODE=deploy env COMPOSE_PARALLEL_LIMIT=2 docker compose up -d")
	want := "SITECTL_MODE=deploy env COMPOSE_PARALLEL_LIMIT=2 docker compose -f /srv/app/compose.yaml --env-file /srv/app/.env up -d"
	if got != want {
		t.Fatalf("DockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestDockerComposeShellCommandDoesNotRewriteComposeText(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
	}
	command := `printf '%s\n' 'docker compose ps' || echo docker compose ps`
	if got := ctx.DockerComposeShellCommand(command); got != command {
		t.Fatalf("DockerComposeShellCommand() = %q, want non-executable Compose text unchanged", got)
	}
}

func TestDockerComposeShellCommandPreservesQuotedShellGrammar(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
	}
	command := `docker compose exec -T app sh -c 'value=$(date +%s) || exit 1; printf "%s\n" "$value" | grep -q .; test "$value" -gt 0'`
	want := `docker compose -f /srv/app/compose.yaml exec -T app sh -c 'value=$(date +%s) || exit 1; printf "%s\n" "$value" | grep -q .; test "$value" -gt 0'`
	if got := ctx.DockerComposeShellCommand(command); got != want {
		t.Fatalf("DockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestDockerComposeShellCommandRewritesPipelineCommands(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
	}
	got := ctx.DockerComposeShellCommand("docker compose ps | docker compose logs --tail=10")
	want := "docker compose -f /srv/app/compose.yaml ps | docker compose -f /srv/app/compose.yaml logs --tail=10"
	if got != want {
		t.Fatalf("DockerComposeShellCommand() = %q, want %q", got, want)
	}
}

func TestDockerComposeShellCommandFailsClosedForAmbiguousGrammar(t *testing.T) {
	t.Parallel()
	ctx := Context{
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml"},
	}
	commands := []string{
		"docker compose ps >/tmp/compose-ps",
		"docker compose ps & wait",
		"(docker compose ps)",
		"docker compose ps 'unterminated",
		"VALUE=$(printf value) docker compose ps",
		"env -u COMPOSE_FILE docker compose ps",
	}
	for _, command := range commands {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			if got := ctx.DockerComposeShellCommand(command); got != command {
				t.Fatalf("DockerComposeShellCommand() = %q, want ambiguous command unchanged", got)
			}
		})
	}
}
