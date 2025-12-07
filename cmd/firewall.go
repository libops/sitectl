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

var createFirewallCmd = &cobra.Command{
	Use:   "firewall",
	Short: "Create a firewall rule",
	Long:  "Create a firewall rule for an organization, project, or site. Specify one of --organization-id, --project-id, or --site-id.",
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

		cidr, err := cmd.Flags().GetString("cidr")
		if err != nil {
			return err
		}

		ruleType, err := cmd.Flags().GetString("type")
		if err != nil {
			return err
		}

		ruleTypeEnum := libopsv1.FirewallRuleType(libopsv1.FirewallRuleType_value[ruleType])

		// Determine which endpoint to call based on which ID is provided
		if orgID != "" {
			resp, err := client.FirewallService.CreateOrganizationFirewallRule(cmd.Context(), connect.NewRequest(&libopsv1.CreateOrganizationFirewallRuleRequest{
				OrganizationId: orgID,
				Name:           name,
				Cidr:           cidr,
				RuleType:       ruleTypeEnum,
			}))
			if err != nil {
				return fmt.Errorf("failed to create organization firewall rule: %w", err)
			}
			fmt.Printf("✓ Created organization firewall rule: %s\n", resp.Msg.Rule.RuleId)
			fmt.Printf("  Name: %s\n", resp.Msg.Rule.Name)
			fmt.Printf("  CIDR: %s\n", resp.Msg.Rule.Cidr)
			fmt.Printf("  Type: %s\n", resp.Msg.Rule.RuleType)
		} else if projectID != "" {
			resp, err := client.ProjectFirewallService.CreateProjectFirewallRule(cmd.Context(), connect.NewRequest(&libopsv1.CreateProjectFirewallRuleRequest{
				ProjectId: projectID,
				Name:      name,
				Cidr:      cidr,
				RuleType:  ruleTypeEnum,
			}))
			if err != nil {
				return fmt.Errorf("failed to create project firewall rule: %w", err)
			}
			fmt.Printf("✓ Created project firewall rule: %s\n", resp.Msg.Rule.RuleId)
			fmt.Printf("  Name: %s\n", resp.Msg.Rule.Name)
			fmt.Printf("  CIDR: %s\n", resp.Msg.Rule.Cidr)
			fmt.Printf("  Type: %s\n", resp.Msg.Rule.RuleType)
		} else if siteID != "" {
			resp, err := client.SiteFirewallService.CreateSiteFirewallRule(cmd.Context(), connect.NewRequest(&libopsv1.CreateSiteFirewallRuleRequest{
				SiteId:   siteID,
				Name:     name,
				Cidr:     cidr,
				RuleType: ruleTypeEnum,
			}))
			if err != nil {
				return fmt.Errorf("failed to create site firewall rule: %w", err)
			}
			fmt.Printf("✓ Created site firewall rule: %s\n", resp.Msg.Rule.RuleId)
			fmt.Printf("  Name: %s\n", resp.Msg.Rule.Name)
			fmt.Printf("  CIDR: %s\n", resp.Msg.Rule.Cidr)
			fmt.Printf("  Type: %s\n", resp.Msg.Rule.RuleType)
		} else {
			return fmt.Errorf("must specify one of --organization-id, --project-id, or --site-id")
		}

		return nil
	},
}

