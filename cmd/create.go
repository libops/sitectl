package cmd

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/api/proto/libops/v1/common"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/resources"
	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create resources",
}

var createOrganizationCmd = &cobra.Command{
	Use:   "organization",
	Short: "Create a new organization",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		name, err := cmd.Flags().GetString("name")
		if err != nil {
			return err
		}

		location, err := cmd.Flags().GetString("location")
		if err != nil {
			return err
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			return err
		}

		resp, err := client.OrganizationService.CreateOrganization(cmd.Context(), connect.NewRequest(&libopsv1.CreateOrganizationRequest{
			Folder: &common.FolderConfig{
				OrganizationName: name,
				Location:         common.Location(common.Location_value[location]),
				Region:           region,
			},
		}))
		if err != nil {
			slog.Error("Failed to create organization", "err", err)
			return err
		}

		fmt.Printf("✓ Created organization\n")
		fmt.Printf("  UUID: %s\n", resp.Msg.Folder.OrganizationId)
		fmt.Printf("  Name: %s\n", resp.Msg.Folder.OrganizationName)
		fmt.Printf("  Location: %s\n", resp.Msg.Folder.Location)
		fmt.Printf("  Region: %s\n", resp.Msg.Folder.Region)

		// Invalidate organization cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var createProjectCmd = &cobra.Command{
	Use:   "project",
	Short: "Create a new project",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		orgID, err := cmd.Flags().GetString("organization-id")
		if err != nil {
			return err
		}

		name, err := cmd.Flags().GetString("name")
		if err != nil {
			return err
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			return err
		}

		zone, err := cmd.Flags().GetString("zone")
		if err != nil {
			return err
		}

		machineType, err := cmd.Flags().GetString("machine-type")
		if err != nil {
			return err
		}

		createBranchSites, err := cmd.Flags().GetBool("create-branch-sites")
		if err != nil {
			return err
		}

		githubRepo, err := cmd.Flags().GetString("github-repo")
		if err != nil {
			return err
		}

		var githubRepoPtr *string
		if githubRepo != "" {
			githubRepoPtr = &githubRepo
		}

		resp, err := client.ProjectService.CreateProject(cmd.Context(), connect.NewRequest(&libopsv1.CreateProjectRequest{
			OrganizationId: orgID,
			Project: &common.ProjectConfig{
				ProjectName:       name,
				Region:            region,
				Zone:              zone,
				MachineType:       machineType,
				CreateBranchSites: createBranchSites,
				GithubRepo:        githubRepoPtr,
			},
		}))
		if err != nil {
			slog.Error("Failed to create project", "err", err)
			return err
		}

		fmt.Printf("✓ Created project\n")
		fmt.Printf("  UUID: %s\n", resp.Msg.Project.ProjectId)
		fmt.Printf("  Name: %s\n", resp.Msg.Project.ProjectName)
		fmt.Printf("  Organization ID: %s\n", resp.Msg.Project.OrganizationId)
		fmt.Printf("  Region: %s\n", resp.Msg.Project.Region)
		fmt.Printf("  Zone: %s\n", resp.Msg.Project.Zone)
		if resp.Msg.Project.GithubRepo != nil && *resp.Msg.Project.GithubRepo != "" {
			fmt.Printf("  GitHub Repo: %s\n", *resp.Msg.Project.GithubRepo)
		}

		// Invalidate project cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var createSiteCmd = &cobra.Command{
	Use:   "site",
	Short: "Create a new site",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		projID, err := cmd.Flags().GetString("project-id")
		if err != nil {
			return err
		}

		name, err := cmd.Flags().GetString("name")
		if err != nil {
			return err
		}

		githubRef, err := cmd.Flags().GetString("github-ref")
		if err != nil {
			return err
		}

		resp, err := client.SiteService.CreateSite(cmd.Context(), connect.NewRequest(&libopsv1.CreateSiteRequest{
			ProjectId: projID,
			Site: &common.SiteConfig{
				SiteName:  name,
				GithubRef: githubRef,
			},
		}))
		if err != nil {
			slog.Error("Failed to create site", "err", err)
			return err
		}

		fmt.Printf("✓ Created site\n")
		fmt.Printf("  UUID: %s\n", resp.Msg.Site.SiteId)
		fmt.Printf("  Name: %s\n", resp.Msg.Site.SiteName)
		fmt.Printf("  Organization ID: %s\n", resp.Msg.Site.OrganizationId)
		fmt.Printf("  Project ID: %s\n", resp.Msg.Site.ProjectId)
		fmt.Printf("  GitHub Ref: %s\n", resp.Msg.Site.GithubRef)

		// Invalidate site cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

func init() {
	RootCmd.AddCommand(createCmd)
	createCmd.AddCommand(createOrganizationCmd)
	createCmd.AddCommand(createProjectCmd)
	createCmd.AddCommand(createSiteCmd)

	// Organization flags
	createOrganizationCmd.Flags().String("name", "", "Organization name (required)")
	createOrganizationCmd.Flags().String("location", "LOCATION_US", "Geographic location (LOCATION_ASIA, LOCATION_AU, LOCATION_CA, LOCATION_DE, LOCATION_EU, LOCATION_IN, LOCATION_IT, LOCATION_US)")
	createOrganizationCmd.Flags().String("region", "us-central1", "Specific region (e.g., us-central1, europe-west1)")
	_ = createOrganizationCmd.MarkFlagRequired("name")

	// Project flags
	createProjectCmd.Flags().String("organization-id", "", "Organization ID (required)")
	createProjectCmd.Flags().String("name", "", "Project name (required)")
	createProjectCmd.Flags().String("github-repo", "", "GitHub repository URL (required)")
	createProjectCmd.Flags().String("region", "us-central1", "GCP region")
	createProjectCmd.Flags().String("zone", "us-central1-f", "GCP zone")
	createProjectCmd.Flags().String("machine-type", "e2-standard-2", "GCP machine type")
	createProjectCmd.Flags().Bool("create-branch-sites", false, "Auto-create sites for new branches")
	_ = createProjectCmd.MarkFlagRequired("organization-id")
	_ = createProjectCmd.MarkFlagRequired("name")
	_ = createProjectCmd.MarkFlagRequired("github-repo")

	// Site flags
	createSiteCmd.Flags().String("project-id", "", "Project ID (required)")
	createSiteCmd.Flags().String("name", "", "Site name (required)")
	createSiteCmd.Flags().String("github-ref", "", "GitHub reference (e.g., heads/main, tags/v1.0)")
	_ = createSiteCmd.MarkFlagRequired("project-id")
	_ = createSiteCmd.MarkFlagRequired("name")
}
