package cmd

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/sitectl/pkg/api"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a resource by ID",
}

var getOrganizationCmd = &cobra.Command{
	Use:   "organization <organization-id>",
	Short: "Get an organization by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		orgID := args[0]
		resp, err := client.OrganizationService.GetOrganization(cmd.Context(), connect.NewRequest(&libopsv1.GetOrganizationRequest{
			OrganizationId: orgID,
		}))
		if err != nil {
			slog.Error("Failed to get organization", "id", orgID, "err", err)
			return err
		}

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Folder)
		if err != nil {
			return fmt.Errorf("failed to marshal organization to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		return nil
	},
}

var getProjectCmd = &cobra.Command{
	Use:   "project <project-id>",
	Short: "Get a project by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		projID := args[0]
		resp, err := client.ProjectService.GetProject(cmd.Context(), connect.NewRequest(&libopsv1.GetProjectRequest{
			ProjectId: projID,
		}))
		if err != nil {
			slog.Error("Failed to get project", "id", projID, "err", err)
			return err
		}

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Project)
		if err != nil {
			return fmt.Errorf("failed to marshal project to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		return nil
	},
}

var getSiteCmd = &cobra.Command{
	Use:   "site <site-id>",
	Short: "Get a site by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		siteID := args[0]
		resp, err := client.SiteService.GetSite(cmd.Context(), connect.NewRequest(&libopsv1.GetSiteRequest{
			SiteId: siteID,
		}))
		if err != nil {
			slog.Error("Failed to get site", "id", siteID, "err", err)
			return err
		}

		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonOutput, err := marshaler.Marshal(resp.Msg.Site)
		if err != nil {
			return fmt.Errorf("failed to marshal site to JSON: %w", err)
		}
		fmt.Println(string(jsonOutput))

		return nil
	},
}

func init() {
	RootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getOrganizationCmd)
	getCmd.AddCommand(getProjectCmd)
	getCmd.AddCommand(getSiteCmd)
}
