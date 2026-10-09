package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/push"
)

func newPushCmd() *cobra.Command {
	var target, name, dest, keyFile, duplicates string
	var watch bool
	var excludes []string
	var settle, age, interval time.Duration
	cmd := &cobra.Command{
		Use:   "push <local-dir> --to <daemon-url> --name <push-name> --dest <virtual-dir>",
		Short: "Push a local folder to a keyed daemon, preserving source identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := url.Parse(target)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
				return errors.New("--to must be an http(s) daemon origin without credentials, path, query, or fragment")
			}
			key := os.Getenv("DOCBANK_API_KEY")
			if keyFile != "" {
				value, err := os.ReadFile(keyFile) // #nosec G304 -- Explicit operator-selected credential file.
				if err != nil {
					return fmt.Errorf("reading API key file: %w", err)
				}
				key = strings.TrimSpace(string(value))
			}
			if key == "" || strings.ContainsAny(key, "\r\n") {
				return errors.New("set DOCBANK_API_KEY or --api-key-file to a nonempty API key")
			}
			connection, err := daemonconn.NewPushConnection(strings.TrimRight(target, "/"), key)
			if err != nil {
				return err
			}
			report, runErr := push.Run(cmd.Context(), connection, push.Options{
				Folder:     config.WatchConfig{Name: name, Source: args[0], Destination: dest, SettleTime: config.Duration(settle), MinimumAge: config.Duration(age), ScanInterval: config.Duration(interval), Exclude: excludes},
				Duplicates: duplicates, Watch: watch,
				Progress: func(ref, outcome string) error {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", outcome, strconv.QuoteToASCII(ref)); err != nil {
						return fmt.Errorf("writing push progress: %w", err)
					}
					return nil
				},
			})
			_, outputErr := fmt.Fprintf(cmd.OutOrStdout(), "added %d, updated %d, linked %d, unchanged %d, duplicate-skipped %d\n", report.Added, report.Updated, report.Linked, report.Skipped, report.DuplicateSkipped)
			if outputErr != nil {
				outputErr = fmt.Errorf("writing push summary: %w", outputErr)
			}
			return errors.Join(runErr, outputErr)
		},
	}
	cmd.Flags().StringVar(&target, "to", "", "daemon URL")
	cmd.Flags().StringVar(&name, "name", "", "stable push name (lowercase letters, digits, -, _, .)")
	cmd.Flags().StringVar(&dest, "dest", "", "absolute virtual destination")
	cmd.Flags().StringVar(&keyFile, "api-key-file", "", "read API key from a file instead of DOCBANK_API_KEY")
	cmd.Flags().StringVar(&duplicates, "duplicates", "link", "new-path duplicate policy: link, skip, create")
	cmd.Flags().BoolVar(&watch, "watch", false, "keep scanning with watched-inbox stability rules")
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "literal basename or source-relative path to exclude (repeatable)")
	cmd.Flags().DurationVar(&settle, "settle-time", 30*time.Second, "unchanged window before uploading in watch mode")
	cmd.Flags().DurationVar(&age, "minimum-age", 0, "minimum time since source modification")
	cmd.Flags().DurationVar(&interval, "scan-interval", 5*time.Second, "watch polling interval")
	for _, flag := range []string{"to", "name", "dest"} {
		if err := cmd.MarkFlagRequired(flag); err != nil {
			panic(err)
		}
	}
	return cmd
}

func init() { rootCmd.AddCommand(newPushCmd()) }
