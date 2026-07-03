package traefik

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

type ingressHostCommandRunnerFunc func(context.Context, *config.Context, []string) (string, error)
type ingressMkcertPrerequisiteFunc func(context.Context, *config.Context, corecomponent.ApplyOptions) error

var (
	ingressRunHostCommand            ingressHostCommandRunnerFunc  = runIngressHostCommand
	ingressEnsureMkcertPrerequisites ingressMkcertPrerequisiteFunc = ensureIngressMkcertPrerequisites
)

type ingressPackageInstaller string

const (
	ingressInstallerUnknown   ingressPackageInstaller = "unknown"
	ingressInstallerApt       ingressPackageInstaller = "apt"
	ingressInstallerDnf       ingressPackageInstaller = "dnf"
	ingressInstallerYum       ingressPackageInstaller = "yum"
	ingressInstallerRpmOstree ingressPackageInstaller = "rpm-ostree"
	ingressInstallerCOS       ingressPackageInstaller = "cos"
)

type ingressPackageProbe struct {
	OSRelease map[string]string
	Commands  map[string]bool
	UserID    string
}

func ensureIngressMkcertPrerequisites(runCtx context.Context, ctx *config.Context, opts corecomponent.ApplyOptions) error {
	if commandAvailable(runCtx, ctx, "mkcert") {
		return nil
	}

	probe := probeIngressPackageHost(runCtx, ctx)
	installer := detectIngressPackageInstaller(probe)
	if installer == ingressInstallerCOS {
		return fmt.Errorf("mkcert is missing on %s; Google Container-Optimized OS has no host package manager, so install mkcert before using --%s %s or use an Ubuntu/Debian remote host", ingressHostLabel(ctx), ingressModeName, IngressModeHTTPSMkcert)
	}
	if installer == ingressInstallerUnknown {
		return fmt.Errorf("mkcert is missing on %s and sitectl could not detect a supported package installer", ingressHostLabel(ctx))
	}
	if !probe.canRunRoot() {
		return fmt.Errorf("mkcert is missing on %s, but the user is not root and sudo is unavailable", ingressHostLabel(ctx))
	}

	if !opts.Yolo && !opts.AutoApprove {
		ok, err := confirmIngressPackageInstall(opts, ctx, installer, probe)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("mkcert installation cancelled")
		}
	}

	rebootRequired, err := installIngressMkcert(runCtx, ctx, installer, probe)
	if err != nil {
		return fmt.Errorf("install mkcert on %s: %w", ingressHostLabel(ctx), err)
	}
	if rebootRequired {
		return fmt.Errorf("mkcert was staged on %s with rpm-ostree; reboot the host, then rerun the ingress update", ingressHostLabel(ctx))
	}
	if !commandAvailable(runCtx, ctx, "mkcert") {
		return fmt.Errorf("mkcert is still missing on %s after installation", ingressHostLabel(ctx))
	}
	return nil
}

func commandAvailable(runCtx context.Context, ctx *config.Context, name string) bool {
	_, err := ingressRunHostCommand(runCtx, ctx, []string{name, "-version"})
	return err == nil
}

func probeIngressPackageHost(runCtx context.Context, ctx *config.Context) ingressPackageProbe {
	probe := ingressPackageProbe{
		OSRelease: map[string]string{},
		Commands:  map[string]bool{},
	}
	if output, err := ingressRunHostCommand(runCtx, ctx, []string{"cat", "/etc/os-release"}); err == nil {
		probe.OSRelease = parseIngressOSRelease(output)
	}
	for _, command := range []string{"apt-get", "dnf", "yum", "rpm-ostree", "sudo"} {
		_, err := ingressRunHostCommand(runCtx, ctx, []string{command, "--version"})
		probe.Commands[command] = err == nil
	}
	if output, err := ingressRunHostCommand(runCtx, ctx, []string{"id", "-u"}); err == nil {
		probe.UserID = strings.TrimSpace(output)
	}
	return probe
}

func detectIngressPackageInstaller(probe ingressPackageProbe) ingressPackageInstaller {
	if isIngressGoogleContainerOptimizedOS(probe.OSRelease) {
		return ingressInstallerCOS
	}
	if probe.Commands["apt-get"] {
		return ingressInstallerApt
	}
	if probe.Commands["rpm-ostree"] && isIngressCoreOS(probe.OSRelease) {
		return ingressInstallerRpmOstree
	}
	if probe.Commands["dnf"] {
		return ingressInstallerDnf
	}
	if probe.Commands["yum"] {
		return ingressInstallerYum
	}
	if probe.Commands["rpm-ostree"] {
		return ingressInstallerRpmOstree
	}
	return ingressInstallerUnknown
}

func (p ingressPackageProbe) canRunRoot() bool {
	return p.UserID == "0" || p.Commands["sudo"]
}

