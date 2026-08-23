package hostruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const maximumRuntimeArchiveBytes = 256 << 20

var (
	runtimePackagePattern = regexp.MustCompile(`^sitectl(?:-[a-z0-9]+)*$`)
	runtimeVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	runtimeOwnerPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// RuntimeInstallOptions controls installation of the shared sitectl package set.
type RuntimeInstallOptions struct {
	StateDir     string
	PublishedDir string
	Packages     []string
	Versions     map[string]string
	Fallback     string
	GitHubOwner  string
	ReleaseBase  string
	APIBase      string
	Architecture string
	Client       *http.Client
	AllowHTTP    bool
	TrustedUID   int
	Artifact     ArtifactInstallOptions
}

type runtimePackage struct {
	Name, Version, Digest string
	Binary                []byte
}

// InstallManagedRuntime stages and verifies the complete package set before
// atomically publishing it as one immutable generation.
func InstallManagedRuntime(ctx context.Context, options RuntimeInstallOptions) error {
	if options.StateDir == "" {
		options.StateDir = "/mnt/disks/data/libops-managed"
	}
	if options.PublishedDir == "" {
		options.PublishedDir = "/home/cloud-compose/bin"
	}
	if options.Fallback == "" {
		options.Fallback = "latest"
	}
	if options.GitHubOwner == "" {
		options.GitHubOwner = "libops"
	}
	if options.ReleaseBase == "" {
		options.ReleaseBase = "https://github.com"
	}
	if options.APIBase == "" {
		options.APIBase = "https://api.github.com"
	}
	if options.Architecture == "" {
		var err error
		options.Architecture, err = releaseArchitecture(runtime.GOARCH)
		if err != nil {
			return err
		}
	}
	if options.Client == nil {
		options.Client = secureHTTPClient(options.AllowHTTP)
	}
	packages, err := normalizeRuntimePackages(options.Packages)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(packages))
	for _, name := range packages {
		selected[name] = true
	}
	for name := range options.Versions {
		if !selected[name] {
			return fmt.Errorf("version configured for unselected sitectl package %q", name)
		}
	}
	if !runtimeOwnerPattern.MatchString(options.GitHubOwner) {
		return fmt.Errorf("invalid GitHub owner %q", options.GitHubOwner)
	}
	if options.Architecture != "x86_64" && options.Architecture != "arm64" {
		return fmt.Errorf("unsupported release architecture %q", options.Architecture)
	}
	if err := validateDownloadBase(options.ReleaseBase, options.AllowHTTP); err != nil {
		return fmt.Errorf("release base: %w", err)
	}
	if err := validateDownloadBase(options.APIBase, options.AllowHTTP); err != nil {
		return fmt.Errorf("API base: %w", err)
	}
	if err := ensureSafeDirectory(options.StateDir, 0o700); err != nil {
		return err
	}
	if err := requireTrustedRuntimeDirectory(options.StateDir, 0o700, options.TrustedUID); err != nil {
		return err
	}
	lock, err := AcquireLock(filepath.Join(options.StateDir, "runtime.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := ensureSafeDirectory(filepath.Join(options.StateDir, "generations"), 0o700); err != nil {
		return err
	}
	if err := ensureSafeDirectory(options.PublishedDir, 0o755); err != nil {
		return err
	}
	if err := requireTrustedRuntimeDirectory(options.PublishedDir, 0o755, options.TrustedUID); err != nil {
		return err
	}
	if desiredVersions, exact := exactRuntimeVersions(packages, options); exact && activeRuntimeMatches(packages, desiredVersions, options) {
		return installRuntimeArtifacts(ctx, options)
	}
	staged, err := downloadRuntimePackages(ctx, packages, options)
	if err != nil {
		return err
	}
	if err := publishRuntimeGeneration(staged, options); err != nil {
		return err
	}
	return installRuntimeArtifacts(ctx, options)
}

func installRuntimeArtifacts(ctx context.Context, options RuntimeInstallOptions) error {
	if options.Artifact.Manifest == "" {
		options.Artifact.Manifest = "/home/cloud-compose/managed-runtime-artifacts.tsv"
	}
	if options.Artifact.StateDir == "" {
		options.Artifact.StateDir = filepath.Join(options.StateDir, "artifacts")
	}
	return InstallManagedArtifacts(ctx, options.Artifact)
}

func exactRuntimeVersions(packages []string, options RuntimeInstallOptions) (map[string]string, bool) {
	versions := make(map[string]string, len(packages))
	for _, name := range packages {
		version := options.Versions[name]
		if version == "" {
			version = options.Fallback
		}
		if version == "latest" || !runtimeVersionPattern.MatchString(version) {
			return nil, false
		}
		versions[name] = version
	}
	return versions, true
}

func activeRuntimeMatches(packages []string, versions map[string]string, options RuntimeInstallOptions) bool {
	current := filepath.Join(options.StateDir, "current")
	target, err := os.Readlink(current)
	if err != nil || !withinRoot(target, filepath.Join(options.StateDir, "generations")) {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(target, "versions.json"))
	if err != nil {
		return false
	}
	installed := map[string]string{}
	if json.Unmarshal(contents, &installed) != nil || len(installed) != len(versions) {
		return false
	}
	desired := make(map[string]bool, len(packages))
	for _, name := range packages {
		desired[name] = true
		if installed[name] != versions[name] {
			return false
		}
		digestBytes, err := os.ReadFile(filepath.Join(target, name+".sha256"))
		digest := strings.TrimSpace(string(digestBytes))
		if err != nil || !artifactSHApattern.MatchString(digest) {
			return false
		}
		actual, _, err := digestFile(filepath.Join(target, name))
		if err != nil || actual != digest {
			return false
		}
		info, err := os.Lstat(filepath.Join(target, name))
		metadata, ok := infoSysStat(info, err)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 || int(metadata.Uid) != options.TrustedUID || metadata.Nlink != 1 {
			return false
		}
		link, err := os.Readlink(filepath.Join(options.PublishedDir, name))
		if err != nil || link != filepath.Join(current, name) {
			return false
		}
	}
	entries, err := os.ReadDir(options.PublishedDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if runtimePackagePattern.MatchString(entry.Name()) && !desired[entry.Name()] {
			return false
		}
	}
	return true
}

func infoSysStat(info os.FileInfo, err error) (*syscall.Stat_t, bool) {
	if err != nil || info == nil {
		return nil, false
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	return metadata, ok
}

func normalizeRuntimePackages(input []string) ([]string, error) {
	seen := map[string]bool{"sitectl": true}
	packages := []string{"sitectl"}
	for _, name := range input {
		if !runtimePackagePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid sitectl package %q", name)
		}
		if !seen[name] {
			seen[name] = true
			packages = append(packages, name)
		}
	}
	sort.Strings(packages[1:])
	return packages, nil
}

func downloadRuntimePackages(ctx context.Context, names []string, options RuntimeInstallOptions) ([]runtimePackage, error) {
	packages := make([]runtimePackage, 0, len(names))
	for _, name := range names {
		version := options.Versions[name]
		if version == "" {
			version = options.Fallback
		}
		if version == "latest" {
			var err error
			version, err = latestRuntimeVersion(ctx, name, options)
			if err != nil {
				return nil, err
			}
		}
		if !runtimeVersionPattern.MatchString(version) {
			return nil, fmt.Errorf("invalid version %q for package %q", version, name)
		}
		archiveName := fmt.Sprintf("%s_Linux_%s.tar.gz", name, options.Architecture)
		base := fmt.Sprintf("%s/%s/%s/releases/download/%s", strings.TrimRight(options.ReleaseBase, "/"), options.GitHubOwner, name, version)
		archive, err := downloadRuntimeFile(ctx, options.Client, base+"/"+archiveName, maximumRuntimeArchiveBytes)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
		checksums, err := downloadRuntimeFile(ctx, options.Client, base+"/checksums.txt", 4<<20)
		if err != nil {
			return nil, fmt.Errorf("download %s checksums: %w", name, err)
		}
		expected, err := selectRuntimeChecksum(checksums, archiveName)
		if err != nil {
			return nil, fmt.Errorf("verify %s checksums: %w", name, err)
		}
		actual := sha256.Sum256(archive)
		if hex.EncodeToString(actual[:]) != expected {
			return nil, fmt.Errorf("package %q archive digest does not match checksums.txt", name)
		}
		binary, err := extractRuntimeBinary(archive, name)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", name, err)
		}
		binaryDigest := sha256.Sum256(binary)
		packages = append(packages, runtimePackage{Name: name, Version: version, Digest: hex.EncodeToString(binaryDigest[:]), Binary: binary})
	}
	return packages, nil
}

