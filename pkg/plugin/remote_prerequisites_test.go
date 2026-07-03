package plugin

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

func TestEnsureRemoteCreatePrerequisitesSkipsLocalContexts(t *testing.T) {
	oldRun := runRemoteCreatePrerequisitesCommand
	defer func() { runRemoteCreatePrerequisitesCommand = oldRun }()

	called := false
	runRemoteCreatePrerequisitesCommand = func(context.Context, *config.Context, io.Writer, io.Writer, string) (string, error) {
		called = true
		return "", nil
	}

	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, &config.Context{
		DockerHostType: config.ContextLocal,
	}, RemoteCreatePrerequisitesOptions{})
	if err != nil {
		t.Fatalf("EnsureRemoteCreatePrerequisitesContext() error = %v", err)
	}
	if called {
		t.Fatal("expected local context to skip remote prerequisite checks")
	}
}

func TestEnsureRemoteCreatePrerequisitesYoloInstallsWithoutPrompt(t *testing.T) {
	mock := newRemotePrereqMock("ID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n")
	mock.available["apt-get"] = true
	mock.available["systemctl"] = true
	withRemotePrereqMock(t, mock)

	prompted := false
	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{
		Yolo: true,
		Input: func(question ...string) (string, error) {
			prompted = true
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureRemoteCreatePrerequisitesContext() error = %v", err)
	}
	if prompted {
		t.Fatal("expected --yolo to bypass prompt")
	}
	if !mock.sawCommand("apt-get", "update") {
		t.Fatalf("expected apt-get update, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
	if !mock.sawCommand("apt-get", "install", "docker.io") {
		t.Fatalf("expected apt-get install docker.io, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
	if !mock.sawCommand("systemctl", "enable", "--now", "docker") {
		t.Fatalf("expected systemctl docker start, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestEnsureRemoteCreatePrerequisitesPromptsBeforeInstall(t *testing.T) {
	mock := newRemotePrereqMock("ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12\"\n")
	mock.available["apt-get"] = true
	mock.available["systemctl"] = true
	withRemotePrereqMock(t, mock)

	var prompt string
	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{
		Input: func(question ...string) (string, error) {
			prompt = strings.Join(question, "\n")
			return "yes", nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureRemoteCreatePrerequisitesContext() error = %v", err)
	}
	if !strings.Contains(prompt, "Missing: git, make, docker, docker-compose") {
		t.Fatalf("expected prompt to list missing prerequisites, got:\n%s", prompt)
	}
	if !mock.sawCommand("apt-get", "install") {
		t.Fatalf("expected install command after confirmation, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestEnsureRemoteCreatePrerequisitesDeclineCancelsInstall(t *testing.T) {
	mock := newRemotePrereqMock("ID=ubuntu\nPRETTY_NAME=\"Ubuntu\"\n")
	mock.available["apt-get"] = true
	withRemotePrereqMock(t, mock)

	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{
		Input: func(question ...string) (string, error) {
			return "n", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancelled error, got %v", err)
	}
	if mock.sawCommand("apt-get", "install") {
		t.Fatalf("did not expect install command after decline, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestEnsureRemoteCreatePrerequisitesRequiresRootOrSudo(t *testing.T) {
	mock := newRemotePrereqMock("ID=ubuntu\nPRETTY_NAME=\"Ubuntu\"\n")
	mock.userID = "1000"
	mock.available["apt-get"] = true
	withRemotePrereqMock(t, mock)

	prompted := false
	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{
		Input: func(question ...string) (string, error) {
			prompted = true
			return "yes", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "sudo is unavailable") {
		t.Fatalf("expected sudo unavailable error, got %v", err)
	}
	if prompted {
		t.Fatal("did not expect prompt when remote user cannot elevate")
	}
}

func TestEnsureRemoteCreatePrerequisitesRejectsCOSPackageInstall(t *testing.T) {
	mock := newRemotePrereqMock("ID=cos\nPRETTY_NAME=\"Container-Optimized OS from Google\"\n")
	mock.available["docker"] = true
	withRemotePrereqMock(t, mock)

	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{Yolo: true})
	if err == nil || !strings.Contains(err.Error(), "Google Container-Optimized OS") {
		t.Fatalf("expected COS unsupported error, got %v", err)
	}
	if mock.sawCommand("apt-get", "install") || mock.sawCommand("dnf", "install") || mock.sawCommand("yum", "install") {
		t.Fatalf("did not expect package install on COS, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestEnsureRemoteCreatePrerequisitesCOSCanStartDockerOnly(t *testing.T) {
	mock := newRemotePrereqMock("ID=cos\nPRETTY_NAME=\"Container-Optimized OS from Google\"\n")
	mock.available["docker"] = true
	mock.available["git"] = true
	mock.available["make"] = true
	mock.available["systemctl"] = true
	mock.composeOK = true
	withRemotePrereqMock(t, mock)

	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{Yolo: true})
	if err != nil {
		t.Fatalf("EnsureRemoteCreatePrerequisitesContext() error = %v", err)
	}
	if !mock.sawCommand("systemctl", "enable", "--now", "docker") {
		t.Fatalf("expected COS docker service start, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestEnsureRemoteCreatePrerequisitesRpmOstreeRequiresReboot(t *testing.T) {
	mock := newRemotePrereqMock("ID=fedora\nVARIANT_ID=coreos\nPRETTY_NAME=\"Fedora CoreOS\"\n")
	mock.available["rpm-ostree"] = true
	withRemotePrereqMock(t, mock)

	sdk := NewSDK(Metadata{Name: "wp"})
	err := sdk.EnsureRemoteCreatePrerequisitesContext(context.Background(), io.Discard, remotePrerequisiteTestContext(), RemoteCreatePrerequisitesOptions{Yolo: true})
	if err == nil || !strings.Contains(err.Error(), "reboot") {
		t.Fatalf("expected reboot-required error, got %v", err)
	}
	if !mock.sawCommand("rpm-ostree", "install", "--idempotent") {
		t.Fatalf("expected rpm-ostree install, got commands:\n%s", strings.Join(mock.commands, "\n"))
	}
}

func TestDetectRemoteCreatePrerequisiteInstaller(t *testing.T) {
	tests := []struct {
		name  string
		probe remoteCreatePrerequisiteProbe
		want  remotePrerequisiteInstaller
	}{
		{
			name: "debian ubuntu",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "ubuntu"},
				Commands:  map[string]bool{"apt-get": true},
			},
			want: remoteInstallerApt,
		},
		{
			name: "fedora coreos",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "fedora", "VARIANT_ID": "coreos"},
				Commands:  map[string]bool{"rpm-ostree": true},
			},
			want: remoteInstallerRpmOstree,
		},
		{
			name: "google cos",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "cos", "PRETTY_NAME": "Container-Optimized OS from Google"},
				Commands:  map[string]bool{"docker": true},
			},
			want: remoteInstallerCOS,
		},
		{
			name: "fedora dnf",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "fedora"},
				Commands:  map[string]bool{"dnf": true},
			},
			want: remoteInstallerDnf,
		},
		{
			name: "rhel yum",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "rhel"},
				Commands:  map[string]bool{"yum": true},
			},
			want: remoteInstallerYum,
		},
		{
			name: "unsupported",
			probe: remoteCreatePrerequisiteProbe{
				OSRelease: map[string]string{"ID": "alpine"},
				Commands:  map[string]bool{"apk": true},
			},
			want: remoteInstallerUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectRemoteCreatePrerequisiteInstaller(tt.probe); got != tt.want {
				t.Fatalf("detectRemoteCreatePrerequisiteInstaller() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoteRootCommandUsesSudoAndEnv(t *testing.T) {
	command, err := remoteRootCommand(remoteCreatePrerequisiteProbe{
		UserID:   "1000",
		Commands: map[string]bool{"sudo": true},
	}, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update")
	if err != nil {
		t.Fatalf("remoteRootCommand() error = %v", err)
	}
	want := "'sudo' 'env' 'DEBIAN_FRONTEND=noninteractive' 'apt-get' 'update'"
	if command != want {
		t.Fatalf("remoteRootCommand() = %q, want %q", command, want)
	}
}

func TestResolveComposeCreateRequestReadsYoloFlag(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "wp"})
	cmd := &cobra.Command{Use: "create"}
	if err := sdk.BindComposeCreateFlags(cmd, CreateSpec{
		DockerComposeRepo:   "https://example.org/template.git",
		DockerComposeBranch: "main",
	}, nil, ""); err != nil {
		t.Fatalf("BindComposeCreateFlags() error = %v", err)
	}
	for name, value := range map[string]string{
		"type":            string(config.ContextLocal),
		"checkout-source": "template",
		"path":            t.TempDir(),
		"yolo":            "true",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}

	req, err := sdk.ResolveComposeCreateRequest(cmd, func(question ...string) (string, error) {
		t.Fatalf("did not expect prompt: %v", question)
		return "", nil
	}, "wp", "", "", "", "")
	if err != nil {
		t.Fatalf("ResolveComposeCreateRequest() error = %v", err)
	}
	if !req.Yolo {
		t.Fatal("expected yolo flag to be recorded on create request")
	}
}

type remotePrereqMock struct {
	osRelease   string
	userID      string
	available   map[string]bool
	composeOK   bool
	dockerReady bool
	commands    []string
}

func newRemotePrereqMock(osRelease string) *remotePrereqMock {
	return &remotePrereqMock{
		osRelease: osRelease,
		userID:    "0",
		available: map[string]bool{
			"sudo": false,
		},
	}
}

func withRemotePrereqMock(t *testing.T, mock *remotePrereqMock) {
	t.Helper()
	oldRun := runRemoteCreatePrerequisitesCommand
	runRemoteCreatePrerequisitesCommand = mock.run
	t.Cleanup(func() { runRemoteCreatePrerequisitesCommand = oldRun })
}

func (m *remotePrereqMock) run(_ context.Context, _ *config.Context, _, _ io.Writer, command string) (string, error) {
	m.commands = append(m.commands, command)
	switch {
	case command == "cat /etc/os-release":
		return m.osRelease, nil
	case command == "id -u":
		return m.userID, nil
	case strings.HasPrefix(command, "command -v "):
		name := strings.TrimPrefix(command, "command -v ")
		name = strings.Trim(name, "'")
		if m.available[name] {
			return "/usr/bin/" + name, nil
		}
		return "", &ssh.ExitError{}
	case command == "docker compose version":
		if m.composeOK {
			return "Docker Compose version v2.40.3", nil
		}
		return "", &ssh.ExitError{}
	case command == "docker info":
		if m.dockerReady {
			return "", nil
		}
		return "", &ssh.ExitError{}
	}

	if strings.Contains(command, "'apt-get' 'install'") ||
		strings.Contains(command, "'dnf' 'install'") ||
		strings.Contains(command, "'yum' 'install'") ||
		strings.Contains(command, "'rpm-ostree' 'install'") {
		m.available["git"] = true
		m.available["make"] = true
		m.available["docker"] = true
		m.composeOK = true
		return "", nil
	}
	if strings.Contains(command, "'systemctl' 'enable' '--now' 'docker'") ||
		strings.Contains(command, "'systemctl' 'start' 'docker'") ||
		strings.Contains(command, "'service' 'docker' 'start'") {
		m.dockerReady = true
		return "", nil
	}
	if strings.Contains(command, "'apt-get' 'update'") {
		return "", nil
	}
	return "", fmt.Errorf("unexpected command %q", command)
}

func (m *remotePrereqMock) sawCommand(parts ...string) bool {
	for _, command := range m.commands {
		matches := true
		for _, part := range parts {
			if !strings.Contains(command, "'"+part+"'") && !strings.Contains(command, part) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func remotePrerequisiteTestContext() *config.Context {
	return &config.Context{
		Name:           "wp-remote",
		DockerHostType: config.ContextRemote,
		SSHHostname:    "192.0.2.10",
		SSHUser:        "root",
		SSHPort:        22,
		SSHKeyPath:     "/tmp/key",
	}
}
