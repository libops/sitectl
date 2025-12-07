package cmd

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"connectrpc.com/connect"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/resources"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete resources",
}

// confirmDeletion prompts the user for confirmation unless --yes flag is set
func confirmDeletion(cmd *cobra.Command, resourceType, resourceID string) (bool, error) {
	yes, err := cmd.Flags().GetBool("yes")
	if err != nil {
		return false, err
	}

	if yes {
		return true, nil
	}

	// Prompt user for confirmation
	fmt.Printf("Are you sure you want to delete %s '%s'? This action cannot be undone.\n", resourceType, resourceID)
	fmt.Print("Type 'yes' to confirm: ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "yes", nil
}

var deleteOrganizationCmd = &cobra.Command{
	Use:   "organization <organization-id>",
	Short: "Delete an organization",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID := args[0]

		confirmed, err := confirmDeletion(cmd, "organization", orgID)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Deletion cancelled.")
			return nil
		}

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		_, err = client.OrganizationService.DeleteOrganization(cmd.Context(), connect.NewRequest(&libopsv1.DeleteOrganizationRequest{
			OrganizationId: orgID,
		}))
		if err != nil {
			slog.Error("Failed to delete organization", "id", orgID, "err", err)
			return err
		}

		fmt.Printf("✓ Deleted organization: %s\n", orgID)

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var deleteProjectCmd = &cobra.Command{
	Use:   "project <project-id>",
	Short: "Delete a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectID := args[0]

		confirmed, err := confirmDeletion(cmd, "project", projectID)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Deletion cancelled.")
			return nil
		}

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		_, err = client.ProjectService.DeleteProject(cmd.Context(), connect.NewRequest(&libopsv1.DeleteProjectRequest{
			ProjectId: projectID,
		}))
		if err != nil {
			slog.Error("Failed to delete project", "id", projectID, "err", err)
			return err
		}

		fmt.Printf("✓ Deleted project: %s\n", projectID)

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

var deleteSiteCmd = &cobra.Command{
	Use:   "site <site-id>",
	Short: "Delete a site",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		siteID := args[0]

		confirmed, err := confirmDeletion(cmd, "site", siteID)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Deletion cancelled.")
			return nil
		}

		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		_, err = client.SiteService.DeleteSite(cmd.Context(), connect.NewRequest(&libopsv1.DeleteSiteRequest{
			SiteId: siteID,
		}))
		if err != nil {
			slog.Error("Failed to delete site", "id", siteID, "err", err)
			return err
		}

		fmt.Printf("✓ Deleted site: %s\n", siteID)

		// Invalidate cache
		if err := resources.InvalidateAllResourceCaches(); err != nil {
			slog.Warn("Failed to invalidate cache", "err", err)
		}

		return nil
	},
}

func init() {
	RootCmd.AddCommand(deleteCmd)
	deleteCmd.AddCommand(deleteOrganizationCmd)
	deleteCmd.AddCommand(deleteProjectCmd)
	deleteCmd.AddCommand(deleteSiteCmd)

	// Add --yes flag to all delete commands
	deleteOrganizationCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	deleteProjectCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	deleteSiteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
}
