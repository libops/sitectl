package hostruntime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConvergeKeyRotationCreatesAndDistributesCredential(t *testing.T) {
	privateKey := testPrivateKey(t)
	root := t.TempDir()
	credentialPath := filepath.Join(root, "central", "key.json")
	copyPath := filepath.Join(root, "app", "secrets", "key.json")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/metadata":
			_, _ = writer.Write([]byte(`{"access_token":"metadata-token"}`))
		case request.URL.Path == "/token":
			_, _ = writer.Write([]byte(`{"access_token":"new-key-token"}`))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/keys"):
			_, _ = writer.Write([]byte(`{"keys":[]}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/keys"):
			payload := testCredentialJSON(t, "new-key", privateKey, server.URL+"/token")
			response := createdKey{Name: "projects/project/serviceAccounts/agent@example.com/keys/new-key", PrivateKeyData: base64.StdEncoding.EncodeToString(payload)}
			_ = json.NewEncoder(writer).Encode(response)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	options := KeyRotationOptions{
		ServiceAccount: "agent@example.com", ProjectID: "project", CredentialsFile: credentialPath,
		Copies: []CredentialCopy{{Path: copyPath}}, MinimumAge: 0, DisableGrace: time.Hour,
		HTTPClient: server.Client(), IAMBaseURL: server.URL, MetadataTokenURL: server.URL + "/metadata",
	}
	if err := ConvergeKeyRotation(context.Background(), options); err != nil {
		t.Fatalf("ConvergeKeyRotation() error = %v", err)
	}
	central, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(central) != string(copy) {
		t.Fatal("distributed credential differs from central credential")
	}
	if _, err := os.Stat(credentialPath + ".rotation-pending.json"); !os.IsNotExist(err) {
		t.Fatalf("pending state remains after first credential: %v", err)
	}
}

func TestConvergeKeyRotationRetainsThenDeletesPreviousKey(t *testing.T) {
	privateKey := testPrivateKey(t)
	root := t.TempDir()
	credentialPath := filepath.Join(root, "key.json")
	now := time.Unix(1_700_000_000, 0)
	var mutations []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/metadata":
			_, _ = writer.Write([]byte(`{"access_token":"metadata-token"}`))
		case request.URL.Path == "/token":
			_, _ = writer.Write([]byte(`{"access_token":"new-key-token"}`))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/keys"):
			_, _ = writer.Write([]byte(`{"keys":[{"name":"projects/project/serviceAccounts/agent@example.com/keys/old-key","keyType":"USER_MANAGED"}]}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/keys"):
			payload := testCredentialJSON(t, "new-key", privateKey, server.URL+"/token")
			_ = json.NewEncoder(writer).Encode(createdKey{Name: "projects/project/serviceAccounts/agent@example.com/keys/new-key", PrivateKeyData: base64.StdEncoding.EncodeToString(payload)})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, ":disable"):
			mutations = append(mutations, "disable")
			writer.WriteHeader(http.StatusOK)
		case request.Method == http.MethodDelete:
			mutations = append(mutations, "delete")
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	// The current fixture needs the mock token endpoint once the server exists.
	current := testCredentialJSON(t, "old-key", privateKey, server.URL+"/token")
	if err := os.WriteFile(credentialPath, current, 0o440); err != nil {
		t.Fatal(err)
	}

	options := KeyRotationOptions{
		ServiceAccount: "agent@example.com", ProjectID: "project", CredentialsFile: credentialPath,
		MinimumAge: 0, DisableGrace: time.Hour, HTTPClient: server.Client(), IAMBaseURL: server.URL,
		MetadataTokenURL: server.URL + "/metadata", Now: func() time.Time { return now },
	}
	if err := ConvergeKeyRotation(context.Background(), options); err != nil {
		t.Fatalf("first ConvergeKeyRotation() error = %v", err)
	}
	if len(mutations) != 1 || mutations[0] != "disable" {
		t.Fatalf("mutations = %v, want disable", mutations)
	}
	now = now.Add(2 * time.Hour)
	if err := ConvergeKeyRotation(context.Background(), options); err != nil {
		t.Fatalf("second ConvergeKeyRotation() error = %v", err)
	}
	if len(mutations) != 2 || mutations[1] != "delete" {
		t.Fatalf("mutations = %v, want disable then delete", mutations)
	}
}

func testPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testCredentialJSON(t *testing.T, keyID string, privateKey *rsa.PrivateKey, tokenURI string) []byte {
	t.Helper()
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(serviceAccountCredentials{
		Type: "service_account", ProjectID: "project", PrivateKeyID: keyID,
		PrivateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})),
		ClientEmail: "agent@example.com", TokenURI: tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
