package hostruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallManagedRuntimePublishesCompleteGenerationAndRemovesStalePlugin(t *testing.T) {
	t.Parallel()
	archives := map[string][]byte{
		"sitectl":        runtimeArchive(t, "sitectl", []byte("core-v2")),
		"sitectl-drupal": runtimeArchive(t, "sitectl-drupal", []byte("plugin-v2")),
	}
	server, requests := runtimeReleaseServer(t, archives, false)
	defer server.Close()
	root := t.TempDir()
	state, published := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if err := os.MkdirAll(published, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "old"), filepath.Join(published, "sitectl-isle")); err != nil {
		t.Fatal(err)
	}
	err := InstallManagedRuntime(context.Background(), RuntimeInstallOptions{
		StateDir: state, PublishedDir: published, Packages: []string{"sitectl-drupal"},
		Versions:    map[string]string{"sitectl": "v1.12.0", "sitectl-drupal": "v1.12.0"},
		ReleaseBase: server.URL, APIBase: server.URL, AllowHTTP: true, Architecture: "x86_64",
		TrustedUID: os.Geteuid(),
		Artifact:   ArtifactInstallOptions{Manifest: filepath.Join(root, "missing.tsv")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"sitectl": "core-v2", "sitectl-drupal": "plugin-v2"} {
		contents, err := os.ReadFile(filepath.Join(published, name))
		if err != nil || string(contents) != want {
			t.Fatalf("%s = %q, %v; want %q", name, contents, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(published, "sitectl-isle")); !os.IsNotExist(err) {
		t.Fatalf("stale package was not removed: %v", err)
	}
	current, err := os.Readlink(filepath.Join(state, "current"))
	if err != nil || filepath.Dir(current) != filepath.Join(state, "generations") {
		t.Fatalf("current generation = %q, %v", current, err)
	}
	for path, want := range map[string]os.FileMode{
		state:                               runtimeStateMode,
		filepath.Join(state, "generations"): runtimeStateMode,
		current:                             runtimeStateMode,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode %s = %v; want %v", path, info.Mode().Perm(), want)
		}
	}
	for name := range map[string]string{"sitectl": "core-v2", "sitectl-drupal": "plugin-v2"} {
		info, statErr := os.Stat(filepath.Join(published, name))
		if statErr != nil {
			t.Fatalf("stat published %s: %v", name, statErr)
		}
		if info.Mode().Perm()&0o005 != 0o005 {
			t.Fatalf("published %s is not executable by the runtime account: %v", name, info.Mode().Perm())
		}
	}
	firstRequests := *requests
	if err := InstallManagedRuntime(context.Background(), RuntimeInstallOptions{
		StateDir: state, PublishedDir: published, Packages: []string{"sitectl-drupal"},
		Versions:    map[string]string{"sitectl": "v1.12.0", "sitectl-drupal": "v1.12.0"},
		ReleaseBase: server.URL, APIBase: server.URL, AllowHTTP: true, Architecture: "x86_64", TrustedUID: os.Geteuid(),
		Artifact: ArtifactInstallOptions{Manifest: filepath.Join(root, "missing.tsv")},
	}); err != nil {
		t.Fatal(err)
	}
	if *requests != firstRequests {
		t.Fatalf("idempotent install made %d additional downloads", *requests-firstRequests)
	}
}

func TestInstallManagedRuntimeRejectsOneBadPackageBeforePublishingAny(t *testing.T) {
	t.Parallel()
	archives := map[string][]byte{
		"sitectl":        runtimeArchive(t, "sitectl", []byte("core-v2")),
		"sitectl-drupal": runtimeArchive(t, "sitectl-drupal", []byte("plugin-v2")),
	}
	server, _ := runtimeReleaseServer(t, archives, true)
	defer server.Close()
	root := t.TempDir()
	state, published := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if err := os.MkdirAll(published, 0o755); err != nil {
		t.Fatal(err)
	}
	err := InstallManagedRuntime(context.Background(), RuntimeInstallOptions{
		StateDir: state, PublishedDir: published, Packages: []string{"sitectl-drupal"},
		Versions:    map[string]string{"sitectl": "v1.12.0", "sitectl-drupal": "v1.12.0"},
		ReleaseBase: server.URL, APIBase: server.URL, AllowHTTP: true, Architecture: "x86_64",
		TrustedUID: os.Geteuid(),
	})
	if err == nil {
		t.Fatal("expected checksum failure")
	}
	entries, readErr := os.ReadDir(published)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("published entries = %v, %v; want none", entries, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(state, "current")); !os.IsNotExist(statErr) {
		t.Fatalf("current generation was published: %v", statErr)
	}
}

func TestRuntimePackageValidation(t *testing.T) {
	t.Parallel()
	if _, err := normalizeRuntimePackages([]string{"sitectl-ok", "../bad", "sitectl-ok"}); err == nil {
		t.Fatal("expected unsafe package name to be rejected")
	}
	if _, err := selectRuntimeChecksum([]byte("a  file\na  file\n"), "file"); err == nil {
		t.Fatal("expected duplicate checksum to be rejected")
	}
	if _, err := releaseArchitecture("386"); err == nil {
		t.Fatal("expected unsupported architecture to be rejected")
	}
}

func TestInstallManagedRuntimeRejectsVersionForUnselectedPackage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	err := InstallManagedRuntime(context.Background(), RuntimeInstallOptions{
		StateDir: filepath.Join(root, "state"), PublishedDir: filepath.Join(root, "bin"),
		Versions: map[string]string{"sitectl": "v1.12.0", "sitectl-isle": "v1.12.0"},
	})
	if err == nil {
		t.Fatal("expected an unselected package version to be rejected")
	}
}

func runtimeArchive(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func runtimeReleaseServer(t *testing.T, archives map[string][]byte, corruptPluginChecksum bool) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		for name, archive := range archives {
			archiveName := name + "_Linux_x86_64.tar.gz"
			base := "/libops/" + name + "/releases/download/v1.12.0/"
			switch request.URL.Path {
			case base + archiveName:
				_, _ = response.Write(archive)
				return
			case base + "checksums.txt":
				digest := sha256.Sum256(archive)
				if corruptPluginChecksum && name == "sitectl-drupal" {
					digest = sha256.Sum256([]byte("wrong"))
				}
				_, _ = fmt.Fprintf(response, "%x  %s\n", digest, archiveName)
				return
			}
		}
		http.NotFound(response, request)
	}))
	return server, &requests
}
