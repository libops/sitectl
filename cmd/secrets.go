package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"connectrpc.com/connect"

	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/resources"
	"github.com/spf13/cobra"
)

var createSecretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Create a secret",
	Long:  "Create a secret for an organization, project, or site. Specify one of --organization-id, --project-id, or --site-id.",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		orgID, _ := cmd.Flags().GetString("organization-id")
		projectID, _ := cmd.Flags().GetString("project-id")
		siteID, _ := cmd.Flags().GetString("site-id")

		name, err := cmd.Flags().GetString("name")
		if err != nil {
			return err
		}

		value, err := cmd.Flags().GetString("value")
		if err != nil {
			return err
		}

		// Determine which endpoint to call based on which ID is provided
		if orgID != "" {
			resp, err := client.OrganizationSecretService.CreateOrganizationSecret(cmd.Context(), connect.NewRequest(&libopsv1.CreateOrganizationSecretRequest{
				OrganizationId: orgID,
				Name:           name,
				Value:          value,
			}))
			if err != nil {
				return fmt.Errorf("failed to create organization secret: %w", err)
			}
			fmt.Printf("✓ Created organization secret: %s\n", resp.Msg.Secret.SecretId)
			fmt.Printf("  Name: %s\n", resp.Msg.Secret.Name)
		} else if projectID != "" {
			resp, err := client.ProjectSecretService.CreateProjectSecret(cmd.Context(), connect.NewRequest(&libopsv1.CreateProjectSecretRequest{
				ProjectId: projectID,
				Name:      name,
				Value:     value,
			}))
			if err != nil {
				return fmt.Errorf("failed to create project secret: %w", err)
			}
			fmt.Printf("✓ Created project secret: %s\n", resp.Msg.Secret.SecretId)
			fmt.Printf("  Name: %s\n", resp.Msg.Secret.Name)
		} else if siteID != "" {
			resp, err := client.SiteSecretService.CreateSiteSecret(cmd.Context(), connect.NewRequest(&libopsv1.CreateSiteSecretRequest{
				SiteId: siteID,
				Name:   name,
				Value:  value,
			}))
			if err != nil {
				return fmt.Errorf("failed to create site secret: %w", err)
			}
			fmt.Printf("✓ Created site secret: %s\n", resp.Msg.Secret.SecretId)
			fmt.Printf("  Name: %s\n", resp.Msg.Secret.Name)
		} else {
			return fmt.Errorf("must specify one of --organization-id, --project-id, or --site-id")
		}

		return nil
	},
}

var listSecretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "List secrets",
	Long:  "List secrets. Optionally filter by --organization-id, --project-id, or --site-id. If no filter is specified, lists all secrets.",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		orgID, _ := cmd.Flags().GetString("organization-id")
		projectID, _ := cmd.Flags().GetString("project-id")
		siteID, _ := cmd.Flags().GetString("site-id")

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.TabIndent)
		fmt.Fprintln(w, "SECRET ID\tNAME\tSCOPE")
		fmt.Fprintln(w, "---------\t----\t-----")

		// If specific ID is provided, query that endpoint
		if orgID != "" {
			resp, err := client.OrganizationSecretService.ListOrganizationSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListOrganizationSecretsRequest{
				OrganizationId: orgID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list organization secrets: %w", err)
			}
			for _, s := range resp.Msg.Secrets {
				fmt.Fprintf(w, "%s\t%s\torg:%s\n", s.SecretId, s.Name, orgID)
			}
		} else if projectID != "" {
			resp, err := client.ProjectSecretService.ListProjectSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListProjectSecretsRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list project secrets: %w", err)
			}
			for _, s := range resp.Msg.Secrets {
				fmt.Fprintf(w, "%s\t%s\tproject:%s\n", s.SecretId, s.Name, projectID)
			}
		} else if siteID != "" {
			resp, err := client.SiteSecretService.ListSiteSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListSiteSecretsRequest{
				SiteId: siteID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list site secrets: %w", err)
			}
			for _, s := range resp.Msg.Secrets {
				fmt.Fprintf(w, "%s\t%s\tsite:%s\n", s.SecretId, s.Name, siteID)
			}
		} else {
			// List all - use shared resource functions with caching
			noCache, _ := cmd.Flags().GetBool("no-cache")
			useCache := !noCache

			// List organization secrets
			orgs, err := resources.ListOrganizations(cmd.Context(), apiBaseURL, useCache)
			if err != nil {
				slog.Warn("Failed to list organizations", "err", err)
			} else {
				for _, org := range orgs {
					orgSecretsResp, err := client.OrganizationSecretService.ListOrganizationSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListOrganizationSecretsRequest{
						OrganizationId: org.OrganizationId,
					}))
					if err != nil {
						slog.Warn("Failed to list secrets for organization", "org_id", org.OrganizationId, "err", err)
						continue
					}
					for _, s := range orgSecretsResp.Msg.Secrets {
						fmt.Fprintf(w, "%s\t%s\torg:%s\n", s.SecretId, s.Name, org.OrganizationId)
					}
				}
			}

			// List project secrets
			projects, err := resources.ListProjects(cmd.Context(), apiBaseURL, useCache, nil)
			if err != nil {
				slog.Warn("Failed to list projects", "err", err)
			} else {
				for _, proj := range projects {
					projSecretsResp, err := client.ProjectSecretService.ListProjectSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListProjectSecretsRequest{
						ProjectId: proj.ProjectId,
					}))
					if err != nil {
						slog.Warn("Failed to list secrets for project", "project_id", proj.ProjectId, "err", err)
						continue
					}
					for _, s := range projSecretsResp.Msg.Secrets {
						fmt.Fprintf(w, "%s\t%s\tproject:%s\n", s.SecretId, s.Name, proj.ProjectId)
					}
				}
			}

			// List site secrets
			sites, err := resources.ListSites(cmd.Context(), apiBaseURL, useCache, nil, nil)
			if err != nil {
				slog.Warn("Failed to list sites", "err", err)
			} else {
				for _, site := range sites {
					siteSecretsResp, err := client.SiteSecretService.ListSiteSecrets(cmd.Context(), connect.NewRequest(&libopsv1.ListSiteSecretsRequest{
						SiteId: site.SiteId,
					}))
					if err != nil {
						slog.Warn("Failed to list secrets for site", "site_id", site.SiteId, "err", err)
						continue
					}
					for _, s := range siteSecretsResp.Msg.Secrets {
						fmt.Fprintf(w, "%s\t%s\tsite:%s\n", s.SecretId, s.Name, site.SiteId)
					}
				}
			}
		}

		w.Flush()
		return nil
	},
}

// Note: Secret update/delete requires both the parent resource ID (organization/project/site)
// and the secret ID. These commands have been removed as they cannot be implemented with
// just the secret ID. Use the secret-id shown in list output with the appropriate
// --organization-id, --project-id, or --site-id flag when creating secrets.

func init() {
	// Add secrets subcommand to create command
	createCmd.AddCommand(createSecretsCmd)
	createSecretsCmd.Flags().String("organization-id", "", "Organization ID")
	createSecretsCmd.Flags().String("project-id", "", "Project ID")
	createSecretsCmd.Flags().String("site-id", "", "Site ID")
	createSecretsCmd.Flags().String("name", "", "Secret name (required)")
	createSecretsCmd.Flags().String("value", "", "Secret value (required)")
	_ = createSecretsCmd.MarkFlagRequired("name")
	_ = createSecretsCmd.MarkFlagRequired("value")
	createSecretsCmd.MarkFlagsOneRequired("organization-id", "project-id", "site-id")
	createSecretsCmd.MarkFlagsMutuallyExclusive("organization-id", "project-id", "site-id")

	// Add secrets subcommand to list command
	listCmd.AddCommand(listSecretsCmd)
	listSecretsCmd.Flags().String("organization-id", "", "Filter by organization ID")
	listSecretsCmd.Flags().String("project-id", "", "Filter by project ID")
	listSecretsCmd.Flags().String("site-id", "", "Filter by site ID")
	listSecretsCmd.MarkFlagsMutuallyExclusive("organization-id", "project-id", "site-id")
}
