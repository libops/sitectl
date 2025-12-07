package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/libops/api/proto/libops/v1/libopsv1connect"
	"github.com/libops/sitectl/pkg/auth"
)

// LibopsAPIClient holds all the service clients
type LibopsAPIClient struct {
	OrganizationService libopsv1connect.OrganizationServiceClient
	ProjectService      libopsv1connect.ProjectServiceClient
	SiteService         libopsv1connect.SiteServiceClient
	AccountService      libopsv1connect.AccountServiceClient

	// Members
	MemberService        libopsv1connect.MemberServiceClient
	ProjectMemberService libopsv1connect.ProjectMemberServiceClient
	SiteMemberService    libopsv1connect.SiteMemberServiceClient

	// Firewall
	FirewallService        libopsv1connect.FirewallServiceClient
	ProjectFirewallService libopsv1connect.ProjectFirewallServiceClient
	SiteFirewallService    libopsv1connect.SiteFirewallServiceClient

	// Secrets
	OrganizationSecretService libopsv1connect.OrganizationSecretServiceClient
	ProjectSecretService      libopsv1connect.ProjectSecretServiceClient
	SiteSecretService         libopsv1connect.SiteSecretServiceClient
}

// authTransport is an http.RoundTripper that adds an Authorization header to requests
// and handles automatic token refreshing.
type authTransport struct {
	apiBaseURL string
	next       http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check for API key first
	apiKey, err := loadAPIKey()
	if err == nil && apiKey != "" {
		// Use API key authentication
		req.Header.Set("Authorization", "Bearer "+apiKey)
		return t.next.RoundTrip(req)
	}

	// Fall back to OAuth tokens
	tokens, err := auth.LoadTokens()
	if err != nil {
		// If we can't load tokens, just proceed without auth (likely to fail) or return error?
		// Let's return error as we expect to be authenticated.
		return nil, fmt.Errorf("failed to load tokens: %w", err)
	}

	// Add Authorization header
	req.Header.Set("Authorization", "Bearer "+tokens.IDToken)

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// If we get a 401, the token is invalid - user needs to re-login
	if resp.StatusCode == http.StatusUnauthorized {
		_ = auth.ClearTokens()
	}

	return resp, nil
}

// loadAPIKey loads the API key from ~/.sitectl/key
func loadAPIKey() (string, error) {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return "", fmt.Errorf("HOME environment variable not set")
	}

	keyPath := filepath.Join(homeDir, ".sitectl", "key")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// NewLibopsAPIClient creates and returns a new LibopsAPIClient instance.
// It initializes all necessary service clients with authentication.
func NewLibopsAPIClient(ctx context.Context, apiBaseURL string) (*LibopsAPIClient, error) {
	// Check for API key first
	apiKey, err := loadAPIKey()
	if err == nil && apiKey != "" {
		// API key found, skip token checks
		authenticatedClient := &http.Client{
			Transport: &authTransport{
				apiBaseURL: apiBaseURL,
				next:       http.DefaultTransport,
			},
		}

		return &LibopsAPIClient{
			OrganizationService: libopsv1connect.NewOrganizationServiceClient(authenticatedClient, apiBaseURL),
			ProjectService:      libopsv1connect.NewProjectServiceClient(authenticatedClient, apiBaseURL),
			SiteService:         libopsv1connect.NewSiteServiceClient(authenticatedClient, apiBaseURL),
			AccountService:      libopsv1connect.NewAccountServiceClient(authenticatedClient, apiBaseURL),

			MemberService:        libopsv1connect.NewMemberServiceClient(authenticatedClient, apiBaseURL),
			ProjectMemberService: libopsv1connect.NewProjectMemberServiceClient(authenticatedClient, apiBaseURL),
			SiteMemberService:    libopsv1connect.NewSiteMemberServiceClient(authenticatedClient, apiBaseURL),

			FirewallService:        libopsv1connect.NewFirewallServiceClient(authenticatedClient, apiBaseURL),
			ProjectFirewallService: libopsv1connect.NewProjectFirewallServiceClient(authenticatedClient, apiBaseURL),
			SiteFirewallService:    libopsv1connect.NewSiteFirewallServiceClient(authenticatedClient, apiBaseURL),

			OrganizationSecretService: libopsv1connect.NewOrganizationSecretServiceClient(authenticatedClient, apiBaseURL),
			ProjectSecretService:      libopsv1connect.NewProjectSecretServiceClient(authenticatedClient, apiBaseURL),
			SiteSecretService:         libopsv1connect.NewSiteSecretServiceClient(authenticatedClient, apiBaseURL),
		}, nil
	}

	// Fall back to OAuth tokens
	tokens, err := auth.LoadTokens()
	if err != nil {
		return nil, fmt.Errorf("failed to load authentication tokens: %w", err)
	}

	// Check if token is expired
	if tokens.IsTokenExpired() {
		_ = auth.ClearTokens()
		return nil, fmt.Errorf("authentication token expired, please run 'sitectl login' to re-authenticate")
	}

	authenticatedClient := &http.Client{
		Transport: &authTransport{
			apiBaseURL: apiBaseURL,
			next:       http.DefaultTransport,
		},
	}

	return &LibopsAPIClient{
		OrganizationService: libopsv1connect.NewOrganizationServiceClient(authenticatedClient, apiBaseURL),
		ProjectService:      libopsv1connect.NewProjectServiceClient(authenticatedClient, apiBaseURL),
		SiteService:         libopsv1connect.NewSiteServiceClient(authenticatedClient, apiBaseURL),
		AccountService:      libopsv1connect.NewAccountServiceClient(authenticatedClient, apiBaseURL),

		MemberService:        libopsv1connect.NewMemberServiceClient(authenticatedClient, apiBaseURL),
		ProjectMemberService: libopsv1connect.NewProjectMemberServiceClient(authenticatedClient, apiBaseURL),
		SiteMemberService:    libopsv1connect.NewSiteMemberServiceClient(authenticatedClient, apiBaseURL),

		FirewallService:        libopsv1connect.NewFirewallServiceClient(authenticatedClient, apiBaseURL),
		ProjectFirewallService: libopsv1connect.NewProjectFirewallServiceClient(authenticatedClient, apiBaseURL),
		SiteFirewallService:    libopsv1connect.NewSiteFirewallServiceClient(authenticatedClient, apiBaseURL),

		OrganizationSecretService: libopsv1connect.NewOrganizationSecretServiceClient(authenticatedClient, apiBaseURL),
		ProjectSecretService:      libopsv1connect.NewProjectSecretServiceClient(authenticatedClient, apiBaseURL),
		SiteSecretService:         libopsv1connect.NewSiteSecretServiceClient(authenticatedClient, apiBaseURL),
	}, nil
}