var listFirewallCmd = &cobra.Command{
	Use:   "firewall",
	Short: "List firewall rules",
	Long:  "List firewall rules. Optionally filter by --organization-id, --project-id, or --site-id. If no filter is specified, lists all firewall rules.",
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
		fmt.Fprintln(w, "RULE ID\tNAME\tCIDR\tTYPE\tSTATUS\tSCOPE")
		fmt.Fprintln(w, "-------\t----\t----\t----\t------\t-----")

		// If specific ID is provided, query that endpoint
		if orgID != "" {
			resp, err := client.FirewallService.ListOrganizationFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListOrganizationFirewallRulesRequest{
				OrganizationId: orgID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list organization firewall rules: %w", err)
			}
			for _, r := range resp.Msg.Rules {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\torg:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, orgID)
			}
		} else if projectID != "" {
			resp, err := client.ProjectFirewallService.ListProjectFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListProjectFirewallRulesRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list project firewall rules: %w", err)
			}
			for _, r := range resp.Msg.Rules {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\tproject:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, projectID)
			}
		} else if siteID != "" {
			resp, err := client.SiteFirewallService.ListSiteFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListSiteFirewallRulesRequest{
				SiteId: siteID,
			}))
			if err != nil {
				return fmt.Errorf("failed to list site firewall rules: %w", err)
			}
			for _, r := range resp.Msg.Rules {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\tsite:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, siteID)
			}
		} else {
			// List all - use shared resource functions with caching
			noCache, _ := cmd.Flags().GetBool("no-cache")
			useCache := !noCache

			// List organization firewall rules
			orgs, err := resources.ListOrganizations(cmd.Context(), apiBaseURL, useCache)
			if err != nil {
				slog.Warn("Failed to list organizations", "err", err)
			} else {
				for _, org := range orgs {
					orgFirewallResp, err := client.FirewallService.ListOrganizationFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListOrganizationFirewallRulesRequest{
						OrganizationId: org.OrganizationId,
					}))
					if err != nil {
						slog.Warn("Failed to list firewall rules for organization", "org_id", org.OrganizationId, "err", err)
						continue
					}
					for _, r := range orgFirewallResp.Msg.Rules {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\torg:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, org.OrganizationId)
					}
				}
			}

			// List project firewall rules
			projects, err := resources.ListProjects(cmd.Context(), apiBaseURL, useCache, nil)
			if err != nil {
				slog.Warn("Failed to list projects", "err", err)
			} else {
				for _, proj := range projects {
					projFirewallResp, err := client.ProjectFirewallService.ListProjectFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListProjectFirewallRulesRequest{
						ProjectId: proj.ProjectId,
					}))
					if err != nil {
						slog.Warn("Failed to list firewall rules for project", "project_id", proj.ProjectId, "err", err)
						continue
					}
					for _, r := range projFirewallResp.Msg.Rules {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\tproject:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, proj.ProjectId)
					}
				}
			}

			// List site firewall rules
			sites, err := resources.ListSites(cmd.Context(), apiBaseURL, useCache, nil, nil)
			if err != nil {
				slog.Warn("Failed to list sites", "err", err)
			} else {
				for _, site := range sites {
					siteFirewallResp, err := client.SiteFirewallService.ListSiteFirewallRules(cmd.Context(), connect.NewRequest(&libopsv1.ListSiteFirewallRulesRequest{
						SiteId: site.SiteId,
					}))
					if err != nil {
						slog.Warn("Failed to list firewall rules for site", "site_id", site.SiteId, "err", err)
						continue
					}
					for _, r := range siteFirewallResp.Msg.Rules {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\tsite:%s\n", r.RuleId, r.Name, r.Cidr, r.RuleType, r.Status, site.SiteId)
					}
				}
			}
		}

		w.Flush()
		return nil
	},
}

// Note: Firewall rules do not support update operations via the API
// Note: Firewall rules deletion requires parent resource ID (organization/project/site)
// These commands have been removed as they cannot be implemented with just the rule ID

func init() {
	// Add firewall subcommand to create command
	createCmd.AddCommand(createFirewallCmd)
	createFirewallCmd.Flags().String("organization-id", "", "Organization ID")
	createFirewallCmd.Flags().String("project-id", "", "Project ID")
	createFirewallCmd.Flags().String("site-id", "", "Site ID")
	createFirewallCmd.Flags().String("name", "", "Firewall rule name (required)")
	createFirewallCmd.Flags().String("cidr", "", "CIDR block (required)")
	createFirewallCmd.Flags().String("type", "FIREWALL_RULE_TYPE_HTTPS_ALLOWED", "Rule type: FIREWALL_RULE_TYPE_HTTPS_ALLOWED (allow HTTPS), FIREWALL_RULE_TYPE_SSH_ALLOWED (allow SSH), FIREWALL_RULE_TYPE_BLOCKED (block traffic)")
	_ = createFirewallCmd.MarkFlagRequired("name")
	_ = createFirewallCmd.MarkFlagRequired("cidr")
	createFirewallCmd.MarkFlagsOneRequired("organization-id", "project-id", "site-id")
	createFirewallCmd.MarkFlagsMutuallyExclusive("organization-id", "project-id", "site-id")

	// Add firewall subcommand to list command
	listCmd.AddCommand(listFirewallCmd)
	listFirewallCmd.Flags().String("organization-id", "", "Filter by organization ID")
	listFirewallCmd.Flags().String("project-id", "", "Filter by project ID")
	listFirewallCmd.Flags().String("site-id", "", "Filter by site ID")
	listFirewallCmd.MarkFlagsMutuallyExclusive("organization-id", "project-id", "site-id")
}
