package hostruntime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDockerPluginsVerifiesAndRepairs(t *testing.T) {
	assets := map[string][]byte{
		"docker-compose-linux-x86_64": []byte("compose"),
		"buildx-v2.3.4.linux-amd64":   []byte("buildx"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asset := filepath.Base(request.URL.Path)
		if asset == "checksums.txt" {
			projectAsset := "docker-compose-linux-x86_64"
			if strings.Contains(request.URL.Path, "/buildx/") {
				projectAsset = "buildx-v2.3.4.linux-amd64"
			}
			digest := sha256.Sum256(assets[projectAsset])
			fmt.Fprintf(response, "%x *%s\n", digest, projectAsset)
			return
		}
		if _, err := response.Write(assets[asset]); err != nil {
			t.Errorf("write test asset: %v", err)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	options := DockerPluginOptions{Directory: directory, ComposeVersion: "v1.2.3", BuildxVersion: "v2.3.4", Architecture: "amd64", ReleaseBase: server.URL, Client: server.Client(), AllowHTTP: true}
	if err := InstallDockerPlugins(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "docker-buildx")
	if err := os.WriteFile(path, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallDockerPlugins(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "buildx" {
		t.Fatalf("repaired plugin = %q, %v", contents, err)
	}
}

func TestInstallDockerPluginsPreservesVerifiedTargetOnTamper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asset := filepath.Base(request.URL.Path)
		if asset == "checksums.txt" {
			projectAsset := "docker-compose-linux-x86_64"
			if strings.Contains(request.URL.Path, "/buildx/") {
				projectAsset = "buildx-v2.3.4.linux-amd64"
			}
			fmt.Fprintf(response, "%064d *%s\n", 0, projectAsset)
			return
		}
		if _, err := response.Write([]byte("tampered")); err != nil {
			t.Errorf("write test asset: %v", err)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	path := filepath.Join(directory, "docker-compose")
	if err := os.WriteFile(path, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	options := DockerPluginOptions{Directory: directory, ComposeVersion: "v1.2.3", BuildxVersion: "v2.3.4", Architecture: "amd64", ReleaseBase: server.URL, Client: server.Client(), AllowHTTP: true}
	if err := InstallDockerPlugins(context.Background(), options); err == nil {
		t.Fatal("expected checksum failure")
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "previous" {
		t.Fatalf("preserved plugin = %q, %v", contents, err)
	}
}

func TestUniqueReleaseChecksumRejectsDuplicate(t *testing.T) {
	manifest := strings.Repeat(strings.Repeat("a", 64)+" asset\n", 2)
	if _, err := uniqueReleaseChecksum(manifest, "asset"); err == nil {
		t.Fatal("expected duplicate checksum to fail")
	}
}
