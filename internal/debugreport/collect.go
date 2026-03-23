package debugreport

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/docker/docker/client"
	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	yaml "gopkg.in/yaml.v3"
)

type HostDiagnostics struct {
	CPUCount           int
	MemoryBytes        int64
	SwapBytes          int64
	DiskAvailableBytes int64
	OSVersion          string
	Issues             []string
}

type ComposeDiagnostics struct {
	ComposePath string
	Services    []ComposeServiceImage
	BindMounts  []BindMountDiagnostics
	Issues      []string
}

type ComposeServiceImage struct {
	Service string
	Image   string
}

type BindMountDiagnostics struct {
	Service        string
	Source         string
	Target         string
	AvailableBytes int64
	Issue          string
}

type Session struct {
	ctxCfg       *config.Context
	sshClient    *ssh.Client
	dockerClient *docker.DockerClient
	fileAccessor *config.FileAccessor
}

func NewSession(ctxCfg *config.Context) (*Session, error) {
	session := &Session{ctxCfg: ctxCfg}
	if ctxCfg == nil || ctxCfg.DockerHostType == config.ContextLocal {
		return session, nil
	}
	sshClient, err := ctxCfg.DialSSH()
	if err != nil {
		return nil, err
	}
	session.sshClient = sshClient
	return session, nil
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.fileAccessor != nil {
		if err := s.fileAccessor.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.fileAccessor = nil
	}
	if s.dockerClient != nil {
		if err := s.dockerClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.dockerClient = nil
	}
	if s.sshClient != nil {
		if err := s.sshClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.sshClient = nil
	}
	return firstErr
}

func (s *Session) DockerClient() (*docker.DockerClient, error) {
	if s == nil || s.ctxCfg == nil {
		return nil, fmt.Errorf("debug session context is nil")
	}
	if s.dockerClient != nil {
		return s.dockerClient, nil
	}
	if s.ctxCfg.DockerHostType == config.ContextLocal {
		cli, err := docker.GetDockerCli(s.ctxCfg)
		if err != nil {
			return nil, err
		}
		s.dockerClient = cli
		return s.dockerClient, nil
	}
	if s.sshClient == nil {
		sshClient, err := s.ctxCfg.DialSSH()
		if err != nil {
			return nil, err
		}
		s.sshClient = sshClient
	}
	cli, err := docker.GetDockerCliWithSSH(s.ctxCfg, s.sshClient, false)
	if err != nil {
		return nil, err
	}
	s.dockerClient = cli
	return s.dockerClient, nil
}

func (s *Session) fileAccessorForContext() (*config.FileAccessor, error) {
	if s == nil || s.ctxCfg == nil {
		return nil, fmt.Errorf("debug session context is nil")
	}
	if s.fileAccessor != nil {
		return s.fileAccessor, nil
	}
	if s.ctxCfg.DockerHostType == config.ContextLocal {
		accessor, err := config.NewFileAccessor(s.ctxCfg)
		if err != nil {
			return nil, err
		}
		s.fileAccessor = accessor
		return s.fileAccessor, nil
	}
	if s.sshClient == nil {
		sshClient, err := s.ctxCfg.DialSSH()
		if err != nil {
			return nil, err
		}
		s.sshClient = sshClient
	}
	accessor, err := config.NewFileAccessorWithSSH(s.ctxCfg, s.sshClient, false)
	if err != nil {
		return nil, err
	}
	s.fileAccessor = accessor
	return s.fileAccessor, nil
}

func (s *Session) RunQuietCommandContext(runCtx context.Context, cmd *exec.Cmd) (string, error) {
	if s == nil || s.ctxCfg == nil {
		return "", fmt.Errorf("debug session context is nil")
	}
	if s.ctxCfg.DockerHostType == config.ContextLocal {
		return s.ctxCfg.RunQuietCommandContext(runCtx, cmd)
	}
	if s.sshClient == nil {
		sshClient, err := s.ctxCfg.DialSSH()
		if err != nil {
			return "", err
		}
		s.sshClient = sshClient
	}
	return runRemoteCommandWithSSH(runCtx, s.ctxCfg, s.sshClient, cmd)
}

