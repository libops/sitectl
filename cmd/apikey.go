package cmd

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	libopsv1 "github.com/libops/api/proto/libops/v1"
	"github.com/libops/sitectl/pkg/api"
	"github.com/libops/sitectl/pkg/format"
	"github.com/spf13/cobra"
)

var createAPIKeyCmd = &cobra.Command{
	Use:   "apikey",
	Short: "Create a new API key",
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

		description, err := cmd.Flags().GetString("description")
		if err != nil {
			return err
		}

		scopes, err := cmd.Flags().GetStringSlice("scopes")
		if err != nil {
			return err
		}

		resp, err := client.AccountService.CreateApiKey(cmd.Context(), connect.NewRequest(&libopsv1.CreateApiKeyRequest{
			Name:        name,
			Description: description,
			Scopes:      scopes,
		}))
		if err != nil {
			slog.Error("Failed to create API key", "err", err)
			return err
		}

		fmt.Printf("✓ Created API key\n")
		fmt.Printf("  UUID: %s\n", resp.Msg.ApiKeyId)
		fmt.Printf("  Name: %s\n", name)
		if description != "" {
			fmt.Printf("  Description: %s\n", description)
		}
		if len(scopes) > 0 {
			fmt.Printf("  Scopes: %s\n", strings.Join(scopes, ", "))
		}
		fmt.Printf("\n")
		fmt.Printf("  API Key: %s\n", resp.Msg.ApiKey)
		fmt.Printf("\n")
		fmt.Printf("⚠️  Save this API key now. It will not be shown again.\n")

		return nil
	},
}

var listAPIKeysCmd = &cobra.Command{
	Use:   "apikeys",
	Short: "List API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiBaseURL, err := cmd.Flags().GetString("api-url")
		if err != nil {
			return err
		}

		client, err := api.NewLibopsAPIClient(cmd.Context(), apiBaseURL)
		if err != nil {
			return err
		}

		resp, err := client.AccountService.ListApiKeys(cmd.Context(), connect.NewRequest(&libopsv1.ListApiKeysRequest{}))
		if err != nil {
			slog.Error("Failed to list API keys", "err", err)
			return err
		}

		// Filter out inactive keys
		var activeKeys []*libopsv1.ApiKeyMetadata
		for _, key := range resp.Msg.ApiKeys {
			if key.Active {
				activeKeys = append(activeKeys, key)
			}
		}

		if len(activeKeys) == 0 {
			fmt.Println("No active API keys found")
			return nil
		}

		// Get format flag
		formatStr, err := cmd.Flags().GetString("format")
		if err != nil {
			return err
		}

		formatter, err := format.NewFormatter(formatStr)
		if err != nil {
			return fmt.Errorf("invalid format: %w", err)
		}

		// Prepare data for formatting
		headers := []string{"ID", "NAME", "SCOPES", "CREATED AT"}
		var rows [][]string

		for _, key := range activeKeys {
			scopes := "-"
			if len(key.Scopes) > 0 {
				scopes = strings.Join(key.Scopes, ", ")
			}

			createdAt := "-"
			if key.CreatedAt > 0 {
				createdAt = time.Unix(key.CreatedAt, 0).Format("2006-01-02 15:04:05")
			}

			rows = append(rows, []string{
				key.ApiKeyId,
				key.Name,
				scopes,
				createdAt,
			})
		}

		// Convert to interface{} slice for JSON/template formatting
		var data []interface{}
		for _, key := range activeKeys {
			data = append(data, map[string]interface{}{
				"ApiKeyId":  key.ApiKeyId,
				"Name":      key.Name,
				"Scopes":    key.Scopes,
				"Active":    key.Active,
				"CreatedAt": key.CreatedAt,
			})
		}

		return formatter.Print(data, headers, rows)
	},
}

var deleteAPIKeyCmd = &cobra.Command{
	Use:   "apikey <api-key-id>",
	Short: "Delete (revoke) an API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiKeyID := args[0]

		confirmed, err := confirmDeletion(cmd, "API key", apiKeyID)
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

		resp, err := client.AccountService.RevokeApiKey(cmd.Context(), connect.NewRequest(&libopsv1.RevokeApiKeyRequest{
			ApiKeyId: apiKeyID,
		}))
		if err != nil {
			slog.Error("Failed to revoke API key", "id", apiKeyID, "err", err)
			return err
		}

		if resp.Msg.Success {
			fmt.Printf("✓ Deleted API key: %s\n", apiKeyID)
		} else {
			fmt.Printf("⚠️  API key deletion returned success=false: %s\n", apiKeyID)
		}

		return nil
	},
}

func init() {
	// Register with verb commands
	createCmd.AddCommand(createAPIKeyCmd)
	listCmd.AddCommand(listAPIKeysCmd)
	deleteCmd.AddCommand(deleteAPIKeyCmd)

	// Create API key flags
	createAPIKeyCmd.Flags().String("name", "", "API key name (required)")
	createAPIKeyCmd.Flags().String("description", "", "API key description")
	createAPIKeyCmd.Flags().StringSlice("scopes", []string{}, "API key scopes (e.g., organization:read, project:write)")
	_ = createAPIKeyCmd.MarkFlagRequired("name")

	// Delete API key flags
	deleteAPIKeyCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
}
