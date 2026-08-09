package lifecycle

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
)

var resolveLocalComposeHostNumericIdentity = config.LocalComposeHostNumericIdentity

// CommandList is a constrained &&/|| list of direct argv commands.
type CommandList struct {
	Commands  []string
	Operators []string
}

// Parse accepts direct commands and logical-list operators. Other shell
// grammar is rejected so substantive programs must live in checked-in files.
func Parse(commandText string) (CommandList, error) {
	commandText = strings.TrimSpace(commandText)
	if commandText == "" {
		return CommandList{}, fmt.Errorf("lifecycle command cannot be empty")
	}
	if strings.ContainsAny(commandText, "\r\n\x00") {
		return CommandList{}, fmt.Errorf("lifecycle command cannot contain line breaks or NUL")
	}

	list := CommandList{}
	quote := byte(0)
	escaped := false
	segmentStart := 0
	for index := 0; index < len(commandText); index++ {
		value := commandText[index]
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
		if index+1 < len(commandText) {
			operator := commandText[index : index+2]
			if operator == "&&" || operator == "||" {
				segment := strings.TrimSpace(commandText[segmentStart:index])
				if segment == "" {
					return CommandList{}, fmt.Errorf("lifecycle command has an empty %s operand", operator)
				}
				list.Commands = append(list.Commands, segment)
				list.Operators = append(list.Operators, operator)
				index++
				segmentStart = index + 1
				continue
			}
		}
		if strings.ContainsRune(";&|<>", rune(value)) || value == '`' {
			return CommandList{}, fmt.Errorf("unsupported shell operator %q; move the program into a checked-in script", string(value))
		}
	}
	if quote != 0 || escaped {
		return CommandList{}, fmt.Errorf("lifecycle command has an unterminated quote or escape")
	}
	segment := strings.TrimSpace(commandText[segmentStart:])
	if segment == "" {
		return CommandList{}, fmt.Errorf("lifecycle command has an empty trailing operand")
	}
	list.Commands = append(list.Commands, segment)
	return list, nil
}

// Argv parses one command segment and injects context-owned Compose options.
func Argv(ctx *config.Context, commandText string) ([]string, bool, error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("context cannot be nil")
	}
	return ArgvInProject(ctx, ctx.ProjectDir, commandText)
}

// ArgvInProject parses one command segment using an explicit Compose project
// directory. It resolves the supported $PWD volume source from that directory
// in Go, without relying on shell expansion.
func ArgvInProject(ctx *config.Context, projectDir, commandText string) ([]string, bool, error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("context cannot be nil")
	}
	fields, err := shellquote.Split(strings.TrimSpace(commandText))
	if err != nil {
		return nil, false, err
	}
	if len(fields) == 0 {
		return nil, false, fmt.Errorf("command cannot be empty")
	}
	if err := validateLifecycleExpansions(fields); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(projectDir) == "" {
		projectDir = ctx.ProjectDir
	}
	effectiveContext := *ctx
	effectiveContext.ProjectDir = projectDir
	fields, err = expandProjectDirectory(fields, projectDir)
	if err != nil {
		return nil, false, err
	}
	fields, err = expandLocalHostIdentity(&effectiveContext, fields)
	if err != nil {
		return nil, false, err
	}
	if err := validateInterpreterPrograms(fields); err != nil {
		return nil, false, err
	}
	for _, field := range fields {
		if strings.ContainsRune(field, '\x00') {
			return nil, false, fmt.Errorf("command arguments cannot contain NUL")
		}
		if strings.ContainsRune(field, '`') {
			return nil, false, fmt.Errorf("shell expansion is not supported; resolve values in sitectl or a checked-in script")
		}
	}

	if fields[0] != "docker" {
		return fields, isMakeUp(fields), nil
	}
	dockerCommand, err := dockerCommandIndex(fields)
	if err != nil {
		return nil, false, err
	}
	if dockerCommand < 0 || fields[dockerCommand] != "compose" {
		return fields, isMakeUp(fields), nil
	}
	subcommandIndex := composeSubcommandIndex(fields, dockerCommand+1)
	if subcommandIndex < 0 {
		return nil, false, fmt.Errorf("docker compose command does not contain a subcommand")
	}
	subcommand := fields[subcommandIndex]
	tail := append([]string{}, fields[dockerCommand+1:subcommandIndex]...)
	tail = append(tail, effectiveContext.DockerComposeSubcommandArgs(fields[subcommandIndex:])...)
	argv := append([]string{}, fields[:dockerCommand+1]...)
	argv = append(argv, effectiveContext.DockerComposeGlobalArgsForCommand(subcommand)...)
	argv = append(argv, tail...)
	return argv, subcommand == "up", nil
}

