package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	freshFilesystemLabel = "cc-fresh-pending"
	fstabBeginMarker     = "# BEGIN cloud-compose persistent mounts"
	fstabEndMarker       = "# END cloud-compose persistent mounts"
)

var (
	devicePathPattern    = regexp.MustCompile(`^/dev/[A-Za-z0-9._/+:-]+$`)
	freshIdentityPattern = regexp.MustCompile(`^v1:gcp-disk-id:[0-9]{1,32}$`)
	digitalOceanPattern  = regexp.MustCompile(`^/dev/disk/by-id/scsi-0DO_Volume_([A-Za-z0-9][A-Za-z0-9_.-]{0,254})$`)
)

// FilesystemOptions controls durable disk discovery, formatting, mounting, and persistence.
type FilesystemOptions struct {
	DataDevice    string
	VolumesDevice string
	OverlayDevice string
	DataMount     string
	VolumesMount  string
	OverlayMount  string
	FreshIdentity string
	ReadyMarker   string
	FstabPath     string
	FstabLockPath string
	SystemdDir    string
	DeviceWait    time.Duration
	AutomountWait time.Duration
	Stdout        io.Writer
	Stderr        io.Writer
}

// PrepareFilesystems converges the host's data, Docker-volume, optional overlay,
// bind mounts, and managed fstab block without ever formatting a signed disk.
func PrepareFilesystems(ctx context.Context, options FilesystemOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("filesystem preparation must run as root")
	}
	options = filesystemDefaults(options)
	if err := validateFilesystemOptions(options); err != nil {
		return err
	}
	if options.ReadyMarker != "" {
		if err := removeRegular(options.ReadyMarker); err != nil {
			return err
		}
	}
	dataDevice, err := waitForBlockDevice(ctx, options.DataDevice, options)
	if err != nil {
		return err
	}
	volumesDevice, err := waitForBlockDevice(ctx, options.VolumesDevice, options)
	if err != nil {
		return err
	}
	if dataDevice == volumesDevice {
		return fmt.Errorf("data and Docker-volume device paths resolve to the same block device")
	}
	if options.OverlayDevice != "" {
		overlayDevice, waitErr := waitForBlockDevice(ctx, options.OverlayDevice, options)
		if waitErr != nil {
			return waitErr
		}
		if overlayDevice == dataDevice || overlayDevice == volumesDevice {
			return fmt.Errorf("overlay device resolves to a writable block device")
		}
	}
	data, err := prepareFilesystem(ctx, options.DataDevice, options.DataMount, options.FreshIdentity, options)
	if err != nil {
		return fmt.Errorf("prepare data filesystem: %w", err)
	}
	volumes, err := prepareFilesystem(ctx, options.VolumesDevice, options.VolumesMount, "", options)
	if err != nil {
		return fmt.Errorf("prepare Docker-volume filesystem: %w", err)
	}
	bindTarget := filepath.Join(options.DataMount, "docker", "volumes")
	if err := safeDirectory(bindTarget, 0o755); err != nil {
		return err
	}
	if mounted, err := mountSource(ctx, bindTarget); err != nil {
		return err
	} else if mounted == "" {
		if err := runHostCommand(ctx, options, "mount", "--bind", options.VolumesMount, bindTarget); err != nil {
			return err
		}
	}
	if same, err := sameMountIdentity(ctx, bindTarget, options.VolumesMount); err != nil {
		return err
	} else if !same {
		return fmt.Errorf("%s is not a bind mount of %s", bindTarget, options.VolumesMount)
	}
	if options.OverlayDevice != "" {
		overlayDevice, err := waitForBlockDevice(ctx, options.OverlayDevice, options)
		if err != nil {
			return err
		}
		if err := safeDirectory(options.OverlayMount, 0o755); err != nil {
			return err
		}
		if mounted, err := mountSource(ctx, options.OverlayMount); err != nil {
			return err
		} else if mounted == "" {
			if err := runHostCommand(ctx, options, "mount", "-o", "ro", options.OverlayDevice, options.OverlayMount); err != nil {
				return err
			}
		}
		if err := requireMountDevice(ctx, options.OverlayMount, overlayDevice); err != nil {
			return err
		}
	}
	if err := persistFilesystems(ctx, options, data.providerMount, volumes.providerMount); err != nil {
		return err
	}
	requiredTargets := []string{options.DataMount, options.VolumesMount, bindTarget}
	if options.OverlayDevice != "" {
		requiredTargets = append(requiredTargets, options.OverlayMount)
	}
	for _, target := range requiredTargets {
		if source, err := mountSource(ctx, target); err != nil || source == "" {
			return fmt.Errorf("required mount is unavailable: %s", target)
		}
	}
	if options.ReadyMarker != "" {
		if err := mkdirAllNoSymlink(filepath.Dir(options.ReadyMarker), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(options.ReadyMarker, nil, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type preparedFilesystem struct{ providerMount string }

func prepareFilesystem(ctx context.Context, devicePath, mountPath, freshIdentity string, options FilesystemOptions) (preparedFilesystem, error) {
	device, err := waitForBlockDevice(ctx, devicePath, options)
	if err != nil {
		return preparedFilesystem{}, err
	}
	providerMount, err := digitalOceanMount(devicePath)
	if err != nil {
		return preparedFilesystem{}, err
	}
	if providerMount != "" && commandExists("udevadm") {
		if err := runHostCommand(ctx, options, "udevadm", "settle", "--timeout="+strconv.Itoa(int(options.AutomountWait.Seconds()))); err != nil {
			return preparedFilesystem{}, fmt.Errorf("settle DigitalOcean automount: %w", err)
		}
	}
	if err := safeDirectory(mountPath, 0o755); err != nil {
		return preparedFilesystem{}, err
	}
	targets, err := deviceMountTargets(ctx, device)
	if err != nil {
		return preparedFilesystem{}, err
	}
	if len(targets) == 0 && providerMount != "" {
		if err := startDigitalOceanMount(ctx, devicePath, providerMount, options); err != nil {
			return preparedFilesystem{}, err
		}
		targets, err = deviceMountTargets(ctx, device)
		if err != nil {
			return preparedFilesystem{}, err
		}
	}
	if len(targets) > 1 {
		return preparedFilesystem{}, fmt.Errorf("%s is mounted at multiple targets: %s", device, strings.Join(targets, " "))
	}
	alreadyMounted := false
	if len(targets) == 1 {
		switch targets[0] {
		case mountPath:
			if err := requireMountDevice(ctx, mountPath, device); err != nil {
				return preparedFilesystem{}, err
			}
			alreadyMounted = true
		case providerMount:
			if providerMount == "" {
				return preparedFilesystem{}, fmt.Errorf("device mounted at unexpected target %s", targets[0])
			}
			if err := requireEmptyDirectory(mountPath); err != nil {
				return preparedFilesystem{}, err
			}
			if err := requireMountDevice(ctx, providerMount, device); err != nil {
				return preparedFilesystem{}, err
			}
			if err := runHostCommand(ctx, options, "mount", "--move", providerMount, mountPath); err != nil {
				return preparedFilesystem{}, err
			}
			if err := requireMountDevice(ctx, mountPath, device); err != nil {
				_ = runHostCommand(ctx, options, "mount", "--move", mountPath, providerMount)
				return preparedFilesystem{}, err
			}
			_ = os.Remove(providerMount)
			alreadyMounted = true
		default:
			return preparedFilesystem{}, fmt.Errorf("%s is mounted at unexpected target %s", device, targets[0])
		}
	}

	filesystemType, status, err := hostCommandOutputStatus(ctx, "blkid", "-p", "-s", "TYPE", "-o", "value", "--", device)
	if err != nil {
		return preparedFilesystem{}, err
	}
	freshPending := false
	switch status {
	case 0:
		if strings.TrimSpace(filesystemType) != "ext4" {
			return preparedFilesystem{}, fmt.Errorf("existing filesystem on %s is %q, expected ext4", device, strings.TrimSpace(filesystemType))
		}
		if !alreadyMounted {
			if err := requireDeviceUnmounted(ctx, device); err != nil {
				return preparedFilesystem{}, err
			}
			_, fsckStatus, fsckErr := hostCommandOutputStatus(ctx, "fsck.ext4", "-f", "-p", "--", device)
			if fsckErr != nil {
				return preparedFilesystem{}, fsckErr
			}
			if fsckStatus != 0 && fsckStatus != 1 {
				return preparedFilesystem{}, fmt.Errorf("filesystem check failed with status %d", fsckStatus)
			}
		}
		if err := runHostCommand(ctx, options, "resize2fs", "--", device); err != nil {
			return preparedFilesystem{}, fmt.Errorf("grow ext4 filesystem: %w", err)
		}
		if freshIdentity != "" {
			label, err := commandOutputStrict(ctx, "e2label", device)
			if err != nil {
				return preparedFilesystem{}, err
			}
			freshPending = strings.TrimSpace(label) == freshFilesystemLabel
		}
	case 2:
		if alreadyMounted {
			return preparedFilesystem{}, fmt.Errorf("mounted device has no filesystem signature: %s", device)
		}
		if err := requireDeviceUnmounted(ctx, device); err != nil {
			return preparedFilesystem{}, err
		}
		args := []string{"-m", "0", "-E", "lazy_itable_init=1,lazy_journal_init=1,nodiscard"}
		if freshIdentity != "" {
			args = append(args, "-L", freshFilesystemLabel)
			freshPending = true
		}
		args = append(args, "--", device)
		if err := runHostCommand(ctx, options, "mkfs.ext4", args...); err != nil {
			return preparedFilesystem{}, err
		}
	default:
		return preparedFilesystem{}, fmt.Errorf("could not inspect filesystem signature on %s (status %d)", device, status)
	}
	if !alreadyMounted {
		if err := requireDeviceUnmounted(ctx, device); err != nil {
			return preparedFilesystem{}, err
		}
		if err := runHostCommand(ctx, options, "mount", "-o", "defaults", "--", device, mountPath); err != nil {
			return preparedFilesystem{}, err
		}
		if err := requireMountDevice(ctx, mountPath, device); err != nil {
			return preparedFilesystem{}, err
		}
	}
	if freshPending {
		if err := publishFreshMarker(mountPath, freshIdentity); err != nil {
			return preparedFilesystem{}, err
		}
		unix.Sync()
		if err := runHostCommand(ctx, options, "e2label", device, ""); err != nil {
			return preparedFilesystem{}, fmt.Errorf("clear pending fresh-filesystem label: %w", err)
		}
		unix.Sync()
	}
	return preparedFilesystem{providerMount: providerMount}, nil
}

func filesystemDefaults(options FilesystemOptions) FilesystemOptions {
	if options.DataMount == "" {
		options.DataMount = "/mnt/disks/data"
	}
	if options.VolumesMount == "" {
		options.VolumesMount = "/mnt/disks/volumes"
	}
	if options.OverlayMount == "" {
		options.OverlayMount = "/mnt/disks/prod-readonly"
	}
	if options.FstabPath == "" {
		options.FstabPath = "/etc/fstab"
	}
	if options.FstabLockPath == "" {
		options.FstabLockPath = "/run/cloud-compose-fstab.lock"
	}
	if options.SystemdDir == "" {
		options.SystemdDir = "/etc/systemd/system"
	}
	if options.DeviceWait == 0 {
		options.DeviceWait = 10 * time.Minute
	}
	if options.AutomountWait == 0 {
		options.AutomountWait = time.Minute
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

func validateFilesystemOptions(options FilesystemOptions) error {
	for name, value := range map[string]string{"data device": options.DataDevice, "volumes device": options.VolumesDevice} {
		if !devicePathPattern.MatchString(value) || filepath.Clean(value) != value {
			return fmt.Errorf("%s is unsafe: %s", name, value)
		}
	}
	if options.DataDevice == options.VolumesDevice {
		return fmt.Errorf("data and Docker-volume devices must be distinct")
	}
	if options.OverlayDevice != "" {
		if !devicePathPattern.MatchString(options.OverlayDevice) || filepath.Clean(options.OverlayDevice) != options.OverlayDevice {
			return fmt.Errorf("overlay device is unsafe: %s", options.OverlayDevice)
		}
		if options.OverlayDevice == options.DataDevice || options.OverlayDevice == options.VolumesDevice {
			return fmt.Errorf("overlay device must be distinct")
		}
	}
	for name, value := range map[string]string{"data mount": options.DataMount, "volumes mount": options.VolumesMount, "overlay mount": options.OverlayMount, "fstab": options.FstabPath, "fstab lock": options.FstabLockPath, "systemd directory": options.SystemdDir} {
		if !safeAbsolutePath(value) {
			return fmt.Errorf("%s path is unsafe: %s", name, value)
		}
	}
	mounts := []string{options.DataMount, options.VolumesMount, options.OverlayMount}
	for index, mount := range mounts {
		for _, other := range mounts[index+1:] {
			if withinRoot(mount, other) || withinRoot(other, mount) {
				return fmt.Errorf("managed mount paths must not overlap: %s and %s", mount, other)
			}
		}
	}
	if options.ReadyMarker != "" && !safeAbsolutePath(options.ReadyMarker) {
		return fmt.Errorf("ready-marker path is unsafe: %s", options.ReadyMarker)
	}
	if options.FreshIdentity != "" && options.FreshIdentity != "fresh" && !freshIdentityPattern.MatchString(options.FreshIdentity) {
		return fmt.Errorf("fresh-filesystem identity is unsafe")
	}
	if options.DeviceWait < time.Second || options.DeviceWait > 10*time.Minute || options.AutomountWait < time.Second || options.AutomountWait > 10*time.Minute {
		return fmt.Errorf("filesystem wait durations must be between one second and ten minutes")
	}
	return nil
}

func waitForBlockDevice(ctx context.Context, path string, options FilesystemOptions) (string, error) {
	deadline := time.Now().Add(options.DeviceWait)
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			var stat unix.Stat_t
			if unix.Stat(resolved, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFBLK {
				return resolved, nil
			}
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("block device did not appear before deadline: %s", path)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func digitalOceanMount(device string) (string, error) {
	match := digitalOceanPattern.FindStringSubmatch(device)
	if match == nil {
		return "", nil
	}
	return "/mnt/" + strings.ReplaceAll(match[1], "-", "_"), nil
}

func deviceMountTargets(ctx context.Context, device string) ([]string, error) {
	output, status, err := hostCommandOutputStatus(ctx, "findmnt", "-rn", "-o", "TARGET", "--source", device)
	if err != nil {
		return nil, err
	}
	if status == 1 {
		return nil, nil
	}
	if status != 0 {
		return nil, fmt.Errorf("findmnt failed with status %d", status)
	}
	return strings.Fields(output), nil
}

func mountSource(ctx context.Context, target string) (string, error) {
	output, status, err := hostCommandOutputStatus(ctx, "findmnt", "-rn", "-o", "SOURCE", "--mountpoint", target)
	if err != nil {
		return "", err
	}
	if status == 1 {
		return "", nil
	}
	if status != 0 {
		return "", fmt.Errorf("findmnt failed with status %d", status)
	}
	return strings.TrimSpace(output), nil
}

func sameMountIdentity(ctx context.Context, left, right string) (bool, error) {
	identity := func(target string) (string, error) {
		output, status, err := hostCommandOutputStatus(ctx, "findmnt", "-rn", "-o", "SOURCE,FSROOT", "--mountpoint", target)
		if err != nil {
			return "", err
		}
		if status != 0 {
			return "", fmt.Errorf("required mount is unavailable: %s", target)
		}
		return strings.TrimSpace(output), nil
	}
	leftIdentity, err := identity(left)
	if err != nil {
		return false, err
	}
	rightIdentity, err := identity(right)
	if err != nil {
		return false, err
	}
	return leftIdentity == rightIdentity, nil
}

func requireMountDevice(ctx context.Context, target, device string) error {
	source, err := mountSource(ctx, target)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || resolved != device {
		return fmt.Errorf("%s is mounted from %s, expected %s", target, source, device)
	}
	return nil
}

func requireDeviceUnmounted(ctx context.Context, device string) error {
	targets, err := deviceMountTargets(ctx, device)
	if err != nil {
		return err
	}
	if len(targets) != 0 {
		return fmt.Errorf("%s became mounted before offline mutation: %s", device, strings.Join(targets, " "))
	}
	return nil
}

func startDigitalOceanMount(ctx context.Context, device, providerMount string, options FilesystemOptions) error {
	unit := "mnt-" + filepath.Base(providerMount) + ".mount"
	path := filepath.Join(options.SystemdDir, unit)
	contents, err := readSingleLinkFile(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read DigitalOcean mount unit: %w", err)
	}
	if !unitHasExactMount(string(contents), device, providerMount) {
		return fmt.Errorf("unexpected DigitalOcean mount unit: %s", path)
	}
	if err := runHostCommand(ctx, options, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return runHostCommand(ctx, options, "systemctl", "start", "--", unit)
}

func unitHasExactMount(contents, device, target string) bool {
	var what, where []string
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "What=") {
			what = append(what, strings.TrimPrefix(line, "What="))
		}
		if strings.HasPrefix(line, "Where=") {
			where = append(where, strings.TrimPrefix(line, "Where="))
		}
	}
	return len(what) == 1 && len(where) == 1 && what[0] == device && where[0] == target
}

func persistFilesystems(ctx context.Context, options FilesystemOptions, dataProvider, volumesProvider string) error {
	if err := os.MkdirAll(filepath.Dir(options.FstabPath), 0o755); err != nil {
		return err
	}
	lock, err := AcquireLock(options.FstabLockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	file, err := os.OpenFile(options.FstabPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return err
	}
	reconciled, err := reconcileFstab(string(contents), options, dataProvider, volumesProvider)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{options.DataDevice, dataProvider}, {options.VolumesDevice, volumesProvider}} {
		if err := retireDigitalOceanUnit(ctx, pair[0], pair[1], options); err != nil {
			return err
		}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, _ := info.Sys().(*syscall.Stat_t)
	tmp, err := os.CreateTemp(filepath.Dir(options.FstabPath), ".fstab.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.WriteString(tmp, reconciled); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if stat != nil {
		if err := tmp.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, options.FstabPath); err != nil {
		return err
	}
	if commandExists("systemctl") {
		return runHostCommand(ctx, options, "systemctl", "daemon-reload")
	}
	return nil
}

func reconcileFstab(contents string, options FilesystemOptions, dataProvider, volumesProvider string) (string, error) {
	managed := false
	var kept []string
	for _, line := range strings.Split(strings.TrimSuffix(contents, "\n"), "\n") {
		if line == fstabBeginMarker {
			if managed {
				return "", fmt.Errorf("nested managed fstab block")
			}
			managed = true
			continue
		}
		if line == fstabEndMarker {
			if !managed {
				return "", fmt.Errorf("orphan managed fstab end marker")
			}
			managed = false
			continue
		}
		if managed {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			source, target := fields[0], fields[1]
			if (dataProvider != "" && target == dataProvider && source == options.DataDevice) || (volumesProvider != "" && target == volumesProvider && source == options.VolumesDevice) {
				continue
			}
			if target == dataProvider && dataProvider != "" || target == volumesProvider && volumesProvider != "" || target == options.DataMount || target == options.VolumesMount || target == filepath.Join(options.DataMount, "docker", "volumes") || target == options.OverlayMount {
				return "", fmt.Errorf("unmanaged fstab entry conflicts with Cloud Compose target %s", target)
			}
		}
		kept = append(kept, line)
	}
	if managed {
		return "", fmt.Errorf("unterminated managed fstab block")
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	lines := append(kept, fstabBeginMarker,
		fmt.Sprintf("%s\t%s\text4\tdefaults,nofail,x-systemd.device-timeout=120s\t0\t2", options.DataDevice, options.DataMount),
		fmt.Sprintf("%s\t%s\text4\tdefaults,nofail,x-systemd.device-timeout=120s\t0\t2", options.VolumesDevice, options.VolumesMount),
		fmt.Sprintf("%s\t%s\tnone\tbind,nofail,x-systemd.requires=%s\t0\t0", options.VolumesMount, filepath.Join(options.DataMount, "docker", "volumes"), options.VolumesMount))
	if options.OverlayDevice != "" {
		lines = append(lines, fmt.Sprintf("%s\t%s\text4\tro,nofail,x-systemd.device-timeout=120s\t0\t2", options.OverlayDevice, options.OverlayMount))
	}
	lines = append(lines, fstabEndMarker)
	return strings.Join(lines, "\n") + "\n", nil
}

func retireDigitalOceanUnit(ctx context.Context, device, providerMount string, options FilesystemOptions) error {
	if providerMount == "" {
		return nil
	}
	unit := "mnt-" + filepath.Base(providerMount) + ".mount"
	path := filepath.Join(options.SystemdDir, unit)
	contents, err := readSingleLinkFile(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !unitHasExactMount(string(contents), device, providerMount) {
		return fmt.Errorf("unexpected DigitalOcean mount unit: %s", path)
	}
	links, err := filepath.Glob(filepath.Join(options.SystemdDir, "*.wants", unit))
	if err != nil {
		return err
	}
	for _, link := range links {
		resolved, err := filepath.EvalSymlinks(link)
		if err != nil || resolved != path {
			return fmt.Errorf("unexpected DigitalOcean mount-unit link: %s", link)
		}
	}
	if commandExists("systemctl") {
		if err := runHostCommand(ctx, options, "systemctl", "disable", "--now", "--", unit); err != nil {
			return err
		}
	}
	for _, link := range links {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func publishFreshMarker(root, identity string) error {
	return publishFreshMarkerOwned(root, identity, 0, 0)
}

func publishFreshMarkerOwned(root, identity string, uid, gid uint32) error {
	if identity != "fresh" && !freshIdentityPattern.MatchString(identity) {
		return fmt.Errorf("unsafe fresh-filesystem identity")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		return fmt.Errorf("fresh-filesystem root is not pristine: %s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "lost+found" && entry.Name() != ".cloud-compose" {
			return fmt.Errorf("fresh-filesystem root contains unexpected data: %s", entry.Name())
		}
		if entry.Name() == "lost+found" {
			child, err := os.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil || len(child) != 0 {
				return fmt.Errorf("lost+found is not empty")
			}
		}
	}
	directory := filepath.Join(root, ".cloud-compose")
	marker := filepath.Join(directory, "fresh-filesystem")
	if existing, err := readSingleLinkFile(marker, 128); err == nil {
		if string(existing) != identity+"\n" {
			return fmt.Errorf("fresh-filesystem marker belongs to another disk incarnation")
		}
		return requireOwnedFile(marker, uid, gid, 0o600)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedBy(info, uid, gid) {
		return fmt.Errorf("unsafe fresh-filesystem marker directory: %s", directory)
	} else if contents, err := os.ReadDir(directory); err != nil || len(contents) != 0 {
		return fmt.Errorf("fresh-filesystem marker directory is not empty: %s", directory)
	}
	if info, err := os.Lstat(directory); err != nil {
		return err
	} else if !ownedBy(info, uid, gid) {
		if err := os.Chown(directory, int(uid), int(gid)); err != nil {
			return err
		}
	}
	if err := writeAtomic(marker, []byte(identity+"\n"), 0o600); err != nil {
		return err
	}
	return requireOwnedFile(marker, uid, gid, 0o600)
}

func requireOwnedFile(path string, uid, gid uint32, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return fmt.Errorf("unsafe file: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid || stat.Nlink != 1 {
		return fmt.Errorf("unsafe file ownership: %s", path)
	}
	return nil
}

func ownedBy(info os.FileInfo, uid, gid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid && stat.Gid == gid
}

func safeDirectory(path string, mode os.FileMode) error {
	if !safeAbsolutePath(path) {
		return fmt.Errorf("unsafe directory: %s", path)
	}
	if err := mkdirAllNoSymlink(path, mode); err != nil {
		return err
	}
	return requireDirectory(path)
}

func mkdirAllNoSymlink(path string, mode os.FileMode) error {
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe directory component: %s", current)
		}
	}
	return nil
}

func safeAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && path != "/" && !strings.ContainsAny(path, "\r\n") && filepath.Clean(path) == path
}

func requireEmptyDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("target mount directory is not empty: %s", path)
	}
	return nil
}

func commandExists(name string) bool { _, err := exec.LookPath(name); return err == nil }

func runHostCommand(ctx context.Context, options FilesystemOptions, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func commandOutputStrict(ctx context.Context, name string, args ...string) (string, error) {
	output, status, err := hostCommandOutputStatus(ctx, name, args...)
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("%s failed with status %d", name, status)
	}
	return output, nil
}

func hostCommandOutputStatus(ctx context.Context, name string, args ...string) (string, int, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(output), exit.ExitCode(), nil
	}
	return string(output), -1, err
}
