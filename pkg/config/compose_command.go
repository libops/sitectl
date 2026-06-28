package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kballard/go-shellquote"
)

var composeOverrideCandidates = []string{
	"compose.override.yml",
	"compose.override.yaml",
	"docker-compose.override.yml",
	"docker-compose.override.yaml",
}

// DockerComposeGlobalArgs returns docker compose options that must be inserted
// after "docker compose" and before the compose subcommand.
func (c Context) DockerComposeGlobalArgs() []string {
	return c.DockerComposeGlobalArgsForCommand("")
}

func (c Context) DockerComposeGlobalArgsForCommand(command string) []string {
	if strings.TrimSpace(command) == "build" {
		return nil
	}
	dockerProjectDir := c.dockerComposeTranslatedProjectDir()
	if dockerProjectDir == "" {
		return nil
	}

	args := []string{"--project-directory", dockerProjectDir}
	for _, file := range c.composeCommandFiles() {
		args = append(args, "-f", file)
	}
	for _, envFile := range c.composeCommandEnvFiles() {
		args = append(args, "--env-file", envFile)
	}
	return args
}

// DockerComposeSubcommandArgs returns compose subcommand arguments adjusted for
// the local Docker daemon path mapping.
func (c Context) DockerComposeSubcommandArgs(args []string) []string {
	return dockerComposeSubcommandArgs(args, c.dockerComposeTranslatedProjectDir() != "")
}

// DockerComposeShellCommand rewrites simple "docker compose ..." shell command
// strings so Docker receives bind-mount paths visible from the daemon host.
func (c Context) DockerComposeShellCommand(command string) string {
	leading, tail, ok := splitDockerComposeShellCommand(command)
	if !ok {
		return command
	}
	tail = strings.TrimLeft(tail, " \t")
	fields, err := shellquote.Split(tail)
	if err != nil || len(fields) == 0 {
		return command
	}
	subcommand := fields[0]
	globalArgs := c.DockerComposeGlobalArgsForCommand(subcommand)
	if len(globalArgs) == 0 {
		return command
	}
	normalized := c.DockerComposeSubcommandArgs(fields)
	addNoBuild := len(normalized) > len(fields) && normalized[len(normalized)-1] == "--no-build"
	return rewriteDockerComposeShellCommand(leading, tail, globalArgs, addNoBuild)
}

func (c Context) dockerComposeTranslatedProjectDir() string {
	if c.DockerHostType != ContextLocal {
		return ""
	}
	dockerProjectDir := DockerVisibleLocalPath(c.ProjectDir)
	if strings.TrimSpace(dockerProjectDir) == "" || filepath.Clean(dockerProjectDir) == filepath.Clean(c.ProjectDir) {
		return ""
	}
	return dockerProjectDir
}

func dockerComposeSubcommandArgs(args []string, translatedProjectDir bool) []string {
	out := append([]string{}, args...)
	if !translatedProjectDir || len(out) == 0 || strings.TrimSpace(out[0]) != "up" {
		return out
	}
	for _, arg := range out[1:] {
		if arg == "--build" || strings.HasPrefix(arg, "--build=") || arg == "--no-build" || strings.HasPrefix(arg, "--no-build=") {
			return out
		}
	}
	return append(out, "--no-build")
}

func splitDockerComposeShellCommand(command string) (leading, tail string, ok bool) {
	trimmed := strings.TrimLeft(command, " \t")
	leading = command[:len(command)-len(trimmed)]
	if !strings.HasPrefix(trimmed, "docker") {
		return "", "", false
	}
	afterDocker := strings.TrimPrefix(trimmed, "docker")
	if afterDocker == "" || !isShellSpace(afterDocker[0]) {
		return "", "", false
	}
	afterDocker = strings.TrimLeft(afterDocker, " \t")
	if !strings.HasPrefix(afterDocker, "compose") {
		return "", "", false
	}
	afterCompose := strings.TrimPrefix(afterDocker, "compose")
	if afterCompose != "" && !isShellSpace(afterCompose[0]) {
		return "", "", false
	}
	return leading, afterCompose, true
}

