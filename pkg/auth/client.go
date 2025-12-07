package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// AuthClient handles unified browser-based authentication.
type AuthClient struct {
	apiBaseURL string
}

// callbackResult holds the result of the OAuth callback.
type callbackResult struct {
	tokens *TokenResponse
	err    error
}

// NewAuthClient creates a new authentication client.
func NewAuthClient(apiBaseURL string) *AuthClient {
	return &AuthClient{
		apiBaseURL: apiBaseURL,
	}
}

// Login opens the browser to the API's login page and waits for the callback.
func (c *AuthClient) Login(ctx context.Context) (*TokenResponse, error) {
	// Start a local HTTP server on a random available port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start local server: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	slog.Debug("Started local callback server", "port", port)

	// Generate random state for CSRF protection
	state, err := generateRandomState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	// Build the login URL that points to the API's login page
	// The API will show both Google and userpass options
	// Pass redirect_uri so the API knows where to send the user after authentication
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)
	loginURL := fmt.Sprintf("%s/login?redirect_uri=%s&state=%s", c.apiBaseURL, redirectURI, state)

	// Create a channel to receive the callback result
	resultChan := make(chan callbackResult, 1)

	// Set up the HTTP server with callback handler
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		c.handleCallback(w, r, state, resultChan)
	})

	server := &http.Server{
		Handler: mux,
	}

	// Start the server in a goroutine
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "err", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Failed to shutdown server", "err", err)
		}
	}()

	// Open browser to the login page
	if err := openBrowser(loginURL); err != nil {
		fmt.Printf("Failed to open browser automatically. Please visit:\n%s\n", loginURL)
	}

	// Wait for callback or timeout
	select {
	case result := <-resultChan:
		if result.err != nil {
			return nil, result.err
		}
		return result.tokens, nil
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authentication timed out after 5 minutes")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// handleCallback processes the callback from the API after authentication.
func (c *AuthClient) handleCallback(w http.ResponseWriter, r *http.Request, expectedState string, resultChan chan<- callbackResult) {
	state := r.URL.Query().Get("state")
	errorParam := r.URL.Query().Get("error")
	errorDesc := r.URL.Query().Get("error_description")

	if errorParam != "" {
		resultChan <- callbackResult{
			err: fmt.Errorf("authentication error: %s - %s", errorParam, errorDesc),
		}
		http.Error(w, fmt.Sprintf("Authentication failed: %s", errorDesc), http.StatusBadRequest)
		return
	}

	if state != expectedState {
		resultChan <- callbackResult{
			err: fmt.Errorf("invalid state parameter"),
		}
		http.Error(w, "Invalid state", http.StatusBadRequest)
		return
	}

	// Extract tokens from cookies set by the API's /auth/callback endpoint
	var accessToken, idToken string
	expiresIn := 3600

	for _, cookie := range r.Cookies() {
		switch cookie.Name {
		case "vault_token":
			accessToken = cookie.Value
			if cookie.MaxAge > 0 {
				expiresIn = cookie.MaxAge
			}
		case "id_token":
			idToken = cookie.Value
		}
	}

	// If tokens aren't in cookies, try query parameters (alternative approach)
	if idToken == "" {
		idToken = r.URL.Query().Get("id_token")
	}
	if accessToken == "" {
		accessToken = r.URL.Query().Get("access_token")
	}

	if idToken == "" {
		resultChan <- callbackResult{
			err: fmt.Errorf("authentication completed but no tokens received"),
		}
		http.Error(w, "No tokens received", http.StatusBadRequest)
		return
	}

	// Calculate expiry date
	expiryDate := time.Now().Unix() + int64(expiresIn)

	tokens := &TokenResponse{
		AccessToken: accessToken,
		IDToken:     idToken,
		TokenType:   "Bearer",
		ExpiryDate:  expiryDate,
		Scope:       "openid email profile",
	}

	// Send success page to browser
	w.Header().Set("Content-Type", "text/html")
	successHTML := `
<!DOCTYPE html>
<html>
<head>
    <title>Authentication Successful</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            background-color: #f5f5f5;
        }
        .container {
            text-align: center;
            background: white;
            padding: 40px;
            border-radius: 8px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        h1 {
            color: #333;
            margin-bottom: 10px;
        }
        p {
            color: #666;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Authentication Successful!</h1>
        <p>You can close this window and return to your terminal.</p>
    </div>
    <script>
        setTimeout(function() {
            window.close();
        }, 2000);
    </script>
</body>
</html>
`
	if _, err := w.Write([]byte(successHTML)); err != nil {
		slog.Error("Failed to write response", "err", err)
	}

	resultChan <- callbackResult{tokens: tokens}
}

// generateRandomState generates a cryptographically secure random state parameter.
func generateRandomState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:32], nil
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}
