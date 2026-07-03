package traefik

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

func TestEnsureIngressMkcertPrerequisitesYoloInstallsWithApt(t *testing.T) {
	ctx := &config.Context{Name: "qa", DockerHostType: config.ContextRemote, SSHHostname: "qa.example.org"}
	var calls [][]string
	installed := false
	notFound := errors.New("not found")

	originalRunner := ingressRunHostCommand
	ingressRunHostCommand = func(_ context.Context, _ *config.Context, args []string) (string, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "mkcert -version":
			if installed {
				return "v1.4.4", nil
			}
			return "", notFound
		case "cat /etc/os-release":
			return "ID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n", nil
		case "apt-get --version":
			return "apt 2.7", nil
		case "dnf --version", "yum --version", "rpm-ostree --version", "sudo --version":
			return "", notFound
		case "id -u":
			return "0\n", nil
		case "env DEBIAN_FRONTEND=noninteractive apt-get update":
			return "", nil
		case "env DEBIAN_FRONTEND=noninteractive apt-get install -y mkcert ca-certificates":
			installed = true
			return "", nil
		default:
			t.Fatalf("unexpected command %q", strings.Join(args, " "))
			return "", nil
		}
	}
	t.Cleanup(func() { ingressRunHostCommand = originalRunner })

	err := ensureIngressMkcertPrerequisites(context.Background(), ctx, corecomponent.ApplyOptions{
		Yolo: true,
		Confirm: func(prompt string) (bool, error) {
			t.Fatalf("confirm should not be called with yolo, got %q", prompt)
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("ensureIngressMkcertPrerequisites() error = %v", err)
	}
	if !installed {
		t.Fatal("expected apt install command to run")
	}
	wantTail := [][]string{
		{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"},
		{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "mkcert", "ca-certificates"},
		{"mkcert", "-version"},
	}
	if len(calls) < len(wantTail) {
		t.Fatalf("expected at least %d calls, got %#v", len(wantTail), calls)
	}
	if gotTail := calls[len(calls)-len(wantTail):]; !reflect.DeepEqual(gotTail, wantTail) {
		t.Fatalf("last commands = %#v, want %#v", gotTail, wantTail)
	}
}

func TestEnsureIngressMkcertPrerequisitesDeclineCancelsInstall(t *testing.T) {
	ctx := &config.Context{Name: "qa", DockerHostType: config.ContextRemote, SSHHostname: "qa.example.org"}
	notFound := errors.New("not found")
	var installAttempted bool

	originalRunner := ingressRunHostCommand
	ingressRunHostCommand = func(_ context.Context, _ *config.Context, args []string) (string, error) {
		switch strings.Join(args, " ") {
		case "mkcert -version":
			return "", notFound
		case "cat /etc/os-release":
			return "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12\"\n", nil
		case "apt-get --version":
			return "apt 2.6", nil
		case "dnf --version", "yum --version", "rpm-ostree --version", "sudo --version":
			return "", notFound
		case "id -u":
			return "0\n", nil
		default:
			if strings.Contains(strings.Join(args, " "), "install") {
				installAttempted = true
			}
			return "", nil
		}
	}
	t.Cleanup(func() { ingressRunHostCommand = originalRunner })

	err := ensureIngressMkcertPrerequisites(context.Background(), ctx, corecomponent.ApplyOptions{
		Confirm: func(prompt string) (bool, error) {
			if !strings.Contains(prompt, "Install mkcert now?") {
				t.Fatalf("unexpected prompt %q", prompt)
			}
			return false, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if installAttempted {
		t.Fatal("did not expect package install after declined confirmation")
	}
}

func TestDetectIngressPackageInstaller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe ingressPackageProbe
		want  ingressPackageInstaller
	}{
		{
			name: "ubuntu apt",
			probe: ingressPackageProbe{Commands: map[string]bool{
				"apt-get": true,
			}},
			want: ingressInstallerApt,
		},
		{
			name: "coreos rpm ostree",
			probe: ingressPackageProbe{
				OSRelease: map[string]string{"ID": "fedora", "VARIANT_ID": "coreos"},
				Commands:  map[string]bool{"rpm-ostree": true},
			},
			want: ingressInstallerRpmOstree,
		},
		{
			name: "google cos",
			probe: ingressPackageProbe{
				OSRelease: map[string]string{"ID": "cos", "PRETTY_NAME": "Container-Optimized OS from Google"},
				Commands:  map[string]bool{"apt-get": true},
			},
			want: ingressInstallerCOS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := detectIngressPackageInstaller(tt.probe); got != tt.want {
				t.Fatalf("detectIngressPackageInstaller() = %q, want %q", got, tt.want)
			}
		})
	}
}
