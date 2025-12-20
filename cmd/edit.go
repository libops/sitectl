package cmd

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/api/proto/libops/v1/common"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/resources"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var editCmd = &cobra.Command{
	Use:   "edit",
	Short: "Edit resources",
}

// buildFieldMask creates a field mask from changed flags
func buildFieldMask(cmd *cobra.Command, flagNames []string) *fieldmaskpb.FieldMask {
	var paths []string
	for _, flagName := range flagNames {
		if cmd.Flags().Changed(flagName) {
			// Convert flag name to field path (kebab-case to snake_case)
			fieldPath := flagToFieldPath(flagName)
			paths = append(paths, fieldPath)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return &fieldmaskpb.FieldMask{Paths: paths}
}

// flagToFieldPath converts kebab-case flag names to snake_case field paths
func flagToFieldPath(flagName string) string {
	// For nested fields in protobuf, we need to use dot notation
	// Example: "organization-name" -> "organization_name"
	result := ""
	for i, c := range flagName {
		if c == '-' {
			result += "_"
		} else {
			result += string(c)
		}
		_ = i
	}
	return result
}

var editOrganizationCmd = &cobra.Command{
	Use:   "organization <organization-id>",
	Short: "Edit an organization",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID := args[0]

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		// Build the folder config with only changed fields
		folderConfig := &common.FolderConfig{
			OrganizationId: orgID,
		}

		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			folderConfig.OrganizationName = name
		}

		if cmd.Flags().Changed("location") {
			location, _ := cmd.Flags().GetString("location")
			folderConfig.Location = common.Location(common.Location_value[location])
		}

		if cmd.Flags().Changed("region") {
			region, _ := cmd.Flags().GetString("region")
			folderConfig.Region = region
		}

		// Build field mask
		fieldMask := buildFieldMask(cmd, []string{"name", "location", "region"})
		if fieldMask == nil {
			return fmt.Errorf("no fields to update - specify at least one flag to edit")
		}

		resp, err := client.OrganizationService.UpdateOrganization(cmd.Context(), connect.NewRequest(&libopsv1.UpdateOrganizationRequest{
			Folder:     folderConfig,
			UpdateMask: fieldMask,
		}))
		if err != nil {
			slog.Error("Failed to update organization", "id", orgID, "err", err)
			return err
		}

		fmt.Printf("✓ Updated organization: %s\n", resp.Msg.Folder.OrganizationId)

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Folder)
		if err != nil {
			return fmt.Errorf("failed to marshal organization to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var editProjectCmd = &cobra.Command{
	Use:   "project <project-id>",
	Short: "Edit a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectID := args[0]

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		// Build the project config with only changed fields
		projectConfig := &common.ProjectConfig{}

		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			projectConfig.ProjectName = name
		}

		if cmd.Flags().Changed("machine-type") {
			machineType, _ := cmd.Flags().GetString("machine-type")
			projectConfig.MachineType = machineType
		}

		if cmd.Flags().Changed("create-branch-sites") {
			createBranchSites, _ := cmd.Flags().GetBool("create-branch-sites")
			projectConfig.CreateBranchSites = createBranchSites
		}

		// Build field mask - use "project." prefix for nested fields
		var fieldMaskPaths []string
		if cmd.Flags().Changed("name") {
			fieldMaskPaths = append(fieldMaskPaths, "project.project_name")
		}
		if cmd.Flags().Changed("machine-type") {
			fieldMaskPaths = append(fieldMaskPaths, "project.machine_type")
		}
		if cmd.Flags().Changed("create-branch-sites") {
			fieldMaskPaths = append(fieldMaskPaths, "project.create_branch_sites")
		}

		if len(fieldMaskPaths) == 0 {
			return fmt.Errorf("no fields to update - specify at least one flag to edit")
		}

		fieldMask := &fieldmaskpb.FieldMask{Paths: fieldMaskPaths}

		resp, err := client.ProjectService.UpdateProject(cmd.Context(), connect.NewRequest(&libopsv1.UpdateProjectRequest{
			ProjectId:  projectID,
			Project:    projectConfig,
			UpdateMask: fieldMask,
		}))
		if err != nil {
			slog.Error("Failed to update project", "id", projectID, "err", err)
			return err
		}

		fmt.Printf("✓ Updated project: %s\n", resp.Msg.Project.ProjectId)

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Project)
		if err != nil {
			return fmt.Errorf("failed to marshal project to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var editSiteCmd = &cobra.Command{
	Use:   "site <site-id>",
	Short: "Edit a site",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		siteID := args[0]

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		// Build the site config with only changed fields
		siteConfig := &common.SiteConfig{
			SiteId: siteID,
		}

		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			siteConfig.SiteName = name
		}

		if cmd.Flags().Changed("github-repository") {
			v, _ := cmd.Flags().GetString("github-repository")
			siteConfig.GithubRepository = v
		}

		if cmd.Flags().Changed("github-ref") {
			githubRef, _ := cmd.Flags().GetString("github-ref")
			siteConfig.GithubRef = githubRef
		}

		if cmd.Flags().Changed("compose-path") {
			v, _ := cmd.Flags().GetString("compose-path")
			siteConfig.ComposePath = v
		}

		if cmd.Flags().Changed("compose-file") {
			v, _ := cmd.Flags().GetString("compose-file")
			siteConfig.ComposeFile = v
		}

		if cmd.Flags().Changed("port") {
			v, _ := cmd.Flags().GetInt32("port")
			siteConfig.Port = v
		}

		if cmd.Flags().Changed("application-type") {
			v, _ := cmd.Flags().GetString("application-type")
			siteConfig.ApplicationType = v
		}

		if cmd.Flags().Changed("up-cmd") {
			v, _ := cmd.Flags().GetStringArray("up-cmd")
			siteConfig.UpCmd = v
		}

		if cmd.Flags().Changed("init-cmd") {
			v, _ := cmd.Flags().GetStringArray("init-cmd")
			siteConfig.InitCmd = v
		}

		if cmd.Flags().Changed("rollout-cmd") {
			v, _ := cmd.Flags().GetStringArray("rollout-cmd")
			siteConfig.RolloutCmd = v
		}

		// Build field mask
		fieldMask := buildFieldMask(cmd, []string{
			"name", "github-repository", "github-ref", "compose-path", "compose-file",
			"port", "application-type", "up-cmd", "init-cmd", "rollout-cmd",
		})
		if fieldMask == nil {
			return fmt.Errorf("no fields to update - specify at least one flag to edit")
		}

		resp, err := client.SiteService.UpdateSite(cmd.Context(), connect.NewRequest(&libopsv1.UpdateSiteRequest{
			Site:       siteConfig,
			UpdateMask: fieldMask,
		}))
		if err != nil {
			slog.Error("Failed to update site", "id", siteID, "err", err)
			return err
		}

		fmt.Printf("✓ Updated site: %s\n", resp.Msg.Site.SiteId)

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Site)
		if err != nil {
			return fmt.Errorf("failed to marshal site to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

func init() {
	RootCmd.AddCommand(editCmd)
	editCmd.AddCommand(editOrganizationCmd)
	editCmd.AddCommand(editProjectCmd)
	editCmd.AddCommand(editSiteCmd)

	// Organization edit flags (same as create, but all optional)
	editOrganizationCmd.Flags().String("name", "", "Organization name")
	editOrganizationCmd.Flags().String("location", "", "Geographic location (LOCATION_ASIA, LOCATION_AU, LOCATION_CA, LOCATION_DE, LOCATION_EU, LOCATION_IN, LOCATION_IT, LOCATION_US)")
	editOrganizationCmd.Flags().String("region", "", "Specific region (e.g., us-central1, europe-west1)")

	// Project edit flags (region and zone cannot be updated after creation)
	editProjectCmd.Flags().String("name", "", "Project name")
	editProjectCmd.Flags().String("machine-type", "", "GCP machine type")
	editProjectCmd.Flags().Bool("create-branch-sites", false, "Auto-create sites for new branches")

	// Site edit flags (same as create, but all optional)
	editSiteCmd.Flags().String("name", "", "Site name")
	editSiteCmd.Flags().String("github-repository", "", "GitHub repository URL")
	editSiteCmd.Flags().String("github-ref", "", "GitHub reference (e.g., heads/main, tags/v1.0)")
	editSiteCmd.Flags().String("compose-path", "", "Path to docker-compose directory")
	editSiteCmd.Flags().String("compose-file", "", "Docker compose file name")
	editSiteCmd.Flags().Int32("port", 0, "Port the application listens on")
	editSiteCmd.Flags().String("application-type", "", "Type of application")
	editSiteCmd.Flags().StringArray("up-cmd", []string{}, "Commands to start containers")
	editSiteCmd.Flags().StringArray("init-cmd", []string{}, "Commands to run on initial setup")
	editSiteCmd.Flags().StringArray("rollout-cmd", []string{}, "Commands to run during rollout")
}