func CollectHostDiagnostics(runCtx context.Context, ctxCfg *config.Context) HostDiagnostics {
	session, err := NewSession(ctxCfg)
	if err != nil {
		return HostDiagnostics{MemoryBytes: -1, SwapBytes: -1, DiskAvailableBytes: -1, Issues: []string{fmt.Sprintf("ssh: %v", err)}}
	}
	defer session.Close()
	return CollectHostDiagnosticsWithSession(runCtx, ctxCfg, session)
}

func CollectHostDiagnosticsWithSession(runCtx context.Context, ctxCfg *config.Context, session *Session) HostDiagnostics {
	if ctxCfg.DockerHostType == config.ContextLocal {
		return collectLocalHostDiagnostics(ctxCfg)
	}

	diagnostics := HostDiagnostics{MemoryBytes: -1, SwapBytes: -1, DiskAvailableBytes: -1}
	cli, err := session.DockerClient()
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("docker info: %v", err))
	} else {
		apiClient, ok := cli.CLI.(*client.Client)
		if !ok {
			diagnostics.Issues = append(diagnostics.Issues, "docker info: docker client does not support host info")
		} else if info, err := apiClient.Info(runCtx); err != nil {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("docker info: %v", err))
		} else {
			if info.NCPU > 0 {
				diagnostics.CPUCount = info.NCPU
			}
			if info.MemTotal > 0 {
				diagnostics.MemoryBytes = info.MemTotal
			}
			if strings.TrimSpace(info.OperatingSystem) != "" {
				diagnostics.OSVersion = strings.TrimSpace(info.OperatingSystem)
			}
		}
	}

	accessor, err := session.fileAccessorForContext()
	if err != nil {
		if diagnostics.MemoryBytes < 0 {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", err))
		}
		if strings.TrimSpace(diagnostics.OSVersion) == "" {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("os: %v", err))
		}
		if availableDiskBytes, diskErr := availableDiskBytesWithSession(ctxCfg, session); diskErr != nil {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("disk: %v", diskErr))
		} else {
			diagnostics.DiskAvailableBytes = availableDiskBytes
		}
		return diagnostics
	}

	meminfo, err := accessor.ReadFileContext(runCtx, "/proc/meminfo")
	if err != nil {
		if diagnostics.MemoryBytes < 0 {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", err))
		}
	} else {
		memoryBytes, swapBytes, parseErr := ParseMemInfo(string(meminfo))
		if parseErr != nil {
			if diagnostics.MemoryBytes < 0 {
				diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", parseErr))
			}
		} else {
			if diagnostics.MemoryBytes < 0 {
				diagnostics.MemoryBytes = memoryBytes
			}
			diagnostics.SwapBytes = swapBytes
		}
	}

	osRelease, err := accessor.ReadFileContext(runCtx, "/etc/os-release")
	if err != nil {
		if strings.TrimSpace(diagnostics.OSVersion) == "" {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("os: %v", err))
		}
	} else if osVersion := parseOSRelease(string(osRelease)); osVersion != "" {
		diagnostics.OSVersion = osVersion
	} else if strings.TrimSpace(diagnostics.OSVersion) == "" {
		diagnostics.Issues = append(diagnostics.Issues, "os: PRETTY_NAME not found in /etc/os-release")
	}

	availableDiskBytes, err := availableDiskBytesWithSession(ctxCfg, session)
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("disk: %v", err))
	} else {
		diagnostics.DiskAvailableBytes = availableDiskBytes
	}

	return diagnostics
}

func CollectComposeDiagnostics(runCtx context.Context, ctxCfg *config.Context) ComposeDiagnostics {
	session, err := NewSession(ctxCfg)
	if err != nil {
		return ComposeDiagnostics{ComposePath: filepath.Join(ctxCfg.ProjectDir, "docker-compose.yml"), Issues: []string{fmt.Sprintf("ssh: %v", err)}}
	}
	defer session.Close()
	return CollectComposeDiagnosticsWithSession(runCtx, ctxCfg, session)
}