func confirmIngressPackageInstall(opts corecomponent.ApplyOptions, ctx *config.Context, installer ingressPackageInstaller, probe ingressPackageProbe) (bool, error) {
	lines := []string{
		fmt.Sprintf("Host: %s", ingressHostLabel(ctx)),
		"Missing: mkcert",
		fmt.Sprintf("Installer: %s", ingressInstallerLabel(installer)),
	}
	if distro := ingressDistroLabel(probe); distro != "" {
		lines = append(lines, fmt.Sprintf("Detected OS: %s", distro))
	}
	prompt := corecomponent.RenderSection("Host prerequisites", strings.Join(lines, "\n")) +
		"\n\n" + corecomponent.RenderPromptLine("Install mkcert now? [y/N]: ")
	if opts.Confirm != nil {
		return opts.Confirm(prompt)
	}
	answer, err := config.GetInput(prompt)
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

func installIngressMkcert(runCtx context.Context, ctx *config.Context, installer ingressPackageInstaller, probe ingressPackageProbe) (bool, error) {
	switch installer {
	case ingressInstallerApt:
		if err := runIngressRootCommand(runCtx, ctx, probe, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update"); err != nil {
			return false, err
		}
		return false, installIngressPackageCandidates(runCtx, ctx, probe, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", [][]string{
			{"mkcert", "ca-certificates"},
			{"mkcert"},
		})
	case ingressInstallerDnf:
		return false, installIngressPackageCandidates(runCtx, ctx, probe, nil, "dnf", [][]string{
			{"mkcert", "nss-tools"},
			{"mkcert"},
		})
	case ingressInstallerYum:
		return false, installIngressPackageCandidates(runCtx, ctx, probe, nil, "yum", [][]string{
			{"mkcert", "nss-tools"},
			{"mkcert"},
		})
	case ingressInstallerRpmOstree:
		if err := installIngressPackageCandidates(runCtx, ctx, probe, nil, "rpm-ostree", [][]string{
			{"mkcert", "nss-tools"},
			{"mkcert"},
		}); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("installer %q is not supported for mkcert", installer)
	}
}

func installIngressPackageCandidates(runCtx context.Context, ctx *config.Context, probe ingressPackageProbe, env []string, installer string, packageSets [][]string) error {
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
		if err := runIngressRootCommand(runCtx, ctx, probe, env, args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func runIngressRootCommand(runCtx context.Context, ctx *config.Context, probe ingressPackageProbe, env []string, args ...string) error {
	commandArgs, err := ingressRootCommandArgs(probe, env, args...)
	if err != nil {
		return err
	}
	_, err = ingressRunHostCommand(runCtx, ctx, commandArgs)
	return err
}

func ingressRootCommandArgs(probe ingressPackageProbe, env []string, args ...string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("host command cannot be empty")
	}
	commandArgs := append([]string{}, args...)
	if len(env) > 0 {
		commandArgs = append(append([]string{"env"}, env...), commandArgs...)
	}
	if probe.UserID == "0" {
		return commandArgs, nil
	}
	if probe.Commands["sudo"] {
		return append([]string{"sudo"}, commandArgs...), nil
	}
	return nil, fmt.Errorf("host needs root or sudo to install mkcert")
}

func runIngressHostCommand(runCtx context.Context, ctx *config.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("host command cannot be empty")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		cmd := exec.Command(args[0], args[1:]...) // #nosec G204 -- command and args are fixed by sitectl.
		output, err := ctx.RunQuietCommandContext(runCtx, cmd)
		return strings.TrimSpace(output), err
	}
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...) // #nosec G204 -- command and args are fixed by sitectl.
	if ctx != nil && strings.TrimSpace(ctx.ProjectDir) != "" {
		cmd.Dir = ctx.ProjectDir
	}
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func parseIngressOSRelease(output string) map[string]string {
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

func isIngressGoogleContainerOptimizedOS(values map[string]string) bool {
	if strings.EqualFold(values["ID"], "cos") {
		return true
	}
	return strings.Contains(strings.ToLower(ingressOSReleaseIdentity(values)), "container-optimized os")
}

func isIngressCoreOS(values map[string]string) bool {
	return strings.Contains(strings.ToLower(ingressOSReleaseIdentity(values)), "coreos")
}

func ingressOSReleaseIdentity(values map[string]string) string {
	return strings.Join([]string{
		values["ID"],
		values["ID_LIKE"],
		values["NAME"],
		values["PRETTY_NAME"],
		values["VARIANT_ID"],
		values["BUILD_ID"],
	}, " ")
}

func ingressDistroLabel(probe ingressPackageProbe) string {
	for _, key := range []string{"PRETTY_NAME", "NAME", "ID"} {
		if value := strings.TrimSpace(probe.OSRelease[key]); value != "" {
			return value
		}
	}
	return ""
}

func ingressInstallerLabel(installer ingressPackageInstaller) string {
	switch installer {
	case ingressInstallerApt:
		return "apt-get"
	case ingressInstallerDnf:
		return "dnf"
	case ingressInstallerYum:
		return "yum"
	case ingressInstallerRpmOstree:
		return "rpm-ostree (reboot required)"
	case ingressInstallerCOS:
		return "Google Container-Optimized OS"
	default:
		return strings.TrimSpace(string(installer))
	}
}

func ingressHostLabel(ctx *config.Context) string {
	if ctx == nil {
		return "local host"
	}
	if ctx.DockerHostType == config.ContextRemote {
		if strings.TrimSpace(ctx.SSHHostname) != "" {
			return strings.TrimSpace(ctx.SSHHostname)
		}
		if strings.TrimSpace(ctx.Name) != "" {
			return strings.TrimSpace(ctx.Name)
		}
		return "remote host"
	}
	if strings.TrimSpace(ctx.Name) != "" {
		return strings.TrimSpace(ctx.Name)
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "local host"
}

func ensureIngressCertDir(runCtx context.Context, ctx *config.Context, certPath string) error {
	certDir := filepath.Dir(certPath)
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		_, err := ingressRunHostCommand(runCtx, ctx, []string{"mkdir", "-p", certDir})
		return err
	}
	return os.MkdirAll(certDir, 0o700)
}
