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
	dockerProjectDir := c.dockerComposeTranslatedProjectDir()
	args := []string{}
	// The translated daemon-side project directory is only valid for commands
	// whose paths are resolved by the daemon. Build contexts are read by the
	// local Compose client, where the translated host path may not exist.
	if strings.TrimSpace(command) != "build" && dockerProjectDir != "" {
		args = append(args, "--project-directory", dockerProjectDir)
	}
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

// DockerComposeShellCommand rewrites executable "docker compose ..." commands
// in a shell list so they honor the context's Compose and environment files.
// It also supplies daemon-visible project paths for local sshfs workspaces.
func (c Context) DockerComposeShellCommand(command string) string {
	separators, ok := shellCommandListSeparators(command)
	if !ok {
		return command
	}
	var rewritten strings.Builder
	segmentStart := 0
	for _, separator := range separators {
		rewritten.WriteString(c.rewriteDockerComposeShellSegment(command[segmentStart:separator.start]))
		rewritten.WriteString(command[separator.start:separator.end])
		segmentStart = separator.end
	}
	rewritten.WriteString(c.rewriteDockerComposeShellSegment(command[segmentStart:]))
	return rewritten.String()
}

type shellCommandSeparator struct {
	start int
	end   int
}

// shellCommandListSeparators finds unquoted command-list separators. A
// malformed quoted string is left completely unchanged instead of attempting
// a partial rewrite.
func shellCommandListSeparators(command string) ([]shellCommandSeparator, bool) {
	separators := []shellCommandSeparator{}
	quote := byte(0)
	escaped := false
	for index := 0; index < len(command); index++ {
		value := command[index]
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && value == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if index+1 < len(command) && (command[index:index+2] == "&&" || command[index:index+2] == "||") {
			separators = append(separators, shellCommandSeparator{start: index, end: index + 2})
			index++
			continue
		}
		if index+1 < len(command) && command[index:index+2] == "|&" {
			separators = append(separators, shellCommandSeparator{start: index, end: index + 2})
			index++
			continue
		}
		if value == ';' || value == '\n' || value == '|' {
			separators = append(separators, shellCommandSeparator{start: index, end: index + 1})
		}
	}
	return separators, quote == 0 && !escaped
}

type shellCommandWord struct {
	value string
	start int
	end   int
}

func shellCommandWords(command string) ([]shellCommandWord, bool) {
	words := []shellCommandWord{}
	wordStart := -1
	quote := byte(0)
	escaped := false
	valid := true
	appendWord := func(end int) {
		if wordStart < 0 {
			return
		}
		raw := command[wordStart:end]
		fields, err := shellquote.Split(raw)
		if err != nil || len(fields) != 1 {
			valid = false
			wordStart = -1
			return
		}
		words = append(words, shellCommandWord{value: fields[0], start: wordStart, end: end})
		wordStart = -1
	}
	for index := 0; index < len(command); index++ {
		value := command[index]
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && value == '\\' {
			if wordStart < 0 {
				wordStart = index
			}
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			if wordStart < 0 {
				wordStart = index
			}
			quote = value
			continue
		}
		// Command-list operators have already been split. Other unquoted shell
		// grammar is deliberately unsupported: rewriting only part of a
		// redirection, subshell, function, or background command is unsafe.
		if strings.ContainsRune("&<>();{}", rune(value)) {
			return nil, false
		}
		if isShellSpace(value) || value == '\r' || value == '\n' {
			appendWord(index)
			continue
		}
		if wordStart < 0 {
			wordStart = index
		}
	}
	appendWord(len(command))
	return words, valid && quote == 0 && !escaped
}

func (c Context) rewriteDockerComposeShellSegment(segment string) string {
	words, ok := shellCommandWords(segment)
	if !ok {
		return segment
	}
	dockerIndex := dockerComposeCommandWordIndex(words)
	if dockerIndex < 0 || dockerIndex+2 >= len(words) {
		return segment
	}
	subcommandIndex := dockerComposeSubcommandWordIndex(words, dockerIndex+2)
	if subcommandIndex < 0 {
		return segment
	}
	subcommand := words[subcommandIndex].value
	globalArgs := c.DockerComposeGlobalArgsForCommand(subcommand)
	addNoBuild := c.dockerComposeTranslatedProjectDir() != "" && subcommand == "up" && !hasComposeBuildOption(words[subcommandIndex+1:])
	if len(globalArgs) == 0 && !addNoBuild {
		return segment
	}

	composeEnd := words[dockerIndex+1].end
	var rewritten strings.Builder
	rewritten.WriteString(segment[:composeEnd])
	if len(globalArgs) > 0 {
		rewritten.WriteByte(' ')
		rewritten.WriteString(shellquote.Join(globalArgs...))
	}
	if addNoBuild {
		rewritten.WriteString(segment[composeEnd:words[subcommandIndex].end])
		rewritten.WriteString(" --no-build")
		rewritten.WriteString(segment[words[subcommandIndex].end:])
	} else {
		rewritten.WriteString(segment[composeEnd:])
	}
	return rewritten.String()
}

func dockerComposeCommandWordIndex(words []shellCommandWord) int {
	for index := 0; index+1 < len(words); index++ {
		if words[index].value != "docker" || words[index+1].value != "compose" {
			continue
		}
		if shellCommandPrefixAllowed(words[:index]) {
			return index
		}
	}
	return -1
}

func shellCommandPrefixAllowed(words []shellCommandWord) bool {
	index := 0
	for index < len(words) && isShellAssignment(words[index].value) {
		index++
	}
	if index == len(words) {
		return true
	}
	switch words[index].value {
	case "command", "exec":
		index++
	case "env":
		index++
		for index < len(words) && (isShellAssignment(words[index].value) || isSimpleEnvOption(words[index].value)) {
			index++
		}
	case "sudo":
		index++
		for index < len(words) && (words[index].value == "-n" || words[index].value == "--non-interactive") {
			index++
		}
	default:
		return false
	}
	return index == len(words)
}

func isShellAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return false
	}
	for index, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && char != '_' && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func isSimpleEnvOption(value string) bool {
	return value == "-i" || value == "--ignore-environment" || strings.HasPrefix(value, "--chdir=") || strings.HasPrefix(value, "--unset=")
}

func dockerComposeSubcommandWordIndex(words []shellCommandWord, start int) int {
	for index := start; index < len(words); index++ {
		option := words[index].value
		if !strings.HasPrefix(option, "-") {
			return index
		}
		if composeGlobalOptionTakesValue(option) && !strings.Contains(option, "=") {
			index++
		}
	}
	return -1
}

func composeGlobalOptionTakesValue(option string) bool {
	switch option {
	case "--ansi", "--env-file", "-f", "--file", "--parallel", "--profile", "--progress", "--project-directory", "-p", "--project-name":
		return true
	default:
		return false
	}
}

func hasComposeBuildOption(words []shellCommandWord) bool {
	for _, word := range words {
		if word.value == "--build" || strings.HasPrefix(word.value, "--build=") || word.value == "--no-build" || strings.HasPrefix(word.value, "--no-build=") {
			return true
		}
	}
	return false
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