func CollectComposeDiagnosticsWithSession(runCtx context.Context, ctxCfg *config.Context, session *Session) ComposeDiagnostics {
	composePath := filepath.Join(ctxCfg.ProjectDir, "docker-compose.yml")
	diagnostics := ComposeDiagnostics{ComposePath: composePath}
	if err := runCtx.Err(); err != nil {
		diagnostics.Issues = append(diagnostics.Issues, err.Error())
		return diagnostics
	}
	output, err := session.RunQuietCommandContext(runCtx, exec.Command("docker", composeConfigArgs(*ctxCfg)...))
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("compose config: %v", err))
		return diagnostics
	}
	services, bindMounts, parseErr := ParseComposeDiagnostics([]byte(output))
	if parseErr != nil {
		diagnostics.Issues = append(diagnostics.Issues, parseErr.Error())
		return diagnostics
	}
	diagnostics.Services = services
	diagnostics.BindMounts = collectBindMountDiskDiagnostics(ctxCfg, session, bindMounts)
	return diagnostics
}

func ParseComposeDiagnostics(data []byte) ([]ComposeServiceImage, []BindMountDiagnostics, error) {
	var compose struct {
		Services map[string]struct {
			Image   string `yaml:"image"`
			Volumes []any  `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, nil, fmt.Errorf("parse compose file: %w", err)
	}
	services := make([]ComposeServiceImage, 0, len(compose.Services))
	bindMounts := make([]BindMountDiagnostics, 0)
	for serviceName, service := range compose.Services {
		image := strings.TrimSpace(service.Image)
		if image == "" {
			image = "(no image field)"
		}
		services = append(services, ComposeServiceImage{Service: serviceName, Image: image})
		bindMounts = append(bindMounts, extractBindMounts(serviceName, service.Volumes)...)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Service < services[j].Service })
	sort.Slice(bindMounts, func(i, j int) bool {
		if bindMounts[i].Source == bindMounts[j].Source {
			return bindMounts[i].Service < bindMounts[j].Service
		}
		return bindMounts[i].Source < bindMounts[j].Source
	})
	return services, bindMounts, nil
}

func ParseComposeServiceImages(data []byte) ([]ComposeServiceImage, error) {
	services, _, err := ParseComposeDiagnostics(data)
	return services, err
}

func collectBindMountDiskDiagnostics(ctxCfg *config.Context, session *Session, mounts []BindMountDiagnostics) []BindMountDiagnostics {
	seen := map[string]bool{}
	results := make([]BindMountDiagnostics, 0, len(mounts))
	for _, mount := range mounts {
		key := strings.TrimSpace(mount.Source)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		mount.AvailableBytes = -1
		availableBytes, err := availableDiskBytesAtPathWithSession(ctxCfg, session, mount.Source)
		if err != nil {
			mount.Issue = err.Error()
		} else {
			mount.AvailableBytes = availableBytes
		}
		results = append(results, mount)
	}
	return results
}

func extractBindMounts(serviceName string, volumes []any) []BindMountDiagnostics {
	mounts := make([]BindMountDiagnostics, 0)
	for _, raw := range volumes {
		switch volume := raw.(type) {
		case string:
			source, target, ok := parseBindVolumeString(volume)
			if !ok {
				continue
			}
			mounts = append(mounts, BindMountDiagnostics{Service: serviceName, Source: source, Target: target})
		case map[string]any:
			typeName := strings.ToLower(strings.TrimSpace(stringValue(volume["type"])))
			if typeName != "bind" {
				continue
			}
			source := strings.TrimSpace(stringValue(volume["source"]))
			target := strings.TrimSpace(stringValue(volume["target"]))
			if source == "" || target == "" {
				continue
			}
			mounts = append(mounts, BindMountDiagnostics{Service: serviceName, Source: source, Target: target})
		}
	}
	return mounts
}

func parseBindVolumeString(value string) (source string, target string, ok bool) {
	parts := splitVolumeSpec(value)
	if len(parts) < 2 {
		return "", "", false
	}
	source = strings.TrimSpace(parts[0])
	target = strings.TrimSpace(parts[1])
	if source == "" || target == "" || !looksLikeHostPath(source) {
		return "", "", false
	}
	return source, target, true
}

func splitVolumeSpec(value string) []string {
	if len(value) >= 2 && value[1] == ':' {
		parts := strings.SplitN(value[2:], ":", 3)
		if len(parts) == 0 {
			return []string{value}
		}
		parts[0] = value[:2] + parts[0]
		return parts
	}
	return strings.SplitN(value, ":", 3)
}

func looksLikeHostPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "~/") || (len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/'))
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return str
	}
	return fmt.Sprint(value)
}

func ParseMemInfo(data string) (memoryBytes int64, swapBytes int64, err error) {
	values := map[string]int64{}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, convErr := strconv.ParseInt(fields[1], 10, 64)
		if convErr != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	memoryBytes, memoryFound := values["MemTotal"]
	swapBytes, swapFound := values["SwapTotal"]
	if !memoryFound && !swapFound {
		return 0, 0, fmt.Errorf("no MemTotal or SwapTotal entries found")
	}
	if !memoryFound {
		memoryBytes = 0
	}
	if !swapFound {
		swapBytes = 0
	}
	return memoryBytes, swapBytes, nil
}

func composeConfigArgs(ctxCfg config.Context) []string {
	args := []string{"compose"}
	for _, file := range ctxCfg.ComposeFile {
		args = append(args, "-f", file)
	}
	for _, env := range ctxCfg.EnvFile {
		args = append(args, "--env-file", env)
	}
	return append(args, "config")
}

func availableDiskBytes(ctxCfg *config.Context) (int64, error) {
	return availableDiskBytesWithSession(ctxCfg, nil)
}

func availableDiskBytesAtPathWithSession(ctxCfg *config.Context, session *Session, path string) (int64, error) {
	trimmedPath := firstNonEmpty(strings.TrimSpace(path), "/")
	if ctxCfg.DockerHostType == config.ContextLocal {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(trimmedPath, &stat); err != nil {
			return 0, err
		}
		return int64(stat.Bavail) * int64(stat.Bsize), nil
	}
	if session != nil {
		accessor, err := session.fileAccessorForContext()
		if err != nil {
			return 0, err
		}
		if accessor != nil {
			stat, err := accessor.StatVFS(trimmedPath)
			if err != nil {
				return 0, err
			}
			fragmentSize := int64(stat.Frsize)
			if fragmentSize <= 0 {
				fragmentSize = int64(stat.Bsize)
			}
			return int64(stat.Bavail) * fragmentSize, nil
		}
	}
	sshClient, err := ctxCfg.DialSSH()
	if err != nil {
		return 0, err
	}
	defer sshClient.Close()
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return 0, err
	}
	defer sftpClient.Close()
	stat, err := sftpClient.StatVFS(trimmedPath)
	if err != nil {
		return 0, err
	}
	fragmentSize := int64(stat.Frsize)
	if fragmentSize <= 0 {
		fragmentSize = int64(stat.Bsize)
	}
	return int64(stat.Bavail) * fragmentSize, nil
}

func availableDiskBytesWithSession(ctxCfg *config.Context, session *Session) (int64, error) {
	return availableDiskBytesAtPathWithSession(ctxCfg, session, ctxCfg.ProjectDir)
}

func runRemoteCommandWithSSH(runCtx context.Context, ctxCfg *config.Context, sshClient *ssh.Client, cmd *exec.Cmd) (string, error) {
	remoteCmd := fmt.Sprintf("cd %s && %s", shellquote.Join(ctxCfg.ProjectDir), shellquote.Join(cmd.Args...))
	session, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("error creating SSH session: %v", err)
	}
	var stdout strings.Builder
	var stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr
	var closeOnce sync.Once
	closeSession := func() { _ = session.Close() }
	defer closeOnce.Do(closeSession)
	go func() {
		<-runCtx.Done()
		closeOnce.Do(closeSession)
	}()
	if err := session.Start(remoteCmd); err != nil {
		return "", fmt.Errorf("error starting remote command %q: %v", remoteCmd, err)
	}
	if err := session.Wait(); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok && exitErr.ExitStatus() == 130 {
			return strings.TrimRight(stdout.String()+stderr.String(), "\n"), nil
		}
		combined := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		if combined != "" {
			return "", fmt.Errorf("error waiting for remote command %q: %v: %s", remoteCmd, err, combined)
		}
		return "", fmt.Errorf("error waiting for remote command %q: %v", remoteCmd, err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func parseOSRelease(data string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
