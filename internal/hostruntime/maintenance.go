package hostruntime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

var dockerDurationPattern = regexp.MustCompile(`^[0-9]+[smh]$`)

// PruneDocker removes stopped and dangling Docker data older than the supplied duration.
func PruneDocker(ctx context.Context, until, lockPath string, stdout, stderr io.Writer) error {
	if !dockerDurationPattern.MatchString(until) {
		return fmt.Errorf("docker prune duration must look like 168h")
	}
	lock, err := AcquireLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	for _, args := range [][]string{
		{"container", "prune", "--force", "--filter", "until=" + until},
		{"network", "prune", "--force", "--filter", "until=" + until},
		{"image", "prune", "--force", "--filter", "until=" + until},
		{"builder", "prune", "--force", "--filter", "until=" + until},
	} {
		command := exec.CommandContext(ctx, "docker", args...)
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// BootstrapState reports complete, active, or idle for host provisioning.
func BootstrapState(ctx context.Context, marker string) (string, error) {
	if contents, err := readSingleLinkFile(marker, 64); err == nil && string(contents) == "ready\n" {
		info, _ := os.Stat(marker)
		stat, _ := info.Sys().(*syscall.Stat_t)
		if info.Mode().Perm() == 0o644 && stat != nil && stat.Uid == 0 {
			return "complete", nil
		}
	}
	load := commandOutput(ctx, "systemctl", "show", "--property=LoadState", "--value", "cloud-compose-bootstrap.service")
	if load == "loaded" {
		active := commandOutput(ctx, "systemctl", "show", "--property=ActiveState", "--value", "cloud-compose-bootstrap.service")
		sub := commandOutput(ctx, "systemctl", "show", "--property=SubState", "--value", "cloud-compose-bootstrap.service")
		if active == "active" || active == "activating" || sub == "auto-restart" {
			return "active", nil
		}
	}
	for _, unit := range []string{"cloud-final.service", "cloud-compose.service"} {
		if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil {
			return "active", nil
		}
	}
	return "idle", nil
}

// WriteDiagnostics prints host bootstrap and application state without mutating it.
func WriteDiagnostics(ctx context.Context, manifest Manifest, dump bool, stdout io.Writer) error {
	_, _ = fmt.Fprintln(stdout, "--- cloud-init status ---")
	runDiagnostic(ctx, stdout, "cloud-init", "status", "--long")
	for _, unit := range []string{"cloud-final.service", "cloud-compose-bootstrap.service", "cloud-compose.service"} {
		_, _ = fmt.Fprintf(stdout, "--- %s state ---\n", unit)
		runDiagnostic(ctx, stdout, "systemctl", "show", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=SubState", "--property=Result", "--property=MainPID", "--property=ExecMainStatus", unit)
	}
	state, _ := BootstrapState(ctx, "/var/lib/cloud-compose/bootstrap-complete")
	_, _ = fmt.Fprintf(stdout, "bootstrap-state: %s\n", state)
	if !dump {
		return nil
	}
	for _, file := range []string{"/var/log/cloud-init-output.log", "/var/log/cloud-init.log", "/home/cloud-compose/run.log"} {
		_, _ = fmt.Fprintf(stdout, "--- %s ---\n", file)
		tailFile(stdout, file, 400)
	}
	for _, unit := range []string{"cloud-compose-bootstrap", "cloud-compose"} {
		_, _ = fmt.Fprintf(stdout, "--- %s journal ---\n", unit)
		runDiagnostic(ctx, stdout, "journalctl", "-u", unit, "--no-pager", "-n", "400")
	}
	_, _ = fmt.Fprintln(stdout, "--- docker ps ---")
	runDiagnostic(ctx, stdout, "docker", "ps", "-a")
	for _, name := range manifest.Names() {
		app := manifest[name]
		_, _ = fmt.Fprintf(stdout, "--- docker compose ps: %s ---\n", name)
		runDiagnostic(ctx, stdout, "docker", "compose", "--project-directory", app.ProjectDir, "ps")
	}
	return nil
}

func commandOutput(ctx context.Context, name string, args ...string) string {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func runDiagnostic(ctx context.Context, output io.Writer, name string, args ...string) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = output, output
	_ = command.Run()
}

func tailFile(output io.Writer, path string, lines int) {
	contents, err := readSingleLinkFile(path, 16<<20)
	if err != nil {
		_, _ = fmt.Fprintln(output, "not present as a safe regular file")
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	var buffered []string
	for scanner.Scan() {
		buffered = append(buffered, scanner.Text())
		if len(buffered) > lines {
			buffered = buffered[1:]
		}
	}
	_, _ = fmt.Fprintln(output, strings.Join(buffered, "\n"))
}
