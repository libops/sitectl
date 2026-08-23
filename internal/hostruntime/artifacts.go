package hostruntime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const maximumManagedArtifactBytes = 512 << 20

var (
	artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	artifactSHApattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	accountNamePattern  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)
	restartUnitPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]*\.service$`)
)

// ArtifactInstallOptions controls installation of verified managed artifacts.
type ArtifactInstallOptions struct {
	Manifest   string
	StateDir   string
	Client     *http.Client
	AllowHTTP  bool
	Restart    func(context.Context, string) error
	TrustedUID int
}

type managedArtifact struct {
	Name, URL, SHA256, Path, Owner, Group, Restart string
	Mode                                           os.FileMode
	UID, GID                                       int
}

// InstallManagedArtifacts validates the complete manifest, then atomically installs each artifact.
func InstallManagedArtifacts(ctx context.Context, options ArtifactInstallOptions) error {
	if options.Manifest == "" {
		options.Manifest = "/home/cloud-compose/managed-runtime-artifacts.tsv"
	}
	if options.StateDir == "" {
		options.StateDir = "/mnt/disks/data/libops-managed/artifacts"
	}
	if options.Client == nil {
		options.Client = secureHTTPClient(options.AllowHTTP)
	}
	if options.Restart == nil {
		options.Restart = restartUnit
	}
	artifacts, err := readArtifactManifest(options.Manifest, options.AllowHTTP)
	if err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return nil
	}
	if err := ensureSafeDirectory(options.StateDir, 0o700); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := preflightArtifactTarget(artifact.Path, options.TrustedUID); err != nil {
			return fmt.Errorf("preflight artifact %q: %w", artifact.Name, err)
		}
	}
	for _, artifact := range artifacts {
		if artifactMatches(artifact) {
			continue
		}
		if err := installManagedArtifact(ctx, artifact, options); err != nil {
			_ = writeAtomic(filepath.Join(options.StateDir, artifact.Name+".failed"), []byte(err.Error()+"\n"), 0o600)
			return err
		}
		_ = os.Remove(filepath.Join(options.StateDir, artifact.Name+".failed"))
		if err := writeAtomic(filepath.Join(options.StateDir, artifact.Name+".sha256"), []byte(artifact.SHA256+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func readArtifactManifest(path string, allowHTTP bool) ([]managedArtifact, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("managed artifact manifest is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	seenNames, seenPaths := map[string]struct{}{}, map[string]struct{}{}
	var artifacts []managedArtifact
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "" {
			continue
		}
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 8 {
			return nil, fmt.Errorf("managed artifact rows must contain eight fields")
		}
		mode, parseErr := strconv.ParseUint(strings.TrimPrefix(fields[4], "0"), 8, 12)
		if parseErr != nil {
			return nil, fmt.Errorf("artifact %q has an invalid mode", fields[0])
		}
		artifact := managedArtifact{Name: fields[0], URL: fields[1], SHA256: fields[2], Path: fields[3], Mode: os.FileMode(mode), Owner: fields[5], Group: fields[6], Restart: fields[7]}
		if err := validateArtifact(artifact, allowHTTP); err != nil {
			return nil, err
		}
		artifact.UID, artifact.GID, err = resolveOwner(artifact.Owner, artifact.Group)
		if err != nil {
			return nil, err
		}
		if _, exists := seenNames[artifact.Name]; exists {
			return nil, fmt.Errorf("duplicate managed artifact name %q", artifact.Name)
		}
		if _, exists := seenPaths[artifact.Path]; exists {
			return nil, fmt.Errorf("duplicate managed artifact path %q", artifact.Path)
		}
		seenNames[artifact.Name], seenPaths[artifact.Path] = struct{}{}, struct{}{}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, scanner.Err()
}

func validateArtifact(artifact managedArtifact, allowHTTP bool) error {
	parsedURL, err := url.Parse(artifact.URL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.Fragment != "" || !allowedRuntimeURLScheme(parsedURL.Scheme, allowHTTP) {
		return fmt.Errorf("artifact %q must use a valid HTTPS URL", artifact.Name)
	}
	if !artifactNamePattern.MatchString(artifact.Name) || !artifactSHApattern.MatchString(artifact.SHA256) {
		return fmt.Errorf("artifact %q has an invalid name or digest", artifact.Name)
	}
	if !filepath.IsAbs(artifact.Path) || filepath.Clean(artifact.Path) != artifact.Path || artifact.Path == "/" || strings.ContainsAny(artifact.Path, "\r\n\t\x00") {
		return fmt.Errorf("artifact %q has an unsafe target path", artifact.Name)
	}
	if artifact.Mode&^os.FileMode(0o777) != 0 || artifact.Mode&0o022 != 0 {
		return fmt.Errorf("artifact %q has an unsafe mode", artifact.Name)
	}
	if !accountNamePattern.MatchString(artifact.Owner) || !accountNamePattern.MatchString(artifact.Group) {
		return fmt.Errorf("artifact %q has an invalid owner or group", artifact.Name)
	}
	if artifact.Restart != "" && !restartUnitPattern.MatchString(artifact.Restart) {
		return fmt.Errorf("artifact %q has an invalid restart unit", artifact.Name)
	}
	return nil
}

func preflightArtifactTarget(path string, trustedUID int) error {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("target parent is missing or contains a symbolic link")
	}
	for current, targetParent := parent, true; ; current, targetParent = filepath.Dir(current), false {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe target parent: %s", current)
		}
		metadata, ok := info.Sys().(*syscall.Stat_t)
		writable := info.Mode().Perm()&0o022 != 0
		stickyRootAncestor := !targetParent && int(metadata.Uid) == 0 && info.Mode()&os.ModeSticky != 0
		if !ok || int(metadata.Uid) != 0 && int(metadata.Uid) != trustedUID || writable && !stickyRootAncestor {
			return fmt.Errorf("untrusted target parent: %s", current)
		}
		if current == "/" {
			break
		}
	}
	if info, err := os.Lstat(path); err == nil {
		metadata, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || metadata.Nlink != 1 {
			return fmt.Errorf("target is not a single-link regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func artifactMatches(artifact managedArtifact) bool {
	digest, _, err := digestFile(artifact.Path)
	if err != nil || digest != artifact.SHA256 {
		return false
	}
	info, err := os.Stat(artifact.Path)
	if err != nil {
		return false
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().Perm() == artifact.Mode && int(metadata.Uid) == artifact.UID && int(metadata.Gid) == artifact.GID
}

func installManagedArtifact(ctx context.Context, artifact managedArtifact, options ArtifactInstallOptions) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	response, err := options.Client.Do(request)
	if err != nil {
		return fmt.Errorf("download artifact %q: %w", artifact.Name, err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || !allowedRuntimeURLScheme(response.Request.URL.Scheme, options.AllowHTTP) {
		return fmt.Errorf("artifact %q redirected to an insecure URL", artifact.Name)
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("download artifact %q: HTTP %d", artifact.Name, response.StatusCode)
	}
	temporary, err := os.CreateTemp(filepath.Dir(artifact.Path), ".sitectl-artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, maximumManagedArtifactBytes+1))
	if copyErr != nil || written > maximumManagedArtifactBytes || fmt.Sprintf("%x", digest.Sum(nil)) != artifact.SHA256 {
		_ = temporary.Close()
		return fmt.Errorf("artifact %q failed content verification", artifact.Name)
	}
	if err := temporary.Chmod(artifact.Mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chown(artifact.UID, artifact.GID); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	backup := artifact.Path + ".sitectl-backup"
	if _, err := os.Lstat(backup); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("artifact backup path is not empty: %s", backup)
	}
	hadPrevious := false
	if _, err := os.Lstat(artifact.Path); err == nil {
		if err := os.Rename(artifact.Path, backup); err != nil {
			return err
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporaryPath, artifact.Path); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, artifact.Path)
		}
		return err
	}
	if artifact.Restart != "" {
		if err := options.Restart(ctx, artifact.Restart); err != nil {
			_ = os.Remove(artifact.Path)
			if hadPrevious {
				err = errors.Join(err, os.Rename(backup, artifact.Path))
			}
			return fmt.Errorf("restart after installing artifact %q: %w", artifact.Name, err)
		}
	}
	if hadPrevious {
		return os.Remove(backup)
	}
	return nil
}

func restartUnit(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "try-restart", "--", unit).Run()
}
