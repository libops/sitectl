package config

import (
	"os"
	"path/filepath"
	"reflect"
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
