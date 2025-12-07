package cmd

import (
	"fmt"
	"log/slog"

	"github.com/libops/sitectl/pkg/format"
	"github.com/libops/sitectl/pkg/resources"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources",
}

var listOrganizationsCmd = &cobra.Command{
	Use:   "organizations",
	Short: "List all organizations",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		noCache, _ := cmd.Flags().GetBool("no-cache")
		useCache := !noCache

		orgs, err := resources.ListOrganizations(cmd.Context(), apiBaseURL, useCache)
		if err != nil {
			slog.Error("Failed to list organizations", "err", err)
			return err
		}

		formatStr, err := cmd.Flags().GetString("format")
		if err != nil {
			return err
		}

		formatter, err := format.NewFormatter(formatStr)
		if err != nil {
			return fmt.Errorf("invalid format: %w", err)
		}

		// Prepare data
		headers := []string{"ID", "NAME"}
		var rows [][]string
		var data []interface{}

		for _, org := range orgs {
			rows = append(rows, []string{org.OrganizationId, org.OrganizationName})
			data = append(data, map[string]interface{}{
				"OrganizationId":   org.OrganizationId,
				"OrganizationName": org.OrganizationName,
			})
		}

		return formatter.Print(data, headers, rows)
	},
}

var listProjectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List all projects",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		orgID, err := cmd.Flags().GetString("organization-id")
		if err != nil {
			return err
		}

		var orgIDPtr *string
		if orgID != "" {
			orgIDPtr = &orgID
		}

		noCache, _ := cmd.Flags().GetBool("no-cache")
		useCache := !noCache

		projects, err := resources.ListProjects(cmd.Context(), apiBaseURL, useCache, orgIDPtr)
		if err != nil {
			slog.Error("Failed to list projects", "err", err)
			return err
		}

		formatStr, err := cmd.Flags().GetString("format")
		if err != nil {
			return err
		}

		formatter, err := format.NewFormatter(formatStr)
		if err != nil {
			return fmt.Errorf("invalid format: %w", err)
		}

		// Prepare data
		headers := []string{"ID", "NAME", "ORG ID"}
		var rows [][]string
		var data []interface{}

		for _, proj := range projects {
			rows = append(rows, []string{proj.ProjectId, proj.ProjectName, proj.OrganizationId})
			data = append(data, map[string]interface{}{
				"ProjectId":      proj.ProjectId,
				"ProjectName":    proj.ProjectName,
				"OrganizationId": proj.OrganizationId,
			})
		}

		return formatter.Print(data, headers, rows)
	},
}

var listSitesCmd = &cobra.Command{
	Use:   "sites",
	Short: "List all sites",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		orgID, err := cmd.Flags().GetString("organization-id")
		if err != nil {
			return err
		}
		projID, err := cmd.Flags().GetString("project-id")
		if err != nil {
			return err
		}

		var orgIDPtr *string
		if orgID != "" {
			orgIDPtr = &orgID
		}
		var projIDPtr *string
		if projID != "" {
			projIDPtr = &projID
		}

		noCache, _ := cmd.Flags().GetBool("no-cache")
		useCache := !noCache

		sites, err := resources.ListSites(cmd.Context(), apiBaseURL, useCache, orgIDPtr, projIDPtr)
		if err != nil {
			slog.Error("Failed to list sites", "err", err)
			return err
		}

		formatStr, err := cmd.Flags().GetString("format")
		if err != nil {
			return err
		}

		formatter, err := format.NewFormatter(formatStr)
		if err != nil {
			return fmt.Errorf("invalid format: %w", err)
		}

		// Prepare data
		headers := []string{"ID", "NAME", "PROJECT ID"}
		var rows [][]string
		var data []interface{}

		for _, site := range sites {
			rows = append(rows, []string{site.SiteId, site.SiteName, site.ProjectId})
			data = append(data, map[string]interface{}{
				"SiteId":    site.SiteId,
				"SiteName":  site.SiteName,
				"ProjectId": site.ProjectId,
			})
		}

		return formatter.Print(data, headers, rows)
	},
}

func init() {
	RootCmd.AddCommand(listCmd)
	listCmd.AddCommand(listOrganizationsCmd)
	listCmd.AddCommand(listProjectsCmd)
	listCmd.AddCommand(listSitesCmd)

	// Project list filters
	listProjectsCmd.Flags().String("organization-id", "", "Filter by organization ID")

	// Site list filters
	listSitesCmd.Flags().String("organization-id", "", "Filter by organization ID")
	listSitesCmd.Flags().String("project-id", "", "Filter by project ID")

	listCmd.PersistentFlags().Bool("no-cache", false, "Disable cache and fetch fresh data")
}
