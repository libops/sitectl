package hostruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// SecureRuntimeOptions controls the root trust boundary around host programs.
type SecureRuntimeOptions struct {
	Home       string
	TrustedUID uint32
	TrustedGID uint32
	RuntimeGID uint32
}

// SecureRuntimeHome verifies and normalizes the privileged Cloud Compose runtime.
func SecureRuntimeHome(options SecureRuntimeOptions) error {
	if os.Geteuid() != 0 && options.TrustedUID == 0 {
		return fmt.Errorf("cloud-compose runtime security must run as root")
	}
	if options.Home == "" {
		options.Home = "/home/cloud-compose"
	}
	if options.RuntimeGID == 0 {
		options.RuntimeGID = runtimeID
	}
	if !safeAbsolutePath(options.Home) {
		return fmt.Errorf("unsafe Cloud Compose runtime home: %s", options.Home)
	}
	home, err := os.Lstat(options.Home)
	if err != nil || !home.IsDir() || home.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cloud-compose runtime home is missing or redirected")
	}
	if err := os.Chown(options.Home, int(options.TrustedUID), int(options.TrustedGID)); err != nil {
		return err
	}
	if err := os.Chmod(options.Home, 0o755); err != nil {
		return err
	}
	required := []string{"run.sh", "profile.sh", "host-conf.sh", "lifecycle-entrypoint.sh", "deploy-rollout.sh", "init", "up", "down", "rollout"}
	for _, name := range required {
		if err := requireTrustedRegular(filepath.Join(options.Home, name), options.TrustedUID); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(options.Home)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(options.Home, name)
		mode := os.FileMode(0)
		switch {
		case strings.HasSuffix(name, ".sh"):
			mode = 0o755
		case strings.HasSuffix(name, ".jq"), strings.HasSuffix(name, ".awk"):
			mode = 0o644
		case name == "init" || name == "up" || name == "down" || name == "rollout":
			mode = 0o755
		}
		if mode == 0 {
			continue
		}
		if err := requireTrustedRegular(path, options.TrustedUID); err != nil {
			return err
		}
		if err := os.Chown(path, int(options.TrustedUID), int(options.TrustedGID)); err != nil {
			return err
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	for _, name := range []string{".env", "compose-projects.json", "application-env.json", "managed-runtime-artifacts.tsv"} {
		path := filepath.Join(options.Home, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if err := requireTrustedRegular(path, options.TrustedUID); err != nil {
			return err
		}
		if err := os.Chown(path, int(options.TrustedUID), int(options.RuntimeGID)); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o640); err != nil {
			return err
		}
	}
	bin := filepath.Join(options.Home, "bin")
	if err := mkdirAllNoSymlink(bin, 0o755); err != nil {
		return err
	}
	if err := os.Chown(bin, int(options.TrustedUID), int(options.TrustedGID)); err != nil {
		return err
	}
	return os.Chmod(bin, 0o755)
}

func requireTrustedRegular(path string, uid uint32) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("cloud-compose runtime file is missing or unsafe: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Nlink != 1 {
		return fmt.Errorf("cloud-compose runtime file is not trusted: %s", path)
	}
	return nil
}
