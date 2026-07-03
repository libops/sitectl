package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/crypto/ssh"
)

type remoteShellCommandFunc func(context.Context, *config.Context, io.Writer, io.Writer, string) (string, error)

var runRemoteCreatePrerequisitesCommand remoteShellCommandFunc = runRemoteShellCommandContext

// RemoteCreatePrerequisitesOptions controls create-time remote host changes.
type RemoteCreatePrerequisitesOptions struct {
	Yolo  bool
	Input config.InputFunc
}

type remotePrerequisiteInstaller string

const (
	remoteInstallerUnknown   remotePrerequisiteInstaller = "unknown"
	remoteInstallerApt       remotePrerequisiteInstaller = "apt"
	remoteInstallerDnf       remotePrerequisiteInstaller = "dnf"
	remoteInstallerYum       remotePrerequisiteInstaller = "yum"
	remoteInstallerRpmOstree remotePrerequisiteInstaller = "rpm-ostree"
	remoteInstallerCOS       remotePrerequisiteInstaller = "cos"
)

type remoteCreatePrerequisiteStatus struct {
	Missing   []string
	Installer remotePrerequisiteInstaller
	Distro    string
}

type remoteCreatePrerequisiteProbe struct {
	OSRelease   map[string]string
	Commands    map[string]bool
	ComposeV2   bool
	DockerReady bool
	UserID      string
}

// EnsureRemoteCreatePrerequisitesContext checks a remote create target for the
// host tools required by the standard Compose template create flow. Supported
// mutable Linux hosts can be bootstrapped after confirmation.
func (s *SDK) EnsureRemoteCreatePrerequisitesContext(runCtx context.Context, out io.Writer, ctx *config.Context, opts RemoteCreatePrerequisitesOptions) error {
	if s == nil {
		return fmt.Errorf("plugin sdk is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if ctx.DockerHostType != config.ContextRemote {
		return nil
	}
	if runCtx == nil {
		runCtx = context.Background()
	}

	status, probe, err := checkRemoteCreatePrerequisites(runCtx, ctx)
	if err != nil {
		return err
	}
	if len(status.Missing) == 0 {
		return nil
	}
	if !status.canBootstrap() {
		return status.unsupportedError(ctx)
	}
	if !probe.canRunRoot() {
		return fmt.Errorf("remote host %s is missing %s, but the SSH user is not root and sudo is unavailable", remoteHostLabel(ctx), strings.Join(status.Missing, ", "))
	}

	if !opts.Yolo {
		input := opts.Input
		if input == nil {
			input = config.GetInput
		}
		ok, err := confirmRemoteCreatePrerequisiteInstall(input, ctx, status)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("remote prerequisite installation cancelled; missing: %s", strings.Join(status.Missing, ", "))
		}
	}

	if out != nil {
		fmt.Fprintf(out, "Installing remote prerequisites on %s: %s\n", remoteHostLabel(ctx), strings.Join(status.Missing, ", "))
	}
	rebootRequired, err := installRemoteCreatePrerequisites(runCtx, out, ctx, status, probe)
	if err != nil {
		return fmt.Errorf("install remote prerequisites on %s: %w", remoteHostLabel(ctx), err)
	}
	if rebootRequired {
		return fmt.Errorf("remote prerequisites were staged on %s with rpm-ostree; reboot the host, then rerun sitectl create", remoteHostLabel(ctx))
	}

	status, _, err = checkRemoteCreatePrerequisites(runCtx, ctx)
	if err != nil {
		return err
	}
	if len(status.Missing) > 0 {
		return fmt.Errorf("remote prerequisites are still missing on %s after installation: %s", remoteHostLabel(ctx), strings.Join(status.Missing, ", "))
	}
	return nil
}

func checkRemoteCreatePrerequisites(runCtx context.Context, ctx *config.Context) (remoteCreatePrerequisiteStatus, remoteCreatePrerequisiteProbe, error) {
	probe, err := probeRemoteCreatePrerequisites(runCtx, ctx)
	if err != nil {
		return remoteCreatePrerequisiteStatus{}, remoteCreatePrerequisiteProbe{}, fmt.Errorf("check remote prerequisites on %s: %w", remoteHostLabel(ctx), err)
	}
	status := remoteCreatePrerequisiteStatusFromProbe(probe)
	return status, probe, nil
}

