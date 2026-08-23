package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const defaultBootstrapMarker = "/var/lib/cloud-compose/bootstrap-complete"

// MarkerValid reports whether a Cloud Compose readiness marker is safe and valid.
func MarkerValid(path string) bool {
	return markerValidFor(path, path == defaultBootstrapMarker, 0, 0)
}

func markerValidFor(path string, durable bool, uid, gid uint32) bool {
	contents, err := readSingleLinkFile(path, 16)
	if err != nil {
		return false
	}
	if !durable {
		return true
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o644 || string(contents) != "ready\n" || !ownedBy(info, uid, gid) {
		return false
	}
	directory, err := os.Lstat(filepath.Dir(path))
	return err == nil && directory.IsDir() && directory.Mode()&os.ModeSymlink == 0 && directory.Mode().Perm() == 0o755 && ownedBy(directory, uid, gid)
}

// PublishMarker atomically writes a root-owned readiness marker.
func PublishMarker(path string) error {
	return publishMarkerOwned(path, path == defaultBootstrapMarker, 0, 0)
}

func publishMarkerOwned(path string, durable bool, uid, gid uint32) error {
	if !safeAbsolutePath(path) {
		return fmt.Errorf("unsafe Cloud Compose marker path: %s", path)
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe Cloud Compose marker directory: %s", directory)
	}
	if durable && (info.Mode().Perm() != 0o755 || !ownedBy(info, uid, gid)) {
		return fmt.Errorf("durable Cloud Compose readiness requires a trusted state directory")
	}
	if err := writeAtomic(path, []byte("ready\n"), 0o644); err != nil {
		return err
	}
	return requireOwnedFile(path, uid, gid, 0o644)
}

// ApplicationNeedsInitialization reports whether neither durable nor current-boot readiness exists.
func ApplicationNeedsInitialization(durable, currentBoot string) bool {
	return !MarkerValid(durable) && !MarkerValid(currentBoot)
}

// ConsumeFreshMarker validates and removes a one-shot fresh-filesystem marker.
func ConsumeFreshMarker(path, identity string) error {
	return consumeFreshMarkerOwned(path, identity, 0, 0)
}

func consumeFreshMarkerOwned(path, identity string, uid, gid uint32) error {
	if identity != "fresh" && !freshIdentityPattern.MatchString(identity) {
		return fmt.Errorf("unsafe fresh-filesystem identity")
	}
	if !safeAbsolutePath(path) {
		return fmt.Errorf("unsafe fresh-filesystem marker path: %s", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !ownedBy(info, uid, gid) {
		return fmt.Errorf("unsafe fresh-filesystem marker: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("unsafe fresh-filesystem marker links: %s", path)
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 || directory.Mode().Perm() != 0o700 || !ownedBy(directory, uid, gid) {
		return fmt.Errorf("unsafe fresh-filesystem marker directory: %s", filepath.Dir(path))
	}
	contents, err := readSingleLinkFile(path, 128)
	if err != nil || string(contents) != identity+"\n" {
		return fmt.Errorf("fresh-filesystem marker does not match this disk incarnation")
	}
	return os.Remove(path)
}

// SystemdOptions controls bounded Cloud Compose oneshot convergence.
type SystemdOptions struct {
	Unit      string
	Timeout   time.Duration
	Poll      time.Duration
	Heartbeat time.Duration
	Stdout    io.Writer
	Stderr    io.Writer
}

// StartAndWaitSystemd enables, starts, and waits for an allowed Cloud Compose oneshot.
func StartAndWaitSystemd(ctx context.Context, options SystemdOptions) error {
	options = systemdDefaults(options)
	if err := validateSystemdOptions(options); err != nil {
		return err
	}
	load, err := systemctlValue(ctx, options, "LoadState")
	if err != nil || load != "loaded" {
		return fmt.Errorf("cloud-compose systemd unit is not loaded: %s (%s)", options.Unit, load)
	}
	if err := runSystemctl(ctx, options, "enable", "--", options.Unit); err != nil {
		return err
	}
	active, err := systemctlValue(ctx, options, "ActiveState")
	if err != nil {
		return err
	}
	if active != "active" && active != "activating" {
		if active == "failed" {
			if err := runSystemctl(ctx, options, "reset-failed", "--", options.Unit); err != nil {
				return err
			}
		}
		if err := runSystemctl(ctx, options, "start", "--no-block", "--", options.Unit); err != nil {
			return err
		}
	}
	deadline := time.NewTimer(options.Timeout)
	defer deadline.Stop()
	poll := time.NewTicker(options.Poll)
	defer poll.Stop()
	heartbeat := time.NewTicker(options.Heartbeat)
	defer heartbeat.Stop()
	for {
		active, err = systemctlValue(ctx, options, "ActiveState")
		if err != nil {
			return err
		}
		switch active {
		case "active":
			return nil
		case "failed":
			_ = runSystemctl(ctx, options, "status", "--no-pager", "--full", "--", options.Unit)
			return fmt.Errorf("cloud-compose systemd unit reached a terminal failed state: %s", options.Unit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			_ = runSystemctl(ctx, options, "status", "--no-pager", "--full", "--", options.Unit)
			return fmt.Errorf("timed out waiting %s for %s to become active", options.Timeout, options.Unit)
		case <-heartbeat.C:
			fmt.Fprintf(options.Stderr, "Still waiting for %s (active state: %s)\n", options.Unit, active)
			_ = runSystemctl(ctx, options, "show", "--no-pager", "--property=ActiveState,SubState,Result,NRestarts,ExecMainCode,ExecMainStatus", "--", options.Unit)
		case <-poll.C:
		}
	}
}

// EnsureBootstrap starts bootstrap only when its durable marker is absent.
func EnsureBootstrap(ctx context.Context, marker string, options SystemdOptions) error {
	if MarkerValid(marker) {
		return nil
	}
	options = systemdDefaults(options)
	if options.Unit != "cloud-compose-bootstrap.service" {
		return fmt.Errorf("bootstrap convergence requires cloud-compose-bootstrap.service")
	}
	if err := runSystemctl(ctx, options, "daemon-reload"); err != nil {
		return err
	}
	active, err := systemctlValue(ctx, options, "ActiveState")
	if err != nil {
		return err
	}
	if active == "active" && !MarkerValid(marker) {
		if err := runSystemctl(ctx, options, "stop", "--", options.Unit); err != nil {
			return err
		}
	}
	if err := StartAndWaitSystemd(ctx, options); err != nil {
		return err
	}
	if !MarkerValid(marker) {
		return fmt.Errorf("cloud-compose bootstrap service became active without publishing readiness")
	}
	return nil
}

func systemdDefaults(options SystemdOptions) SystemdOptions {
	if options.Timeout == 0 {
		options.Timeout = 3 * time.Hour
	}
	if options.Poll == 0 {
		options.Poll = 2 * time.Second
	}
	if options.Heartbeat == 0 {
		options.Heartbeat = 5 * time.Minute
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

func validateSystemdOptions(options SystemdOptions) error {
	if options.Unit != "cloud-compose.service" && options.Unit != "cloud-compose-bootstrap.service" {
		return fmt.Errorf("unsupported Cloud Compose systemd unit: %s", options.Unit)
	}
	if options.Timeout < time.Second || options.Timeout > 12*time.Hour || options.Poll < time.Second || options.Poll > 5*time.Minute || options.Heartbeat < time.Second || options.Heartbeat > time.Hour {
		return fmt.Errorf("invalid Cloud Compose systemd wait duration")
	}
	return nil
}

func systemctlValue(ctx context.Context, options SystemdOptions, property string) (string, error) {
	command := exec.CommandContext(ctx, "systemctl", "show", "--property="+property, "--value", "--", options.Unit)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func runSystemctl(ctx context.Context, options SystemdOptions, args ...string) error {
	command := exec.CommandContext(ctx, "systemctl", args...)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
