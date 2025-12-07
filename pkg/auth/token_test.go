package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenFilePath(t *testing.T) {
	path, err := TokenFilePath()
	if err != nil {
		t.Fatalf("TokenFilePath() failed: %v", err)
	}

	if path == "" {
		t.Error("TokenFilePath() returned empty path")
	}

	if !filepath.IsAbs(path) {
		t.Errorf("TokenFilePath() returned non-absolute path: %s", path)
	}

	expectedSuffix := filepath.Join(".sitectl", "oauth.json")
	if !filepath.IsAbs(path) || filepath.Base(filepath.Dir(path)) != ".sitectl" {
		t.Errorf("TokenFilePath() = %s, should contain %s", path, expectedSuffix)
	}
}

func TestSaveAndLoadTokens(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	tokens := &TokenResponse{
		AccessToken: "test_access_token",
		IDToken:     "test_id_token",
		TokenType:   "Bearer",
		ExpiryDate:  time.Now().Unix() + 3600,
		Scope:       "openid email profile",
	}

	// Test saving tokens
	err := SaveTokens(tokens)
	if err != nil {
		t.Fatalf("SaveTokens() failed: %v", err)
	}

	// Verify file was created with correct permissions
	tokenPath, _ := TokenFilePath()
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("Token file not created: %v", err)
	}

	// Check file permissions (should be 0600)
	expectedPerms := os.FileMode(0600)
	if info.Mode().Perm() != expectedPerms {
		t.Errorf("Token file permissions = %o, want %o", info.Mode().Perm(), expectedPerms)
	}

	// Test loading tokens
	loadedTokens, err := LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens() failed: %v", err)
	}

	// Verify loaded tokens match saved tokens
	if loadedTokens.AccessToken != tokens.AccessToken {
		t.Errorf("AccessToken = %s, want %s", loadedTokens.AccessToken, tokens.AccessToken)
	}
	if loadedTokens.IDToken != tokens.IDToken {
		t.Errorf("IDToken = %s, want %s", loadedTokens.IDToken, tokens.IDToken)
	}
	if loadedTokens.TokenType != tokens.TokenType {
		t.Errorf("TokenType = %s, want %s", loadedTokens.TokenType, tokens.TokenType)
	}
	if loadedTokens.ExpiryDate != tokens.ExpiryDate {
		t.Errorf("ExpiryDate = %d, want %d", loadedTokens.ExpiryDate, tokens.ExpiryDate)
	}
	if loadedTokens.Scope != tokens.Scope {
		t.Errorf("Scope = %s, want %s", loadedTokens.Scope, tokens.Scope)
	}
}

func TestLoadTokens_NotFound(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Ensure no token file exists
	tokenPath, _ := TokenFilePath()
	os.Remove(tokenPath)

	_, err := LoadTokens()
	if err == nil {
		t.Error("LoadTokens() should fail when token file doesn't exist")
	}
}

func TestIsTokenExpired(t *testing.T) {
	tests := []struct {
		name       string
		expiryDate int64
		want       bool
	}{
		{
			name:       "token expired",
			expiryDate: time.Now().Unix() - 3600, // 1 hour ago
			want:       true,
		},
		{
			name:       "token valid",
			expiryDate: time.Now().Unix() + 3600, // 1 hour from now
			want:       false,
		},
		{
			name:       "token expires now",
			expiryDate: time.Now().Unix(),
			want:       true, // Should be considered expired if exactly at expiry time
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &TokenResponse{
				ExpiryDate: tt.expiryDate,
			}
			if got := token.IsTokenExpired(); got != tt.want {
				t.Errorf("IsTokenExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClearTokens(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Create a token file
	tokens := &TokenResponse{
		AccessToken: "test_token",
		IDToken:     "test_id_token",
		TokenType:   "Bearer",
		ExpiryDate:  time.Now().Unix() + 3600,
		Scope:       "openid",
	}

	err := SaveTokens(tokens)
	if err != nil {
		t.Fatalf("SaveTokens() failed: %v", err)
	}

	// Verify file exists
	tokenPath, _ := TokenFilePath()
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		t.Fatal("Token file was not created")
	}

	// Clear tokens
	err = ClearTokens()
	if err != nil {
		t.Fatalf("ClearTokens() failed: %v", err)
	}

	// Verify file was removed
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Error("Token file still exists after ClearTokens()")
	}
}

func TestClearTokens_NotFound(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Ensure no token file exists
	tokenPath, _ := TokenFilePath()
	os.Remove(tokenPath)

	// Clearing non-existent tokens should not error
	err := ClearTokens()
	if err != nil {
		t.Errorf("ClearTokens() failed when no tokens exist: %v", err)
	}
}
