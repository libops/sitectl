package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenResponse represents the OAuth token response stored locally.
type TokenResponse struct {
	AccessToken string `json:"access_token,omitempty"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiryDate  int64  `json:"expiry_date"`
	Scope       string `json:"scope"`
}

// TokenFilePath returns the path to the OAuth token file.
func TokenFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to detect home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".sitectl")
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		if err := os.Mkdir(baseDir, 0700); err != nil {
			return "", fmt.Errorf("unable to create ~/.sitectl directory: %w", err)
		}
	}

	return filepath.Join(baseDir, "oauth.json"), nil
}

// SaveTokens saves OAuth tokens to disk with restricted permissions.
func SaveTokens(tokens *TokenResponse) error {
	tokenPath, err := TokenFilePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tokens: %w", err)
	}

	// Write with restrictive permissions (0600 = rw-------)
	if err := os.WriteFile(tokenPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}

// LoadTokens loads OAuth tokens from disk.
func LoadTokens() (*TokenResponse, error) {
	tokenPath, err := TokenFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not authenticated: run 'sitectl login' first")
		}
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var tokens TokenResponse
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &tokens, nil
}

// IsTokenExpired checks if the token has expired.
func (t *TokenResponse) IsTokenExpired() bool {
	return time.Now().Unix() >= t.ExpiryDate
}

// ClearTokens removes the token file from disk.
func ClearTokens() error {
	tokenPath, err := TokenFilePath()
	if err != nil {
		return err
	}

	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove token file: %w", err)
	}

	return nil
}
