package hostruntime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

// OverlayOptions controls read-only production volume overlays.
type OverlayOptions struct {
	VolumesRoot string
	LowerRoot   string
	Volumes     []string
	Reset       bool
	Stdout      io.Writer
	Stderr      io.Writer
}

// MountOverlays converges the declared Docker volume overlays.
func MountOverlays(ctx context.Context, options OverlayOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("overlay mounting must run as root")
	}
	if options.VolumesRoot == "" {
		options.VolumesRoot = "/mnt/disks/volumes"
	}
	if options.LowerRoot == "" {
		options.LowerRoot = "/mnt/disks/prod-readonly"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	for label, root := range map[string]string{"volumes": options.VolumesRoot, "read-only lower": options.LowerRoot} {
		if err := requireOverlayRoot(root); err != nil {
			return fmt.Errorf("%s root: %w", label, err)
		}
	}
	for _, volume := range options.Volumes {
		if !volumeNamePattern.MatchString(volume) {
			return fmt.Errorf("unsafe Docker volume name %q", volume)
		}
		if err := mountOverlay(ctx, options, volume); err != nil {
			return fmt.Errorf("mount overlay %q: %w", volume, err)
		}
	}
	return nil
}

func mountOverlay(ctx context.Context, options OverlayOptions, volume string) error {
	target := filepath.Join(options.VolumesRoot, volume)
	lower := filepath.Join(options.LowerRoot, volume)
	upper := filepath.Join(options.VolumesRoot, ".overlay", volume, "upper")
	work := filepath.Join(options.VolumesRoot, ".overlay", volume, "work")
	for _, path := range []string{target, upper, work} {
		if err := ensureOverlayDirectory(path, options.VolumesRoot); err != nil {
			return err
		}
	}
	if err := ensureOverlayDirectory(lower, options.LowerRoot); err != nil {
		return fmt.Errorf("lower directory: %w", err)
	}
	upperInfo, err := os.Stat(upper)
	if err != nil {
		return err
	}
	workInfo, err := os.Stat(work)
	if err != nil {
		return err
	}
	upperStat, upperOK := upperInfo.Sys().(*syscall.Stat_t)
	workStat, workOK := workInfo.Sys().(*syscall.Stat_t)
	if !upperOK || !workOK || upperStat.Dev != workStat.Dev {
		return fmt.Errorf("overlay upper and work directories must share a filesystem")
	}
	expected := map[string]string{"lowerdir": lower, "upperdir": upper, "workdir": work}
	mounted, exact, err := overlayMountState(ctx, target, expected)
	if err != nil {
		return err
	}
	if mounted {
		if !exact {
			return fmt.Errorf("target is mounted from another filesystem: %s", target)
		}
		if !options.Reset {
			return nil
		}
		if err := runOverlayCommand(ctx, options, "umount", "--", target); err != nil {
			return err
		}
	}
	if options.Reset {
		for _, path := range []string{upper, work} {
			if err := emptyOverlayDirectory(path); err != nil {
				return err
			}
		}
	}
	mountOptions := "lowerdir=" + lower + ",upperdir=" + upper + ",workdir=" + work
	if err := runOverlayCommand(ctx, options, "mount", "-t", "overlay", "overlay", "-o", mountOptions, target); err != nil {
		return err
	}
	_, exact, err = overlayMountState(ctx, target, expected)
	if err != nil || !exact {
		_ = runOverlayCommand(ctx, options, "umount", "--", target)
		return fmt.Errorf("overlay mount verification failed")
	}
	return nil
}

func requireOverlayRoot(root string) error {
	if !safeAbsolutePath(root) || strings.ContainsAny(root, " ,:\\") {
		return fmt.Errorf("unsafe path %s", root)
	}
	return requireDirectory(root)
}

func ensureOverlayDirectory(path, boundary string) error {
	if !withinRoot(path, boundary) {
		return fmt.Errorf("path escapes overlay root: %s", path)
	}
	if err := mkdirAllNoSymlink(path, 0o755); err != nil {
		return err
	}
	if err := requireDirectory(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func overlayMountState(ctx context.Context, target string, expected map[string]string) (bool, bool, error) {
	filesystem, status, err := hostCommandOutputStatus(ctx, "findmnt", "-rn", "-o", "FSTYPE", "--mountpoint", target)
	if err != nil {
		return false, false, err
	}
	if status == 1 {
		return false, false, nil
	}
	if status != 0 {
		return false, false, fmt.Errorf("findmnt failed with status %d", status)
	}
	options, err := commandOutputStrict(ctx, "findmnt", "-rn", "-o", "OPTIONS", "--mountpoint", target)
	if err != nil {
		return true, false, err
	}
	if strings.TrimSpace(filesystem) != "overlay" {
		return true, false, nil
	}
	actual := map[string]string{}
	for _, option := range strings.Split(strings.TrimSpace(options), ",") {
		name, value, found := strings.Cut(option, "=")
		if found {
			actual[name] = value
		}
	}
	for name, value := range expected {
		if actual[name] != value {
			return true, false, nil
		}
	}
	return true, true, nil
}

func emptyOverlayDirectory(path string) error {
	if err := requireDirectory(path); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func runOverlayCommand(ctx context.Context, options OverlayOptions, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