func probeRemoteCreatePrerequisites(runCtx context.Context, ctx *config.Context) (remoteCreatePrerequisiteProbe, error) {
	probe := remoteCreatePrerequisiteProbe{
		OSRelease: map[string]string{},
		Commands:  map[string]bool{},
	}

	osRelease, err := runRemoteCommandOptional(runCtx, ctx, nil, nil, "cat /etc/os-release")
	if err != nil {
		return probe, err
	}
	probe.OSRelease = parseOSRelease(osRelease)

	for _, command := range []string{"apt-get", "dnf", "docker", "git", "make", "rpm-ostree", "service", "sudo", "systemctl", "yum"} {
		available, err := remoteCommandSucceeds(runCtx, ctx, nil, nil, "command -v "+shellQuote(command))
		if err != nil {
			return probe, err
		}
		probe.Commands[command] = available
	}

	if probe.Commands["docker"] {
		probe.ComposeV2, err = remoteCommandSucceeds(runCtx, ctx, nil, nil, "docker compose version")
		if err != nil {
			return probe, err
		}
		probe.DockerReady, err = remoteCommandSucceeds(runCtx, ctx, nil, nil, "docker info")
		if err != nil {
			return probe, err
		}
	}

	userID, err := runRemoteCommand(runCtx, ctx, nil, nil, "id -u")
	if err != nil {
		return probe, err
	}
	probe.UserID = strings.TrimSpace(userID)
	return probe, nil
}

func remoteCreatePrerequisiteStatusFromProbe(probe remoteCreatePrerequisiteProbe) remoteCreatePrerequisiteStatus {
	status := remoteCreatePrerequisiteStatus{
		Installer: detectRemoteCreatePrerequisiteInstaller(probe),
		Distro:    remoteProbeDistroLabel(probe),
	}
	if !probe.Commands["git"] {
		status.Missing = append(status.Missing, "git")
	}
	if !probe.Commands["make"] {
		status.Missing = append(status.Missing, "make")
	}
	if !probe.Commands["docker"] {
		status.Missing = append(status.Missing, "docker")
	}
	if !probe.ComposeV2 {
		status.Missing = append(status.Missing, "docker-compose")
	}
	if probe.Commands["docker"] && !probe.DockerReady {
		status.Missing = append(status.Missing, "docker-daemon")
	}
	return status
}

func (p remoteCreatePrerequisiteProbe) canRunRoot() bool {
	return p.UserID == "0" || p.Commands["sudo"]
}

func detectRemoteCreatePrerequisiteInstaller(probe remoteCreatePrerequisiteProbe) remotePrerequisiteInstaller {
	if isGoogleContainerOptimizedOS(probe.OSRelease) {
		return remoteInstallerCOS
	}
	if probe.Commands["apt-get"] {
		return remoteInstallerApt
	}
	if probe.Commands["rpm-ostree"] && isCoreOS(probe.OSRelease) {
		return remoteInstallerRpmOstree
	}
	if probe.Commands["dnf"] {
		return remoteInstallerDnf
	}
	if probe.Commands["yum"] {
		return remoteInstallerYum
	}
	if probe.Commands["rpm-ostree"] {
		return remoteInstallerRpmOstree
	}
	return remoteInstallerUnknown
}

