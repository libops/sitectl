package resources

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/api/proto/libops/v1/common"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/cache"
)

// Type aliases for cleaner code
type Organization = common.FolderConfig
type Project = common.ProjectConfig
type Site = common.SiteConfig

// ListOrganizations returns all organizations, using cache when available
func ListOrganizations(ctx context.Context, apiBaseURL string, useCache bool) ([]*Organization, error) {
	cacheKey := cache.CacheKey{
		ResourceType: "organization",
		Operation:    "list",
	}

	// Try cache first
	if useCache {
		var cached []*Organization
		found, err := cache.Get(cacheKey, &cached)
		if err != nil {
			slog.Warn("Failed to read cache", "err", err)
		} else if found {
			slog.Debug("Using cached organizations", "count", len(cached))
			return cached, nil
		}
	}

	// Fetch from API
	client, err := api.NewLibopsAPIClient(ctx, apiBaseURL)
	if err != nil {
		return nil, err
	}

	resp, err := client.OrganizationService.ListOrganizations(ctx, connect.NewRequest(&libopsv1.ListOrganizationsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	// Cache the result
	if useCache {
		if err := cache.Set(cacheKey, resp.Msg.Organizations); err != nil {
			slog.Warn("Failed to cache organizations", "err", err)
		}
	}

	return resp.Msg.Organizations, nil
}

// ListProjects returns all projects, using cache when available
func ListProjects(ctx context.Context, apiBaseURL string, useCache bool, orgID *string) ([]*Project, error) {
	cacheKey := cache.CacheKey{
		ResourceType: "project",
		Operation:    "list",
	}

	// Try cache first
	if useCache {
		var cached []*Project
		found, err := cache.Get(cacheKey, &cached)
		if err != nil {
			slog.Warn("Failed to read cache", "err", err)
		} else if found {
			// Filter by org if needed
			if orgID != nil && *orgID != "" {
				filtered := make([]*Project, 0)
				for _, p := range cached {
					if p.OrganizationId == *orgID {
						filtered = append(filtered, p)
					}
				}
				slog.Debug("Using cached projects (filtered)", "count", len(filtered))
				return filtered, nil
			}
			slog.Debug("Using cached projects", "count", len(cached))
			return cached, nil
		}
	}

	// Fetch from API
	client, err := api.NewLibopsAPIClient(ctx, apiBaseURL)
	if err != nil {
		return nil, err
	}

	resp, err := client.ProjectService.ListProjects(ctx, connect.NewRequest(&libopsv1.ListProjectsRequest{
		OrganizationId: orgID,
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	// Cache the result (only if not filtered)
	if useCache && (orgID == nil || *orgID == "") {
		if err := cache.Set(cacheKey, resp.Msg.Projects); err != nil {
			slog.Warn("Failed to cache projects", "err", err)
		}
	}

	return resp.Msg.Projects, nil
}

// ListSites returns all sites, using cache when available
func ListSites(ctx context.Context, apiBaseURL string, useCache bool, orgID, projectID *string) ([]*Site, error) {
	cacheKey := cache.CacheKey{
		ResourceType: "site",
		Operation:    "list",
	}

	// Try cache first
	if useCache {
		var cached []*Site
		found, err := cache.Get(cacheKey, &cached)
		if err != nil {
			slog.Warn("Failed to read cache", "err", err)
		} else if found {
			// Filter by org/project if needed
			filtered := cached
			if orgID != nil && *orgID != "" {
				temp := make([]*Site, 0)
				for _, s := range filtered {
					if s.OrganizationId == *orgID {
						temp = append(temp, s)
					}
				}
				filtered = temp
			}
			if projectID != nil && *projectID != "" {
				temp := make([]*Site, 0)
				for _, s := range filtered {
					if s.ProjectId == *projectID {
						temp = append(temp, s)
					}
				}
				filtered = temp
			}
			return filtered, nil
		}
	}

	// Fetch from API
	client, err := api.NewLibopsAPIClient(ctx, apiBaseURL)
	if err != nil {
		return nil, err
	}

	resp, err := client.SiteService.ListSites(ctx, connect.NewRequest(&libopsv1.ListSitesRequest{
		OrganizationId: orgID,
		ProjectId:      projectID,
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %w", err)
	}

	// Cache the result (only if not filtered)
	if useCache && (orgID == nil || *orgID == "") && (projectID == nil || *projectID == "") {
		if err := cache.Set(cacheKey, resp.Msg.Sites); err != nil {
			slog.Warn("Failed to cache sites", "err", err)
		}
	}

	return resp.Msg.Sites, nil
}

// GetOrganization returns a specific organization, using cache when available
func GetOrganization(ctx context.Context, apiBaseURL, orgID string, useCache bool) (*Organization, error) {
	cacheKey := cache.CacheKey{
		ResourceType: "organization",
		Operation:    "get",
		ResourceID:   orgID,
	}

	// Try cache first
	if useCache {
		var cached Organization
		found, err := cache.Get(cacheKey, &cached)
		if err != nil {
			slog.Warn("Failed to read cache", "err", err)
		} else if found {
			slog.Debug("Using cached organization", "id", orgID)
			return &cached, nil
		}
	}

	// Fetch from API
	client, err := api.NewLibopsAPIClient(ctx, apiBaseURL)
	if err != nil {
		return nil, err
	}

	resp, err := client.OrganizationService.GetOrganization(ctx, connect.NewRequest(&libopsv1.GetOrganizationRequest{
		OrganizationId: orgID,
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}

	// The response returns a Folder which is our Organization type
	org := resp.Msg.Folder

	// Cache the result
	if useCache {
		if err := cache.Set(cacheKey, org); err != nil {
			slog.Warn("Failed to cache organization", "err", err)
		}
	}

	return org, nil
}

// InvalidateOrganizationCache invalidates all caches related to an organization
func InvalidateOrganizationCache(orgID string) error {
	return cache.InvalidatePattern("organization", orgID)
}

// InvalidateProjectCache invalidates all caches related to a project
func InvalidateProjectCache(projectID string) error {
	return cache.InvalidatePattern("project", projectID)
}

// InvalidateSiteCache invalidates all caches related to a site
func InvalidateSiteCache(siteID string) error {
	return cache.InvalidatePattern("site", siteID)
}

// InvalidateAllResourceCaches invalidates all resource list caches
func InvalidateAllResourceCaches() error {
	if err := cache.InvalidatePattern("organization", ""); err != nil {
		return err
	}
	if err := cache.InvalidatePattern("project", ""); err != nil {
		return err
	}
	if err := cache.InvalidatePattern("site", ""); err != nil {
		return err
	}
	return nil
}
