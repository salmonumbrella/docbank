package main

import (
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/daemonconn"
)

var webSessionsJSON bool

var webSessionsCmd = &cobra.Command{
	Use: "sessions", Short: "List scoped browser sign-ins", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		records, err := c.WebSignIns(cmd.Context())
		if err != nil {
			return err
		}
		if webSessionsJSON {
			return json.MarshalWrite(cmd.OutOrStdout(), struct {
				Items []api.WebSignInRecord `json:"items"`
			}{Items: records})
		}
		for _, record := range records {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  expires %s\n", record.ID, record.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
				return fmt.Errorf("printing browser sign-ins: %w", err)
			}
		}
		if len(records) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "no scoped browser sign-ins")
		}
		if err != nil {
			return fmt.Errorf("printing browser sign-in status: %w", err)
		}
		return nil
	},
}

var webSessionsRevokeCmd = &cobra.Command{
	Use: "revoke SESSION_ID", Short: "Revoke a scoped browser sign-in", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		if err := c.RevokeWebSignIn(cmd.Context(), args[0]); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "browser sign-in revoked")
		if err != nil {
			return fmt.Errorf("printing browser sign-in status: %w", err)
		}
		return nil
	},
}

func init() {
	webSessionsCmd.Flags().BoolVar(&webSessionsJSON, "json", false, "print session IDs and expiry dates as JSON")
	webSessionsCmd.AddCommand(webSessionsRevokeCmd)
	webCmd.AddCommand(webSessionsCmd)
}
