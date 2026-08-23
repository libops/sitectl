package hostruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const runtimeID = 770077

// ConfigureOptions controls root-owned host account and runtime preparation.
type ConfigureOptions struct {
	Provider        string
	RuntimeHome     string
	DataRoot        string
	VolumesRoot     string
	EnvironmentFile string
	InternalEnabled bool
	MetadataURL     string
	HTTPClient      *http.Client
	Stdout          io.Writer
	Stderr          io.Writer
}

// ConfigureHost normalizes the managed account, runtime paths, and provider metadata.
func ConfigureHost(ctx context.Context, options ConfigureOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("host configuration must run as root")
	}
	if options.RuntimeHome == "" {
		options.RuntimeHome = "/home/cloud-compose"
	}
	if options.DataRoot == "" {
		options.DataRoot = "/mnt/disks/data"
	}
	if options.VolumesRoot == "" {
		options.VolumesRoot = "/mnt/disks/volumes"
	}
	if options.EnvironmentFile == "" {
		options.EnvironmentFile = filepath.Join(options.RuntimeHome, ".env")
	}
	if err := convergeRuntimeAccount(ctx, options.Stdout, options.Stderr); err != nil {
		return err
	}
	if options.Provider == "gcp" {
		if err := reconcileGCPMetadata(ctx, options); err != nil {
			return err
		}
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{
		{options.RuntimeHome, 0o755, 0, 0},
		{filepath.Join(options.RuntimeHome, "apps"), 0o750, runtimeID, runtimeID},
		{filepath.Join(options.RuntimeHome, "state"), 0o750, runtimeID, runtimeID},
		{filepath.Join(options.RuntimeHome, ".sitectl"), 0o750, runtimeID, runtimeID},
		{filepath.Join(options.RuntimeHome, ".cache"), 0o750, runtimeID, runtimeID},
		{filepath.Join(options.RuntimeHome, ".config"), 0o750, runtimeID, runtimeID},
		{filepath.Join(options.RuntimeHome, ".local"), 0o750, runtimeID, runtimeID},
		{options.DataRoot, 0o1775, 0, runtimeID},
		{options.VolumesRoot, 0o775, runtimeID, runtimeID},
		{filepath.Join(options.DataRoot, "libops"), 0o775, runtimeID, runtimeID},
	} {
		if err := secureDirectory(directory.path, directory.mode, directory.uid, directory.gid); err != nil {
			return err
		}
	}
	bin := filepath.Join(options.RuntimeHome, "bin")
	if err := requireDirectory(bin); err != nil {
		return fmt.Errorf("managed command directory: %w", err)
	}
	if err := os.Chown(bin, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(options.RuntimeHome)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".sh") {
			path := filepath.Join(options.RuntimeHome, entry.Name())
			if err := os.Chown(path, 0, 0); err != nil {
				return err
			}
			if err := os.Chmod(path, 0o755); err != nil {
				return err
			}
		}
	}
	for _, input := range []string{".env", "compose-projects.json", "application-env.json"} {
		path := filepath.Join(options.RuntimeHome, input)
		if _, err := os.Stat(path); err == nil {
			if err := os.Chown(path, 0, runtimeID); err != nil {
				return err
			}
			if err := os.Chmod(path, 0o640); err != nil {
				return err
			}
		}
	}
	if options.InternalEnabled {
		directory := filepath.Join(options.DataRoot, "libops-internal")
		if err := secureDirectory(directory, 0o755, runtimeID, runtimeID); err != nil {
			return err
		}
		contents, err := os.ReadFile(options.EnvironmentFile)
		if err != nil {
			return err
		}
		if err := installBytes(contents, filepath.Join(directory, ".env"), strconv.Itoa(runtimeID), strconv.Itoa(runtimeID), 0o640); err != nil {
			return err
		}
	}
	return nil
}

func convergeRuntimeAccount(ctx context.Context, stdout, stderr io.Writer) error {
	commands := [][]string{{"groupadd", "--force", "docker"}}
	if group, err := user.LookupGroup("cloud-compose"); err != nil {
		commands = append(commands, []string{"groupadd", "--gid", strconv.Itoa(runtimeID), "cloud-compose"})
	} else if group.Gid != strconv.Itoa(runtimeID) {
		commands = append(commands, []string{"groupmod", "--gid", strconv.Itoa(runtimeID), "cloud-compose"})
	}
	if account, err := user.Lookup("cloud-compose"); err != nil {
		commands = append(commands, []string{"useradd", "--create-home", "--shell", "/bin/bash", "--uid", strconv.Itoa(runtimeID), "--gid", strconv.Itoa(runtimeID), "--groups", "docker", "cloud-compose"})
	} else {
		args := []string{"usermod", "--append", "--groups", "docker"}
		if account.Uid != strconv.Itoa(runtimeID) {
			args = append(args, "--uid", strconv.Itoa(runtimeID))
		}
		if account.Gid != strconv.Itoa(runtimeID) {
			args = append(args, "--gid", strconv.Itoa(runtimeID))
		}
		commands = append(commands, append(args, "cloud-compose"))
	}
	for _, args := range commands {
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func reconcileGCPMetadata(ctx context.Context, options ConfigureOptions) error {
	if options.MetadataURL == "" {
		options.MetadataURL = "http://metadata.google.internal/computeMetadata/v1/?recursive=true"
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, options.MetadataURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := options.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("read GCP metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("GCP metadata returned HTTP %d", response.StatusCode)
	}
	var metadata struct {
		Instance struct {
			NetworkInterfaces []struct {
				IP            string `json:"ip"`
				AccessConfigs []struct {
					ExternalIP string `json:"externalIp"`
				} `json:"accessConfigs"`
			} `json:"networkInterfaces"`
		} `json:"instance"`
	}
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil || len(metadata.Instance.NetworkInterfaces) == 0 || len(metadata.Instance.NetworkInterfaces[0].AccessConfigs) == 0 {
		return fmt.Errorf("GCP metadata has no primary network interface")
	}
	if err := SetRuntimeEnv(options.EnvironmentFile, "GCP_PUBLIC_IP", metadata.Instance.NetworkInterfaces[0].AccessConfigs[0].ExternalIP); err != nil {
		return err
	}
	return SetRuntimeEnv(options.EnvironmentFile, "GCP_PRIVATE_IP", metadata.Instance.NetworkInterfaces[0].IP)
}

func secureDirectory(path string, mode os.FileMode, uid, gid int) error {
	if err := mkdirAllNoSymlink(path, mode); err != nil {
		return err
	}
	if err := requireDirectory(path); err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe directory: %s", path)
	}
	return nil
}