func validateLifecycleExpansions(fields []string) error {
	for _, field := range fields {
		for index := 0; index < len(field); {
			relative := strings.IndexByte(field[index:], '$')
			if relative < 0 {
				break
			}
			index += relative
			remainder := field[index:]
			switch {
			case strings.HasPrefix(remainder, "${PWD}"):
				index += len("${PWD}")
			case strings.HasPrefix(remainder, "$(id -u)"):
				index += len("$(id -u)")
			case strings.HasPrefix(remainder, "$(id -g)"):
				index += len("$(id -g)")
			case strings.HasPrefix(remainder, "$PWD") && lifecycleExpansionBoundary(remainder[len("$PWD"):]):
				index += len("$PWD")
			default:
				return fmt.Errorf("shell expansion is not supported; resolve values in sitectl or a checked-in script")
			}
		}
	}
	return nil
}

func lifecycleExpansionBoundary(remainder string) bool {
	if remainder == "" {
		return true
	}
	value := remainder[0]
	return !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_')
}

// ProjectScriptPath resolves a direct project script invocation without
// looking it up through a shell. Empty results mean argv invokes a normal PATH
// executable or a command inside a Compose container rather than a host script.
func ProjectScriptPath(projectDir string, argv []string) (string, string, error) {
	script := hostScriptArgument(argv)
	if script == "" {
		return "", "", nil
	}
	// Lifecycle metadata can target a POSIX remote from a Windows client (and
	// vice versa). Require portable forward-slash relative paths so validation
	// and execution cannot interpret the same script argument differently.
	if strings.ContainsRune(script, '\\') {
		return script, "", fmt.Errorf("host lifecycle script %q must use a portable forward-slash project-relative path", script)
	}
	clean := path.Clean(script)
	if path.IsAbs(clean) || windowsDrivePath(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return script, "", fmt.Errorf("host lifecycle script %q must be a checked-in project-relative path", script)
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return script, "", fmt.Errorf("project directory is required by host lifecycle script %q", script)
	}
	return script, filepath.Join(projectDir, filepath.FromSlash(clean)), nil
}

