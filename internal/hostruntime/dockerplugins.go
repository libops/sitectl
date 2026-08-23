package hostruntime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const maximumDockerPluginBytes = 256 << 20

var dockerPluginVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[.-][0-9A-Za-z.-]+)?$`)

// DockerPluginOptions controls verified Docker CLI plugin installation.
type DockerPluginOptions struct {
	Directory      string
	ComposeVersion string
	BuildxVersion  string
	Architecture   string
	ReleaseBase    string
	Client         *http.Client
	AllowHTTP      bool
}

type dockerPlugin struct{ name, version, asset, path string }

// InstallDockerPlugins installs checksum-verified Compose and Buildx executables.
func InstallDockerPlugins(ctx context.Context, options DockerPluginOptions) error {
	if options.Directory == "" {
		options.Directory = "/usr/local/lib/docker/cli-plugins"
	}
	// renovate: datasource=github-releases depName=docker-compose packageName=docker/compose versioning=semver
	if options.ComposeVersion == "" {
		options.ComposeVersion = "v5.3.1"
	}
	// renovate: datasource=github-releases depName=docker-buildx packageName=docker/buildx versioning=semver
	if options.BuildxVersion == "" {
		options.BuildxVersion = "v0.35.0"
	}
	if options.Architecture == "" {
		options.Architecture = runtime.GOARCH
	}
	if options.ReleaseBase == "" {
		options.ReleaseBase = "https://github.com"
	}
	if options.Client == nil {
		options.Client = secureHTTPClient(options.AllowHTTP)
	}
	if !safeAbsolutePath(options.Directory) {
		return fmt.Errorf("unsafe Docker CLI plugin directory: %s", options.Directory)
	}
	if err := validateDownloadBase(options.ReleaseBase, options.AllowHTTP); err != nil {
		return err
	}
	if !dockerPluginVersionPattern.MatchString(options.ComposeVersion) || !dockerPluginVersionPattern.MatchString(options.BuildxVersion) {
		return fmt.Errorf("docker CLI plugin versions must be exact release tags")
	}
	composeArch, buildxArch, err := dockerPluginArchitectures(options.Architecture)
	if err != nil {
		return err
	}
	if err := mkdirAllNoSymlink(options.Directory, 0o755); err != nil {
		return err
	}
	plugins := []dockerPlugin{
		{name: "docker-compose", version: options.ComposeVersion, asset: "docker-compose-linux-" + composeArch, path: filepath.Join(options.Directory, "docker-compose")},
		{name: "docker-buildx", version: options.BuildxVersion, asset: "buildx-" + options.BuildxVersion + ".linux-" + buildxArch, path: filepath.Join(options.Directory, "docker-buildx")},
	}
	for _, plugin := range plugins {
		if err := installDockerPlugin(ctx, options, plugin); err != nil {
			return err
		}
	}
	return nil
}

func dockerPluginArchitectures(architecture string) (string, string, error) {
	switch architecture {
	case "amd64", "x86_64":
		return "x86_64", "amd64", nil
	case "arm64", "aarch64":
		return "aarch64", "arm64", nil
	default:
		return "", "", fmt.Errorf("unsupported Docker CLI plugin architecture: %s", architecture)
	}
}

func installDockerPlugin(ctx context.Context, options DockerPluginOptions, plugin dockerPlugin) error {
	project := "docker/compose"
	if plugin.name == "docker-buildx" {
		project = "docker/buildx"
	}
	base := strings.TrimSuffix(options.ReleaseBase, "/") + "/" + project + "/releases/download/" + plugin.version
	manifest, err := downloadDockerPlugin(ctx, options, base+"/checksums.txt", 4<<20)
	if err != nil {
		return fmt.Errorf("download %s checksums: %w", plugin.name, err)
	}
	expected, err := uniqueReleaseChecksum(string(manifest), plugin.asset)
	if err != nil {
		return fmt.Errorf("%s checksum: %w", plugin.name, err)
	}
	if digest, _, err := digestFile(plugin.path); err == nil && digest == expected {
		return nil
	}
	download, err := downloadDockerPlugin(ctx, options, base+"/"+plugin.asset, maximumDockerPluginBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", plugin.name, err)
	}
	digest := sha256.Sum256(download)
	if fmt.Sprintf("%x", digest[:]) != expected {
		return fmt.Errorf("%s failed checksum verification", plugin.name)
	}
	if info, err := os.Lstat(plugin.path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("unsafe Docker CLI plugin target: %s", plugin.path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(plugin.path, download, 0o755)
}

func downloadDockerPlugin(ctx context.Context, options DockerPluginOptions, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := options.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 || response.Request == nil || response.Request.URL == nil || !allowedRuntimeURLScheme(response.Request.URL.Scheme, options.AllowHTTP) {
		return nil, fmt.Errorf("unexpected HTTP response %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return contents, nil
}

func uniqueReleaseChecksum(manifest, asset string) (string, error) {
	var selected string
	scanner := bufio.NewScanner(strings.NewReader(manifest))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset || !artifactSHApattern.MatchString(fields[0]) {
			continue
		}
		if selected != "" {
			return "", fmt.Errorf("duplicate checksum for %s", asset)
		}
		selected = fields[0]
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if selected == "" {
		return "", fmt.Errorf("missing checksum for %s", asset)
	}
	return selected, nil
}
