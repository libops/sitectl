package hostruntime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestInstallManagedArtifactsVerifiesAndSkipsMatchingTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified artifact\n")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	manifest := filepath.Join(root, "manifest.tsv")
	target := filepath.Join(root, "tool")
	owner, group := currentAccountNames(t)
	writeArtifactManifest(t, manifest, "tool", server.URL, payload, target, owner, group, "")
	options := ArtifactInstallOptions{Manifest: manifest, StateDir: filepath.Join(root, "state"), Client: server.Client(), AllowHTTP: true, TrustedUID: os.Geteuid()}
	if err := InstallManagedArtifacts(context.Background(), options); err != nil {
		t.Fatalf("InstallManagedArtifacts() error = %v", err)
	}
	if contents, _ := os.ReadFile(target); string(contents) != string(payload) {
		t.Fatalf("target = %q", contents)
	}
	if err := InstallManagedArtifacts(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one verified download", requests)
	}
}

func TestInstallManagedArtifactsRollsBackFailedRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("replacement\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(payload) }))
	defer server.Close()
	target := filepath.Join(root, "tool")
	if err := os.WriteFile(target, []byte("previous\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	owner, group := currentAccountNames(t)
	manifest := filepath.Join(root, "manifest.tsv")
	writeArtifactManifest(t, manifest, "tool", server.URL, payload, target, owner, group, "cloud-compose.service")
	err := InstallManagedArtifacts(context.Background(), ArtifactInstallOptions{
		Manifest: manifest, StateDir: filepath.Join(root, "state"), Client: server.Client(), AllowHTTP: true, TrustedUID: os.Geteuid(),
		Restart: func(context.Context, string) error { return fmt.Errorf("restart failed") },
	})
	if err == nil {
		t.Fatal("expected restart failure")
	}
	if contents, _ := os.ReadFile(target); string(contents) != "previous\n" {
		t.Fatalf("rollback target = %q", contents)
	}
}

func writeArtifactManifest(t *testing.T, path, name, source string, payload []byte, target, owner, group, restart string) {
	t.Helper()
	digest := sha256.Sum256(payload)
	row := fmt.Sprintf("%s\t%s\t%x\t%s\t0755\t%s\t%s\t%s\n", name, source, digest, target, owner, group, restart)
	if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
}

func currentAccountNames(t *testing.T) (string, string) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil {
		t.Fatal(err)
	}
	return account.Username, group.Name
}