func installRemoteCreatePrerequisites(runCtx context.Context, out io.Writer, ctx *config.Context, status remoteCreatePrerequisiteStatus, probe remoteCreatePrerequisiteProbe) (bool, error) {
	switch status.Installer {
	case remoteInstallerApt:
		if needsRemotePackageInstall(status.Missing) {
			if err := installRemoteAptPrerequisites(runCtx, out, ctx, probe); err != nil {
				return false, err
			}
		}
		return false, startRemoteDockerIfNeeded(runCtx, out, ctx, status, probe)
	case remoteInstallerDnf:
		if needsRemotePackageInstall(status.Missing) {
			if err := installRemoteRpmPrerequisites(runCtx, out, ctx, probe, "dnf"); err != nil {
				return false, err
			}
		}
		return false, startRemoteDockerIfNeeded(runCtx, out, ctx, status, probe)
	case remoteInstallerYum:
		if needsRemotePackageInstall(status.Missing) {
			if err := installRemoteRpmPrerequisites(runCtx, out, ctx, probe, "yum"); err != nil {
				return false, err
			}
		}
		return false, startRemoteDockerIfNeeded(runCtx, out, ctx, status, probe)
	case remoteInstallerRpmOstree:
		if needsRemotePackageInstall(status.Missing) {
			if err := installRemoteRpmOstreePrerequisites(runCtx, out, ctx, probe); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, startRemoteDockerIfNeeded(runCtx, out, ctx, status, probe)
	case remoteInstallerCOS:
		return false, startRemoteDockerIfNeeded(runCtx, out, ctx, status, probe)
	default:
		return false, status.unsupportedError(ctx)
	}
}

func installRemoteAptPrerequisites(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe) error {
	if err := runRemoteRootCommandEnv(runCtx, out, ctx, probe, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update"); err != nil {
		return err
	}
	packageSets := [][]string{
		{"git", "make", "docker.io", "docker-compose-v2"},
		{"git", "make", "docker.io", "docker-compose-plugin"},
		{"git", "make", "docker.io", "docker-compose"},
	}
	return runRemotePackageInstallCandidates(runCtx, out, ctx, probe, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", packageSets)
}

func installRemoteRpmPrerequisites(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe, installer string) error {
	packageSets := [][]string{
		{"git", "make", "docker", "docker-compose-plugin"},
		{"git", "make", "moby-engine", "docker-compose-plugin"},
	}
	return runRemotePackageInstallCandidates(runCtx, out, ctx, probe, nil, installer, packageSets)
}

func installRemoteRpmOstreePrerequisites(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe) error {
	packageSets := [][]string{
		{"git", "make", "moby-engine", "docker-compose-plugin"},
		{"git", "make", "docker", "docker-compose-plugin"},
	}
	return runRemotePackageInstallCandidates(runCtx, out, ctx, probe, nil, "rpm-ostree", packageSets)
}

func runRemotePackageInstallCandidates(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe, env []string, installer string, packageSets [][]string) error {
	var lastErr error
	for _, packages := range packageSets {
		args := []string{installer}
		switch installer {
		case "apt-get", "dnf", "yum":
			args = append(args, "install", "-y")
		case "rpm-ostree":
			args = append(args, "install", "--idempotent")
		}
		args = append(args, packages...)
		err := runRemoteRootCommandEnv(runCtx, out, ctx, probe, env, args...)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func startRemoteDockerIfNeeded(runCtx context.Context, out io.Writer, ctx *config.Context, status remoteCreatePrerequisiteStatus, probe remoteCreatePrerequisiteProbe) error {
	if !needsRemoteDockerStart(status.Missing) {
		return nil
	}
	if probe.Commands["systemctl"] {
		if err := runRemoteRootCommand(runCtx, out, ctx, probe, "systemctl", "enable", "--now", "docker"); err == nil {
			return nil
		}
		if err := runRemoteRootCommand(runCtx, out, ctx, probe, "systemctl", "start", "docker"); err == nil {
			return nil
		}
	}
	if probe.Commands["service"] {
		if err := runRemoteRootCommand(runCtx, out, ctx, probe, "service", "docker", "start"); err == nil {
			return nil
		}
	}
	if probe.Commands["systemctl"] || probe.Commands["service"] {
		return nil
	}
	return fmt.Errorf("docker daemon is not running and neither systemctl nor service is available")
}

func runRemoteRootCommand(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe, args ...string) error {
	return runRemoteRootCommandEnv(runCtx, out, ctx, probe, nil, args...)
}

func runRemoteRootCommandEnv(runCtx context.Context, out io.Writer, ctx *config.Context, probe remoteCreatePrerequisiteProbe, env []string, args ...string) error {
	command, err := remoteRootCommand(probe, env, args...)
	if err != nil {
		return err
	}
	_, err = runRemoteCommand(runCtx, ctx, out, out, command)
	return err
}

func remoteRootCommand(probe remoteCreatePrerequisiteProbe, env []string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("remote command cannot be empty")
	}
	commandArgs := append([]string{}, args...)
	if len(env) > 0 {
		commandArgs = append(append([]string{"env"}, env...), commandArgs...)
	}
	if probe.UserID == "0" {
		return shellJoin(commandArgs), nil
	}
	if probe.Commands["sudo"] {
		return shellJoin(append([]string{"sudo"}, commandArgs...)), nil
	}
	return "", fmt.Errorf("remote host needs root or sudo to install/start prerequisites")
}

func runRemoteCommand(runCtx context.Context, ctx *config.Context, stdout, stderr io.Writer, command string) (string, error) {
	return runRemoteCreatePrerequisitesCommand(runCtx, ctx, stdout, stderr, command)
}

func runRemoteCommandOptional(runCtx context.Context, ctx *config.Context, stdout, stderr io.Writer, command string) (string, error) {
	output, err := runRemoteCommand(runCtx, ctx, stdout, stderr, command)
	if err == nil || isRemoteProcessExitError(err) {
		return output, nil
	}
	return output, err
}

func remoteCommandSucceeds(runCtx context.Context, ctx *config.Context, stdout, stderr io.Writer, command string) (bool, error) {
	_, err := runRemoteCommand(runCtx, ctx, stdout, stderr, command)
	if err == nil {
		return true, nil
	}
	if isRemoteProcessExitError(err) {
		return false, nil
	}
	return false, err
}

func isRemoteProcessExitError(err error) bool {
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	var missingErr *ssh.ExitMissingError
	return errors.As(err, &missingErr)
}

func needsRemotePackageInstall(missing []string) bool {
	for _, item := range missing {
		if item != "docker-daemon" {
			return true
		}
	}
	return false
}

func needsRemoteDockerStart(missing []string) bool {
	for _, item := range missing {
		switch item {
		case "docker", "docker-compose", "docker-daemon":
			return true
		}
	}
	return false
}

func confirmRemoteCreatePrerequisiteInstall(input config.InputFunc, ctx *config.Context, status remoteCreatePrerequisiteStatus) (bool, error) {
	lines := []string{
		fmt.Sprintf("Host: %s", remoteHostLabel(ctx)),
		fmt.Sprintf("Missing: %s", strings.Join(status.Missing, ", ")),
		fmt.Sprintf("Installer: %s", status.installerLabel()),
	}
	if strings.TrimSpace(status.Distro) != "" {
		lines = append(lines, fmt.Sprintf("Detected OS: %s", strings.TrimSpace(status.Distro)))
	}
	question := []string{
		corecomponent.RenderSection("Remote prerequisites", strings.Join(lines, "\n")),
		"",
		corecomponent.RenderPromptLine("Install/start the missing remote prerequisites now? [y/N]: "),
	}
	answer, err := input(question...)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func parseOSRelease(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		} else {
			value = strings.Trim(value, `"'`)
		}
		values[key] = value
	}
	return values
}

func isGoogleContainerOptimizedOS(values map[string]string) bool {
	if strings.EqualFold(values["ID"], "cos") {
		return true
	}
	return strings.Contains(strings.ToLower(osReleaseIdentity(values)), "container-optimized os")
}

func isCoreOS(values map[string]string) bool {
	identity := strings.ToLower(osReleaseIdentity(values))
	return strings.Contains(identity, "coreos")
}

func osReleaseIdentity(values map[string]string) string {
	return strings.Join([]string{
		values["ID"],
		values["ID_LIKE"],
		values["NAME"],
		values["PRETTY_NAME"],
		values["VARIANT_ID"],
		values["BUILD_ID"],
	}, " ")
}

func remoteProbeDistroLabel(probe remoteCreatePrerequisiteProbe) string {
	for _, key := range []string{"PRETTY_NAME", "NAME", "ID"} {
		if value := strings.TrimSpace(probe.OSRelease[key]); value != "" {
			return value
		}
	}
	return ""
}

func (s remoteCreatePrerequisiteStatus) canBootstrap() bool {
	switch s.Installer {
	case remoteInstallerApt, remoteInstallerDnf, remoteInstallerYum, remoteInstallerRpmOstree:
		return true
	case remoteInstallerCOS:
		return len(s.Missing) == 1 && s.Missing[0] == "docker-daemon"
	default:
		return false
	}
}

func (s remoteCreatePrerequisiteStatus) unsupportedError(ctx *config.Context) error {
	host := remoteHostLabel(ctx)
	missing := strings.Join(s.Missing, ", ")
	switch s.Installer {
	case remoteInstallerCOS:
		return fmt.Errorf("remote host %s appears to be Google Container-Optimized OS and is missing %s; COS has no host package manager, so provision these tools before rerunning sitectl create or use an Ubuntu/Debian remote host", host, missing)
	default:
		if s.Installer == remoteInstallerUnknown || strings.TrimSpace(string(s.Installer)) == "" {
			return fmt.Errorf("remote host %s is missing %s and sitectl could not detect a supported installer", host, missing)
		}
		return fmt.Errorf("remote host %s is missing %s and installer %q is not supported by sitectl create", host, missing, s.Installer)
	}
}

func (s remoteCreatePrerequisiteStatus) installerLabel() string {
	switch s.Installer {
	case remoteInstallerApt:
		return "apt-get"
	case remoteInstallerDnf:
		return "dnf"
	case remoteInstallerYum:
		return "yum"
	case remoteInstallerRpmOstree:
		return "rpm-ostree (reboot required)"
	case remoteInstallerCOS:
		return "Google Container-Optimized OS service start only"
	default:
		return strings.TrimSpace(string(s.Installer))
	}
}

func remoteHostLabel(ctx *config.Context) string {
	if ctx == nil {
		return "remote host"
	}
	if strings.TrimSpace(ctx.SSHHostname) != "" {
		return strings.TrimSpace(ctx.SSHHostname)
	}
	if strings.TrimSpace(ctx.Name) != "" {
		return strings.TrimSpace(ctx.Name)
	}
	return "remote host"
}