func latestRuntimeVersion(ctx context.Context, name string, options RuntimeInstallOptions) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", strings.TrimRight(options.APIBase, "/"), options.GitHubOwner, name)
	contents, err := downloadRuntimeFile(ctx, options.Client, endpoint, 1<<20)
	if err != nil {
		return "", fmt.Errorf("resolve latest %s release: %w", name, err)
	}
	var response struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(contents, &response); err != nil || !runtimeVersionPattern.MatchString(response.TagName) {
		return "", fmt.Errorf("latest %s release returned an invalid tag", name)
	}
	return response.TagName, nil
}

func downloadRuntimeFile(ctx context.Context, client *http.Client, address string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("download exceeds %d bytes", maximum)
	}
	return contents, nil
}

func selectRuntimeChecksum(contents []byte, archive string) (string, error) {
	var selected string
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archive {
			continue
		}
		if selected != "" || !artifactSHApattern.MatchString(fields[0]) {
			return "", fmt.Errorf("checksums.txt must contain exactly one valid entry for %s", archive)
		}
		selected = fields[0]
	}
	if selected == "" {
		return "", fmt.Errorf("checksums.txt has no entry for %s", archive)
	}
	return selected, nil
}

func extractRuntimeBinary(contents []byte, name string) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(contents))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var binary []byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name != name {
			continue
		}
		if binary != nil || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maximumRuntimeArchiveBytes {
			return nil, fmt.Errorf("archive must contain exactly one regular top-level %s binary", name)
		}
		binary, err = io.ReadAll(io.LimitReader(reader, maximumRuntimeArchiveBytes+1))
		if err != nil || int64(len(binary)) != header.Size {
			return nil, fmt.Errorf("read package binary: %w", err)
		}
	}
	if binary == nil {
		return nil, fmt.Errorf("archive does not contain %s", name)
	}
	return binary, nil
}