func rewriteDockerComposeShellCommand(leading, tail string, globalArgs []string, addNoBuild bool) string {
	parts := []string{leading + "docker", "compose"}
	if len(globalArgs) > 0 {
		parts = append(parts, shellquote.Join(globalArgs...))
	}
	if strings.TrimSpace(tail) != "" {
		parts = append(parts, strings.TrimLeft(tail, " \t"))
	}
	if addNoBuild {
		parts = append(parts, "--no-build")
	}
	return strings.Join(parts, " ")
}

func isShellSpace(value byte) bool {
	return value == ' ' || value == '\t'
}

func (c Context) composeCommandFiles() []string {
	if len(c.ComposeFile) > 0 {
		files := make([]string, 0, len(c.ComposeFile))
		for _, file := range c.ComposeFile {
			if resolved := c.localComposeCommandPath(file); resolved != "" {
				files = append(files, resolved)
			}
		}
		return files
	}

	files := []string{}
	for _, candidate := range composeProjectCandidates {
		path := filepath.Join(c.ProjectDir, candidate)
		if fileExists(path) {
			files = append(files, path)
			break
		}
	}
	for _, candidate := range composeOverrideCandidates {
		path := filepath.Join(c.ProjectDir, candidate)
		if fileExists(path) {
			files = append(files, path)
		}
	}
	return files
}

func (c Context) composeCommandEnvFiles() []string {
	if len(c.EnvFile) > 0 {
		files := make([]string, 0, len(c.EnvFile))
		for _, file := range c.EnvFile {
			if resolved := c.localComposeCommandPath(file); resolved != "" {
				files = append(files, resolved)
			}
		}
		return files
	}
	path := filepath.Join(c.ProjectDir, ".env")
	if fileExists(path) {
		return []string{path}
	}
	return nil
}

func (c Context) localComposeCommandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.ProjectDir, path)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DockerVisibleLocalPath translates a path inside this process into the path
// the local Docker daemon can see when the workspace is mounted through sshfs.
func DockerVisibleLocalPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return path
	}
	if translated := dockerVisibleLocalPathFromMountinfo(path, string(data)); translated != "" {
		return translated
	}
	return path
}

func dockerVisibleLocalPathFromMountinfo(path, mountinfo string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return ""
	}

	type candidate struct {
		mountPoint string
		hostRoot   string
	}
	candidates := []candidate{}
	for _, line := range strings.Split(mountinfo, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		leftFields := strings.Fields(left)
		rightFields := strings.Fields(right)
		if len(leftFields) < 5 || len(rightFields) < 2 {
			continue
		}
		if rightFields[0] != "fuse.sshfs" || !strings.HasPrefix(rightFields[1], ":/") {
			continue
		}
		root := mountinfoUnescape(leftFields[3])
		mountPoint := filepath.Clean(mountinfoUnescape(leftFields[4]))
		source := filepath.Clean(strings.TrimPrefix(mountinfoUnescape(rightFields[1]), ":"))
		hostRoot := source
		if root != "/" && root != "." {
			hostRoot = filepath.Join(source, strings.TrimPrefix(root, "/"))
		}
		candidates = append(candidates, candidate{mountPoint: mountPoint, hostRoot: hostRoot})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return len(candidates[i].mountPoint) > len(candidates[j].mountPoint)
	})
	for _, candidate := range candidates {
		if path != candidate.mountPoint && !strings.HasPrefix(path, candidate.mountPoint+string(filepath.Separator)) {
			continue
		}
		rel, err := filepath.Rel(candidate.mountPoint, path)
		if err != nil || rel == "." {
			return candidate.hostRoot
		}
		return filepath.Join(candidate.hostRoot, rel)
	}
	return ""
}

func mountinfoUnescape(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}