func windowsDrivePath(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	letter := value[0]
	return (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')
}

func hostScriptArgument(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if script, recognized, err := interpreterProgramArgument(executableBase(argv[0]), argv[1:]); recognized && err == nil {
		return script
	}
	switch executableBase(argv[0]) {
	case "env":
		return hostScriptArgument(envCommandInvocation(argv[1:]))
	case "command", "nohup":
		index := 1
		for index < len(argv) && strings.HasPrefix(argv[index], "-") {
			index++
		}
		return hostScriptArgument(argv[index:])
	case "busybox":
		return hostScriptArgument(argv[1:])
	}
	if !filepath.IsAbs(argv[0]) && strings.ContainsAny(argv[0], `/\`) {
		return argv[0]
	}
	return ""
}

func expandProjectDirectory(fields []string, projectDir string) ([]string, error) {
	needsProjectDir := false
	for _, field := range fields {
		needsProjectDir = needsProjectDir || strings.Contains(field, "$PWD") || strings.Contains(field, "${PWD}")
	}
	if !needsProjectDir {
		return fields, nil
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("project directory is required by lifecycle command")
	}
	out := append([]string{}, fields...)
	for index, field := range out {
		field = strings.ReplaceAll(field, "${PWD}", projectDir)
		out[index] = strings.ReplaceAll(field, "$PWD", projectDir)
	}
	return out, nil
}

func isMakeUp(fields []string) bool {
	if len(fields) == 0 || fields[0] != "make" {
		return false
	}
	for _, field := range fields[1:] {
		if field == "up" {
			return true
		}
	}
	return false
}

// ExpandHostIdentity replaces the only supported lifecycle substitutions with
// values resolved by the caller for the target host.
func ExpandHostIdentity(commandText, uid, gid string) (string, error) {
	needsUID := strings.Contains(commandText, "$(id -u)")
	needsGID := strings.Contains(commandText, "$(id -g)")
	if needsUID && strings.TrimSpace(uid) == "" {
		return "", fmt.Errorf("host uid is required by lifecycle command")
	}
	if needsGID && strings.TrimSpace(gid) == "" {
		return "", fmt.Errorf("host gid is required by lifecycle command")
	}
	commandText = strings.ReplaceAll(commandText, "$(id -u)", strings.TrimSpace(uid))
	commandText = strings.ReplaceAll(commandText, "$(id -g)", strings.TrimSpace(gid))
	return commandText, nil
}

func expandLocalHostIdentity(ctx *config.Context, fields []string) ([]string, error) {
	needsUser := false
	needsGroup := false
	for _, field := range fields {
		needsUser = needsUser || strings.Contains(field, "$(id -u)")
		needsGroup = needsGroup || strings.Contains(field, "$(id -g)")
	}
	if !needsUser && !needsGroup {
		return fields, nil
	}
	if ctx.DockerHostType == config.ContextRemote {
		return nil, fmt.Errorf("remote host identity must be resolved before parsing lifecycle argv")
	}
	uid, gid, available, err := resolveLocalComposeHostNumericIdentity()
	if err != nil {
		return nil, err
	}
	if !available {
		uid, gid = "0", "0"
	}
	return expandFieldsHostIdentity(fields, uid, gid)
}

func expandFieldsHostIdentity(fields []string, uid, gid string) ([]string, error) {
	out := append([]string{}, fields...)
	for index, field := range out {
		expanded, err := ExpandHostIdentity(field, uid, gid)
		if err != nil {
			return nil, err
		}
		out[index] = expanded
	}
	return out, nil
}

func composeSubcommandIndex(fields []string, start int) int {
	for index := start; index < len(fields); index++ {
		field := fields[index]
		if !strings.HasPrefix(field, "-") {
			return index
		}
		if composeGlobalOptionTakesValue(field) && !strings.Contains(field, "=") {
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

func validateInterpreterPrograms(fields []string) error {
	if err := validateProgramInvocation(fields); err != nil {
		return fmt.Errorf("inline interpreter programs are not supported; invoke a checked-in script instead: %w", err)
	}
	return nil
}

func validateProgramInvocation(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	program := executableBase(argv[0])
	if _, recognized, err := interpreterProgramArgument(program, argv[1:]); recognized {
		return err
	}
	switch program {
	case "drush":
		if laterArgumentIs(argv, 0, "php:eval", "php-eval", "ev") {
			return fmt.Errorf("drush eval commands are not supported")
		}
	case "wp":
		if laterArgumentIs(argv, 0, "eval") {
			return fmt.Errorf("wp eval commands are not supported")
		}
	case "env":
		for _, argument := range argv[1:] {
			if argument == "-S" || argument == "--split-string" || strings.HasPrefix(argument, "--split-string=") {
				return fmt.Errorf("env split-string programs are not supported")
			}
		}
		if nested := envCommandInvocation(argv[1:]); len(nested) > 0 {
			return validateProgramInvocation(nested)
		}
	case "busybox":
		if len(argv) > 1 {
			return validateProgramInvocation(argv[1:])
		}
	case "command", "nohup":
		index := 1
		for index < len(argv) && strings.HasPrefix(argv[index], "-") {
			index++
		}
		if index < len(argv) {
			return validateProgramInvocation(argv[index:])
		}
	case "docker":
		nested, err := dockerNestedCommandInvocation(argv)
		if err != nil {
			return err
		}
		if len(nested) > 0 {
			return validateProgramInvocation(nested)
		}
	case "docker-compose":
		nested, err := composeNestedCommandInvocation(argv[1:])
		if err != nil {
			return err
		}
		if len(nested) > 0 {
			return validateProgramInvocation(nested)
		}
	}
	return nil
}

func envCommandInvocation(arguments []string) []string {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return arguments[index+1:]
		}
		if argument == "-u" || argument == "--unset" || argument == "-C" || argument == "--chdir" {
			index++
			continue
		}
		if strings.HasPrefix(argument, "-") || isEnvironmentAssignment(argument) {
			continue
		}
		return arguments[index:]
	}
	return nil
}

func isEnvironmentAssignment(argument string) bool {
	name, _, ok := strings.Cut(argument, "=")
	if !ok || name == "" {
		return false
	}
	for index, value := range name {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || value == '_' || (index > 0 && value >= '0' && value <= '9') {
			continue
		}
		return false
	}
	return true
}

func dockerNestedCommandInvocation(argv []string) ([]string, error) {
	if len(argv) < 2 {
		return nil, nil
	}
	commandIndex, err := dockerCommandIndex(argv)
	if err != nil || commandIndex < 0 {
		return nil, err
	}
	switch argv[commandIndex] {
	case "compose":
		return composeNestedCommandInvocation(argv[commandIndex+1:])
	case "exec":
		return dockerExecCommandInvocation(argv[commandIndex+1:])
	case "run":
		return dockerRunCommandInvocation(argv[commandIndex+1:])
	case "container":
		if commandIndex+1 >= len(argv) {
			return nil, nil
		}
		switch argv[commandIndex+1] {
		case "exec":
			return dockerExecCommandInvocation(argv[commandIndex+2:])
		case "run":
			return dockerRunCommandInvocation(argv[commandIndex+2:])
		default:
			return nil, nil
		}
	default:
		return nil, nil
	}
}

func composeNestedCommandInvocation(arguments []string) ([]string, error) {
	subcommandIndex, err := composeNestedSubcommandIndex(arguments, 0)
	if err != nil {
		return nil, err
	}
	if subcommandIndex < 0 || (arguments[subcommandIndex] != "exec" && arguments[subcommandIndex] != "run") {
		return nil, nil
	}
	return composeContainerCommandInvocation(arguments[subcommandIndex], arguments[subcommandIndex+1:])
}

func dockerCommandIndex(argv []string) (int, error) {
	index := 1
	for index < len(argv) {
		argument := argv[index]
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") {
			break
		}
		next, err := consumeDockerOption(argv, index, dockerGlobalOptionTakesValue, dockerGlobalBooleanOption)
		if err != nil {
			return -1, fmt.Errorf("parse docker global options: %w", err)
		}
		index = next
	}
	if index >= len(argv) {
		return -1, nil
	}
	return index, nil
}

func composeNestedSubcommandIndex(argv []string, start int) (int, error) {
	index := start
	for index < len(argv) {
		argument := argv[index]
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") {
			return index, nil
		}
		next, err := consumeDockerOption(argv, index, composeGlobalOptionTakesValue, composeGlobalBooleanOption)
		if err != nil {
			return -1, fmt.Errorf("parse docker compose global options: %w", err)
		}
		index = next
	}
	if index < len(argv) {
		return index, nil
	}
	return -1, nil
}

func composeContainerCommandInvocation(subcommand string, arguments []string) ([]string, error) {
	entrypoint := ""
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") {
			break
		}
		if strings.HasPrefix(argument, "--entrypoint=") {
			entrypoint = strings.TrimPrefix(argument, "--entrypoint=")
		} else if argument == "--entrypoint" && index+1 < len(arguments) {
			index++
			entrypoint = arguments[index]
		} else {
			next, err := consumeDockerOption(arguments, index, composeExecRunOptionTakesValue, composeExecRunBooleanOption)
			if err != nil {
				return nil, fmt.Errorf("parse docker compose %s options: %w", subcommand, err)
			}
			index = next
			continue
		}
		index++
	}
	// Skip the Compose service name.
	if index >= len(arguments) {
		return nil, nil
	}
	index++
	if entrypoint != "" {
		if err := validateDockerEntrypoint(entrypoint); err != nil {
			return nil, err
		}
		return append([]string{entrypoint}, arguments[index:]...), nil
	}
	if index >= len(arguments) {
		return nil, nil
	}
	if strings.HasPrefix(arguments[index], "-") {
		return nil, fmt.Errorf("docker compose %s command begins with option %q but the service entrypoint is unknown", subcommand, arguments[index])
	}
	return arguments[index:], nil
}

func dockerExecCommandInvocation(arguments []string) ([]string, error) {
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") {
			break
		}
		next, err := consumeDockerOption(arguments, index, dockerExecOptionTakesValue, dockerExecBooleanOption)
		if err != nil {
			return nil, fmt.Errorf("parse docker exec options: %w", err)
		}
		index = next
	}
	// Skip the container name.
	if index+1 >= len(arguments) {
		return nil, nil
	}
	return arguments[index+1:], nil
}

func dockerRunCommandInvocation(arguments []string) ([]string, error) {
	entrypoint := ""
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") {
			break
		}
		if argument == "--health-cmd" || strings.HasPrefix(argument, "--health-cmd=") {
			return nil, fmt.Errorf("docker run --health-cmd inline programs are not supported")
		}
		if strings.HasPrefix(argument, "--entrypoint=") {
			entrypoint = strings.TrimPrefix(argument, "--entrypoint=")
			index++
			continue
		}
		if argument == "--entrypoint" {
			if index+1 >= len(arguments) {
				return nil, fmt.Errorf("docker option %s requires a value", argument)
			}
			entrypoint = arguments[index+1]
			index += 2
			continue
		}
		next, err := consumeDockerOption(arguments, index, dockerRunOptionTakesValue, dockerRunBooleanOption)
		if err != nil {
			return nil, fmt.Errorf("parse docker run options: %w", err)
		}
		index = next
	}
	// Skip the image reference.
	if index >= len(arguments) {
		return nil, nil
	}
	index++
	if entrypoint != "" {
		if err := validateDockerEntrypoint(entrypoint); err != nil {
			return nil, err
		}
		return append([]string{entrypoint}, arguments[index:]...), nil
	}
	if index >= len(arguments) {
		return nil, nil
	}
	if strings.HasPrefix(arguments[index], "-") {
		return nil, fmt.Errorf("docker run command begins with option %q but the image entrypoint is unknown", arguments[index])
	}
	return arguments[index:], nil
}

func validateDockerEntrypoint(entrypoint string) error {
	if strings.TrimSpace(entrypoint) == "" || strings.ContainsAny(entrypoint, " \t\r\n") || strings.HasPrefix(entrypoint, "-") {
		return fmt.Errorf("docker entrypoint %q must be one direct executable", entrypoint)
	}
	return nil
}

func composeExecRunOptionTakesValue(option string) bool {
	switch option {
	case "--cap-add", "--cap-drop", "--env", "-e", "--env-file", "--env-from-file", "--expose", "--index", "--label", "-l", "--name", "--publish", "-p", "--pull", "--user", "-u", "--volume", "-v", "--workdir", "-w":
		return true
	default:
		return false
	}
}

func composeExecRunBooleanOption(option string) bool {
	switch option {
	case "--build", "--detach", "-d", "--dry-run", "--interactive", "-i", "--no-deps", "--no-TTY", "-T", "--privileged", "--publish-all", "-P", "--quiet", "--quiet-build", "--quiet-pull", "--remove-orphans", "--rm", "--service-ports", "--tty", "-t", "--use-aliases", "--watch":
		return true
	default:
		return false
	}
}

func dockerExecOptionTakesValue(option string) bool {
	switch option {
	case "--detach-keys", "--env", "-e", "--env-file", "--user", "-u", "--workdir", "-w":
		return true
	default:
		return false
	}
}

func dockerExecBooleanOption(option string) bool {
	switch option {
	case "--detach", "-d", "--interactive", "-i", "--privileged", "--tty", "-t":
		return true
	default:
		return false
	}
}

func dockerGlobalOptionTakesValue(option string) bool {
	switch option {
	case "--config", "--context", "-c", "--host", "-H", "--log-level", "-l", "--tlscacert", "--tlscert", "--tlskey":
		return true
	default:
		return false
	}
}

func dockerGlobalBooleanOption(option string) bool {
	switch option {
	case "--debug", "-D", "--help", "--tls", "--tlsverify", "--version", "-v":
		return true
	default:
		return false
	}
}

func composeGlobalBooleanOption(option string) bool {
	switch option {
	case "--all-resources", "--compatibility", "--dry-run", "--help", "--verbose", "--version":
		return true
	default:
		return false
	}
}

func dockerRunOptionTakesValue(option string) bool {
	switch option {
	case "--add-host", "--annotation", "--attach", "-a", "--blkio-weight", "--blkio-weight-device", "--cap-add", "--cap-drop", "--cgroup-parent", "--cgroupns", "--cidfile", "--cpu-period", "--cpu-quota", "--cpu-rt-period", "--cpu-rt-runtime", "--cpu-shares", "-c", "--cpus", "--cpuset-cpus", "--cpuset-mems", "--device", "--device-cgroup-rule", "--device-read-bps", "--device-read-iops", "--device-write-bps", "--device-write-iops", "--dns", "--dns-option", "--dns-search", "--domainname", "--env", "-e", "--env-file", "--expose", "--gpus", "--group-add", "--health-interval", "--health-retries", "--health-start-interval", "--health-start-period", "--health-timeout", "--hostname", "-h", "--init-path", "--ip", "--ip6", "--ipc", "--isolation", "--kernel-memory", "--label", "-l", "--label-file", "--link", "--link-local-ip", "--log-driver", "--log-opt", "--mac-address", "--memory", "-m", "--memory-reservation", "--memory-swap", "--memory-swappiness", "--mount", "--name", "--network", "--network-alias", "--oom-score-adj", "--pid", "--pids-limit", "--platform", "--publish", "-p", "--pull", "--restart", "--runtime", "--security-opt", "--shm-size", "--stop-signal", "--stop-timeout", "--storage-opt", "--sysctl", "--tmpfs", "--ulimit", "--user", "-u", "--userns", "--uts", "--volume", "-v", "--volume-driver", "--volumes-from", "--workdir", "-w":
		return true
	default:
		return false
	}
}

func dockerRunBooleanOption(option string) bool {
	switch option {
	case "--detach", "-d", "--disable-content-trust", "--help", "--init", "--interactive", "-i", "--no-healthcheck", "--oom-kill-disable", "--privileged", "--publish-all", "-P", "--quiet", "-q", "--read-only", "--rm", "--sig-proxy", "--tty", "-t":
		return true
	default:
		return false
	}
}

func consumeDockerOption(arguments []string, index int, takesValue, boolean func(string) bool) (int, error) {
	argument := arguments[index]
	name := argument
	if before, _, ok := strings.Cut(argument, "="); ok {
		name = before
		if takesValue(name) || boolean(name) {
			return index + 1, nil
		}
		return index, fmt.Errorf("unsupported option %q", argument)
	}
	if takesValue(name) {
		if index+1 >= len(arguments) {
			return index, fmt.Errorf("docker option %s requires a value", argument)
		}
		return index + 2, nil
	}
	// Docker accepts attached values for its single-letter value options (for
	// example -Hssh://host and -eNAME=value). Treat those as one argument.
	if len(argument) > 2 && strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") && takesValue(argument[:2]) {
		return index + 1, nil
	}
	if boolean(name) {
		return index + 1, nil
	}
	return index, fmt.Errorf("unsupported option %q", argument)
}

func executableBase(value string) string {
	// filepath.Base follows the host OS. Lifecycle commands can contain paths
	// for either the host or a Linux container, so normalize both separators.
	value = strings.ReplaceAll(value, `\`, "/")
	if separator := strings.LastIndexByte(value, '/'); separator >= 0 {
		value = value[separator+1:]
	}
	return strings.TrimSuffix(strings.ToLower(value), ".exe")
}

func interpreterProgramArgument(program string, arguments []string) (string, bool, error) {
	switch {
	case isShellExecutable(program):
		return shellProgramArgument(program, arguments)
	case isVersionedExecutable(program, "python"):
		return pythonProgramArgument(program, arguments)
	case isVersionedExecutable(program, "php"):
		return phpProgramArgument(program, arguments)
	case isVersionedExecutable(program, "node"), program == "nodejs":
		return nodeProgramArgument(program, arguments)
	case isVersionedExecutable(program, "perl"):
		return perlProgramArgument(program, arguments)
	case isVersionedExecutable(program, "ruby"):
		return rubyProgramArgument(program, arguments)
	case program == "awk", program == "gawk", program == "mawk", program == "nawk":
		return awkProgramArgument(program, arguments)
	case program == "sed", program == "gsed":
		return sedProgramArgument(program, arguments)
	default:
		return "", false, nil
	}
}

func shellProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) || argument == "--command" || strings.HasPrefix(argument, "--command=") ||
			argument == "--interactive" || argument == "--stdin" {
			return "", true, fmt.Errorf("%s cannot read a program from stdin or an option", program)
		}
		if strings.HasPrefix(argument, "--") {
			if argument == "--rcfile" || argument == "--init-file" || strings.HasPrefix(argument, "--rcfile=") || strings.HasPrefix(argument, "--init-file=") {
				return "", true, fmt.Errorf("%s startup programs are not supported", program)
			}
			continue
		}
		flags := strings.TrimLeft(argument, "-")
		if strings.HasPrefix(flags, "o") || strings.HasPrefix(flags, "O") {
			if len(flags) == 1 && index+1 < len(arguments) {
				index++
			}
			continue
		}
		if strings.ContainsAny(flags, "cis") {
			return "", true, fmt.Errorf("%s cannot use command, interactive, or stdin mode", program)
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in script argument", program)
}

func pythonProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) || argument == "-i" || argument == "--interactive" ||
			strings.HasPrefix(argument, "-c") || strings.HasPrefix(argument, "-m") {
			return "", true, fmt.Errorf("%s cannot use stdin, interactive, command, or module mode", program)
		}
		if (argument == "-W" || argument == "-X" || argument == "--check-hash-based-pycs") && index+1 < len(arguments) {
			index++
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in program file", program)
}

func phpProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) || phpInlineMode(argument) {
			return "", true, fmt.Errorf("%s cannot use stdin, interactive, or inline processing mode", program)
		}
		if argument == "-f" || argument == "--file" || argument == "-F" || argument == "--process-file" {
			if index+1 >= len(arguments) {
				return "", true, fmt.Errorf("%s option %s requires a checked-in program file", program, argument)
			}
			return checkedProgramArgument(program, arguments[index+1])
		}
		if strings.HasPrefix(argument, "--file=") || strings.HasPrefix(argument, "--process-file=") {
			return checkedProgramArgument(program, strings.SplitN(argument, "=", 2)[1])
		}
		if (argument == "-c" || argument == "-d" || argument == "-z" || argument == "--php-ini" || argument == "--define" || argument == "--zend-extension") && index+1 < len(arguments) {
			index++
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in program file", program)
}

func phpInlineMode(argument string) bool {
	for _, option := range []string{"-r", "-B", "-R", "-E", "-a", "--run", "--process-begin", "--process-code", "--process-end", "--interactive"} {
		if argument == option || strings.HasPrefix(argument, option+"=") || (strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.HasPrefix(argument, option) && len(argument) > len(option)) {
			return true
		}
	}
	return false
}

func nodeProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) || argument == "-i" || argument == "--interactive" ||
			strings.HasPrefix(argument, "-e") || strings.HasPrefix(argument, "-p") ||
			argument == "--eval" || strings.HasPrefix(argument, "--eval=") || argument == "--print" || strings.HasPrefix(argument, "--print=") {
			return "", true, fmt.Errorf("%s cannot use stdin, interactive, eval, or print mode", program)
		}
		for _, hook := range []string{"-r", "--require", "--import", "--loader", "--experimental-loader"} {
			if argument == hook || strings.HasPrefix(argument, hook+"=") {
				return "", true, fmt.Errorf("%s code-loading option %s is not supported", program, hook)
			}
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in program file", program)
}

func perlProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) {
			return "", true, fmt.Errorf("%s cannot read a program from stdin", program)
		}
		flags := strings.TrimLeft(argument, "-")
		if strings.ContainsAny(flags, "eEd") || strings.HasPrefix(flags, "M") || strings.HasPrefix(flags, "m") {
			return "", true, fmt.Errorf("%s inline, debugger, and module-loading modes are not supported", program)
		}
		if flags == "I" && index+1 < len(arguments) {
			index++
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in program file", program)
}

func rubyProgramArgument(program string, arguments []string) (string, bool, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return requiredProgramArgument(program, arguments[index+1:])
		}
		if !strings.HasPrefix(argument, "-") {
			return checkedProgramArgument(program, argument)
		}
		if isStdinProgram(argument) || strings.HasPrefix(argument, "-e") || argument == "--eval" || strings.HasPrefix(argument, "--eval=") ||
			strings.HasPrefix(argument, "-r") || argument == "--require" || strings.HasPrefix(argument, "--require=") || argument == "-S" {
			return "", true, fmt.Errorf("%s stdin, eval, require, and PATH script modes are not supported", program)
		}
		if argument == "-I" && index+1 < len(arguments) {
			index++
		}
	}
	return "", true, fmt.Errorf("%s requires a checked-in program file", program)
}

func awkProgramArgument(program string, arguments []string) (string, bool, error) {
	programFile := ""
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "-e" || argument == "--source" || strings.HasPrefix(argument, "--source=") {
			return "", true, fmt.Errorf("%s inline source is not supported", program)
		}
		if argument == "-f" || argument == "--file" || argument == "-E" || argument == "--exec" {
			if index+1 >= len(arguments) {
				return "", true, fmt.Errorf("%s option %s requires a checked-in program file", program, argument)
			}
			index++
			if programFile != "" {
				return "", true, fmt.Errorf("%s must use exactly one checked-in program file", program)
			}
			programFile = arguments[index]
			continue
		}
		if strings.HasPrefix(argument, "--file=") || strings.HasPrefix(argument, "--exec=") {
			if programFile != "" {
				return "", true, fmt.Errorf("%s must use exactly one checked-in program file", program)
			}
			programFile = strings.SplitN(argument, "=", 2)[1]
			continue
		}
		if argument == "-v" && index+1 < len(arguments) {
			index++
			continue
		}
		if !strings.HasPrefix(argument, "-") && programFile == "" {
			return "", true, fmt.Errorf("%s positional inline source is not supported", program)
		}
	}
	if programFile == "" {
		return "", true, fmt.Errorf("%s requires -f with a checked-in program file", program)
	}
	return checkedProgramArgument(program, programFile)
}

func sedProgramArgument(program string, arguments []string) (string, bool, error) {
	programFile := ""
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "-e" || argument == "--expression" || strings.HasPrefix(argument, "--expression=") {
			return "", true, fmt.Errorf("%s inline expressions are not supported", program)
		}
		if argument == "-f" || argument == "--file" {
			if index+1 >= len(arguments) {
				return "", true, fmt.Errorf("%s option %s requires a checked-in program file", program, argument)
			}
			index++
			if programFile != "" {
				return "", true, fmt.Errorf("%s must use exactly one checked-in program file", program)
			}
			programFile = arguments[index]
			continue
		}
		if strings.HasPrefix(argument, "--file=") {
			if programFile != "" {
				return "", true, fmt.Errorf("%s must use exactly one checked-in program file", program)
			}
			programFile = strings.SplitN(argument, "=", 2)[1]
			continue
		}
		if !strings.HasPrefix(argument, "-") && programFile == "" {
			return "", true, fmt.Errorf("%s positional inline expression is not supported", program)
		}
	}
	if programFile == "" {
		return "", true, fmt.Errorf("%s requires -f with a checked-in program file", program)
	}
	return checkedProgramArgument(program, programFile)
}

func requiredProgramArgument(program string, arguments []string) (string, bool, error) {
	if len(arguments) == 0 {
		return "", true, fmt.Errorf("%s requires a checked-in program file", program)
	}
	return checkedProgramArgument(program, arguments[0])
}

func checkedProgramArgument(program, argument string) (string, bool, error) {
	if isStdinProgram(argument) {
		return "", true, fmt.Errorf("%s cannot read a program from stdin", program)
	}
	return argument, true, nil
}

func isStdinProgram(argument string) bool {
	switch strings.ReplaceAll(argument, `\`, "/") {
	case "-", "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0":
		return true
	default:
		return false
	}
}

func isShellExecutable(program string) bool {
	switch program {
	case "sh", "bash", "dash", "ash", "ksh", "zsh":
		return true
	default:
		return false
	}
}

func isVersionedExecutable(program, base string) bool {
	if program == base {
		return true
	}
	suffix := strings.TrimPrefix(program, base)
	if suffix == program || suffix == "" {
		return false
	}
	hasDigit := false
	for _, value := range suffix {
		switch {
		case value >= '0' && value <= '9':
			hasDigit = true
		case value == '.':
		default:
			return false
		}
	}
	return hasDigit
}

func laterArgumentIs(fields []string, programIndex int, values ...string) bool {
	for _, argument := range fields[programIndex+1:] {
		for _, value := range values {
			if argument == value || strings.HasPrefix(argument, value+"=") {
				return true
			}
		}
	}
	return false
}