func publishRuntimeGeneration(packages []runtimePackage, options RuntimeInstallOptions) error {
	if err := preflightPublishedRuntime(options.PublishedDir); err != nil {
		return err
	}
	generations := filepath.Join(options.StateDir, "generations")
	stage, err := os.MkdirTemp(generations, ".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	versions := map[string]string{}
	for _, item := range packages {
		if err := os.WriteFile(filepath.Join(stage, item.Name), item.Binary, 0o755); err != nil {
			return err
		}
		versions[item.Name] = item.Version
		if err := os.WriteFile(filepath.Join(stage, item.Name+".sha256"), []byte(item.Digest+"\n"), 0o600); err != nil {
			return err
		}
	}
	metadata, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "versions.json"), append(metadata, '\n'), 0o600); err != nil {
		return err
	}
	generation := filepath.Join(generations, strings.TrimPrefix(filepath.Base(stage), ".stage-")+"-"+fmt.Sprint(time.Now().UnixNano()))
	if err := os.Rename(stage, generation); err != nil {
		return err
	}
	current := filepath.Join(options.StateDir, "current")
	temporary := current + ".new"
	_ = os.Remove(temporary)
	if err := os.Symlink(generation, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, current); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	desired := map[string]bool{}
	for _, item := range packages {
		desired[item.Name] = true
		if err := replaceSymlink(filepath.Join(options.PublishedDir, item.Name), filepath.Join(current, item.Name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(options.PublishedDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if runtimePackagePattern.MatchString(entry.Name()) && !desired[entry.Name()] {
			if err := os.Remove(filepath.Join(options.PublishedDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return removeOldRuntimeGenerations(generations, generation)
}

func preflightPublishedRuntime(published string) error {
	entries, err := os.ReadDir(published)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !runtimePackagePattern.MatchString(entry.Name()) {
			continue
		}
		info, err := os.Lstat(filepath.Join(published, entry.Name()))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("managed package target %q is not a symbolic link", entry.Name())
		}
	}
	return nil
}

func requireTrustedRuntimeDirectory(path string, mode os.FileMode, trustedUID int) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || int(metadata.Uid) != trustedUID {
		return fmt.Errorf("managed runtime directory is not trusted: %s", path)
	}
	return nil
}

func replaceSymlink(path, target string) error {
	temporary := path + ".new"
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func removeOldRuntimeGenerations(root, keep string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if path != keep && entry.IsDir() && !strings.HasPrefix(entry.Name(), ".stage-") {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseArchitecture(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported runtime architecture %q", goarch)
	}
}

func validateDownloadBase(value string, allowHTTP bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || !allowedRuntimeURLScheme(parsed.Scheme, allowHTTP) {
		return fmt.Errorf("must be an origin URL using HTTPS")
	}
	return nil
}

func allowedRuntimeURLScheme(scheme string, allowHTTP bool) bool {
	return scheme == "https" || allowHTTP && scheme == "http"
}

func secureHTTPClient(allowHTTP bool) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Timeout:   5 * time.Minute,
		Transport: transport,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if !allowedRuntimeURLScheme(request.URL.Scheme, allowHTTP) {
				return fmt.Errorf("redirected to an insecure runtime URL")
			}
			return nil
		},
	}
}
