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

		resp, err := client.ProjectService.CreateProject(cmd.Context(), connect.NewRequest(&libopsv1.CreateProjectRequest{
			OrganizationId: orgID,
			Project: &common.ProjectConfig{
				ProjectName:       name,
				Region:            region,
				Zone:              zone,
				MachineType:       machineType,
				CreateBranchSites: createBranchSites,
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

		githubRepository, err := cmd.Flags().GetString("github-repository")
		if err != nil {
			return err
		}

		githubRef, err := cmd.Flags().GetString("github-ref")
		if err != nil {
			return err
		}

		composePath, err := cmd.Flags().GetString("compose-path")
		if err != nil {
			return err
		}

		composeFile, err := cmd.Flags().GetString("compose-file")
		if err != nil {
			return err
		}

		port, err := cmd.Flags().GetInt32("port")
		if err != nil {
			return err
		}

		appType, err := cmd.Flags().GetString("application-type")
		if err != nil {
			return err
		}

		upCmd, err := cmd.Flags().GetStringArray("up-cmd")
		if err != nil {
			return err
		}

		initCmd, err := cmd.Flags().GetStringArray("init-cmd")
		if err != nil {
			return err
		}

		rolloutCmd, err := cmd.Flags().GetStringArray("rollout-cmd")
		if err != nil {
			return err
		}

		resp, err := client.SiteService.CreateSite(cmd.Context(), connect.NewRequest(&libopsv1.CreateSiteRequest{
			ProjectId: projID,
			Site: &common.SiteConfig{
				SiteName:         name,
				GithubRepository: githubRepository,
				GithubRef:        githubRef,
				ComposePath:      composePath,
				ComposeFile:      composeFile,
				Port:             port,
				ApplicationType:  appType,
				UpCmd:            upCmd,
				InitCmd:          initCmd,
				RolloutCmd:       rolloutCmd,
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
		fmt.Printf("  GitHub Repo: %s\n", resp.Msg.Site.GithubRepository)
		fmt.Printf("  GitHub Ref: %s\n", resp.Msg.Site.GithubRef)
		fmt.Printf("  Compose Path: %s\n", resp.Msg.Site.ComposePath)
		fmt.Printf("  Compose File: %s\n", resp.Msg.Site.ComposeFile)
		fmt.Printf("  Port: %d\n", resp.Msg.Site.Port)
		fmt.Printf("  Application Type: %s\n", resp.Msg.Site.ApplicationType)

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
	createProjectCmd.Flags().String("region", "us-central1", "GCP region")
	createProjectCmd.Flags().String("zone", "us-central1-f", "GCP zone")
	createProjectCmd.Flags().String("machine-type", "e2-standard-2", "GCP machine type")
	createProjectCmd.Flags().Bool("create-branch-sites", false, "Auto-create sites for new branches")
	_ = createProjectCmd.MarkFlagRequired("organization-id")
	_ = createProjectCmd.MarkFlagRequired("name")

	// Site flags
	createSiteCmd.Flags().String("project-id", "", "Project ID (required)")
	createSiteCmd.Flags().String("name", "", "Site name (required)")
	createSiteCmd.Flags().String("github-repository", "", "GitHub repository URL (required)")
	createSiteCmd.Flags().String("github-ref", "", "GitHub reference (e.g., heads/main, tags/v1.0)")
	createSiteCmd.Flags().String("compose-path", "", "Path to docker-compose directory")
	createSiteCmd.Flags().String("compose-file", "docker-compose.yml", "Docker compose file name")
	createSiteCmd.Flags().Int32("port", 80, "Port the application listens on")
	createSiteCmd.Flags().String("application-type", "generic", "Type of application")
	createSiteCmd.Flags().StringArray("up-cmd", []string{}, "Commands to start containers")
	createSiteCmd.Flags().StringArray("init-cmd", []string{}, "Commands to run on initial setup")
	createSiteCmd.Flags().StringArray("rollout-cmd", []string{}, "Commands to run during rollout")
	_ = createSiteCmd.MarkFlagRequired("project-id")
	_ = createSiteCmd.MarkFlagRequired("name")
	_ = createSiteCmd.MarkFlagRequired("github-repository")
}
