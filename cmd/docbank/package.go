package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/daemonconn"
)

var (
	packagePreflightProfile        string
	packagePreflightPageMapProfile string
	packagePreflightEncoding       string
	packagePreflightMap            string
	packagePreflightJSON           bool
	packageImportInto              string
	packageImportName              string
	packageImportParty             string
	packageImportOperation         string
	packageImportPartial           bool
	packageImportSuppliedText      bool
	packageImportJSON              bool
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Inspect and exchange load-file packages",
}

var packagePreflightCmd = &cobra.Command{
	Use:   "preflight <path>",
	Short: "Validate a received load-file package without importing it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if packagePreflightProfile == "" {
			return usageError(errors.New("--profile is required"))
		}
		if packagePreflightEncoding == "" {
			return usageError(errors.New("--encoding is required"))
		}
		var mapping []byte
		if packagePreflightMap != "" {
			var err error
			mapping, err = os.ReadFile(packagePreflightMap)
			if err != nil {
				return fmt.Errorf("reading package mapping: %w", err)
			}
		}
		request := api.PackagePreflightRequest{Profile: packagePreflightProfile, PageMapProfile: packagePreflightPageMapProfile, Encoding: packagePreflightEncoding, Mapping: mapping}
		reference, err := packagePreflightSource(args[0])
		if err != nil {
			return err
		}
		request.SourceKind, request.SourceRef = "root", reference
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		result, err := c.API().CreatePackagePreflight(cmd.Context(), &apiclient.CreatePackagePreflightRequestOptions{Body: &request})
		if err != nil {
			return err
		}
		if packagePreflightJSON {
			return writeCLIJSON(cmd.OutOrStdout(), result)
		}
		if _, err = fmt.Fprintf(cmd.OutOrStdout(), "preflight %s: %d records, %d pages, blocking=%t\n", result.PreflightID, result.Records, result.Pages, result.Blocking); err != nil {
			return fmt.Errorf("writing package preflight: %w", err)
		}
		return nil
	},
}

var packageImportCmd = &cobra.Command{
	Use:   "import <preflight-id>",
	Short: "Start a durable load-file import from a successful preflight",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if packageImportName == "" {
			return usageError(errors.New("--name is required"))
		}
		operation := packageImportOperation
		if operation == "" {
			operation = uuid.New().String()
		}
		client, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		result, err := client.API().CreatePackageImport(cmd.Context(), &apiclient.CreatePackageImportRequestOptions{Body: &api.PackageImportRequest{
			PreflightID: args[0], Into: packageImportInto, Name: packageImportName,
			Party: packageImportParty, OperationID: operation,
			AcceptPartial: packageImportPartial, IndexSuppliedText: packageImportSuppliedText,
		}})
		if err != nil {
			return err
		}
		return writePackageImport(cmd, result)
	},
}

var packageImportStatusCmd = &cobra.Command{
	Use:   "status <operation-id>",
	Short: "Read load-file import progress",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		operation, err := uuid.Parse(args[0])
		if err != nil {
			return usageError(errors.New("operation ID must be a UUID"))
		}
		result, err := client.API().ReadPackageImport(cmd.Context(), &apiclient.ReadPackageImportRequestOptions{
			PathParams: &apiclient.ReadPackageImportPath{OperationID: operation},
		})
		if err != nil {
			return err
		}
		return writePackageImport(cmd, result)
	},
}

var packageImportCancelCmd = &cobra.Command{
	Use:   "cancel <operation-id>",
	Short: "Cancel a queued or running load-file import",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		operation, err := uuid.Parse(args[0])
		if err != nil {
			return usageError(errors.New("operation ID must be a UUID"))
		}
		result, err := client.API().CancelPackageImport(cmd.Context(), &apiclient.CancelPackageImportRequestOptions{
			PathParams: &apiclient.CancelPackageImportPath{OperationID: operation},
		})
		if err != nil {
			return err
		}
		return writePackageImport(cmd, result)
	},
}

func writePackageImport(cmd *cobra.Command, result *api.PackageImportJob) error {
	if packageImportJSON {
		return writeCLIJSON(cmd.OutOrStdout(), result)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "import %s: %s (%d/%d, gaps=%d)\n",
		result.OperationID, result.State, result.Committed, result.Total, result.GapCount)
	if err != nil {
		return fmt.Errorf("writing package import status: %w", err)
	}
	return nil
}

func packagePreflightSource(source string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("open package directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("package source must be a directory")
	}
	return filepath.Abs(source)
}

func init() {
	packagePreflightCmd.Flags().StringVar(&packagePreflightProfile, "profile", "", "declared load-file profile")
	packagePreflightCmd.Flags().StringVar(&packagePreflightPageMapProfile, "page-map-profile", "", "page-map profile: opt-standard-v1 (OPT default), opt-pagecount5-v1, or lfp-ipro-v1 (LFP default)")
	packagePreflightCmd.Flags().StringVar(&packagePreflightEncoding, "encoding", "", "declared source encoding")
	packagePreflightCmd.Flags().StringVar(&packagePreflightMap, "map", "", "loadfile-mapping/v1 JSON file")
	packagePreflightCmd.Flags().BoolVar(&packagePreflightJSON, "json", false, "emit machine-readable JSON")
	packageImportCmd.Flags().StringVar(&packageImportInto, "into", "/", "existing destination folder")
	packageImportCmd.Flags().StringVar(&packageImportName, "name", "", "stable package name")
	packageImportCmd.Flags().StringVar(&packageImportParty, "party", "", "sending party label")
	packageImportCmd.Flags().StringVar(&packageImportOperation, "operation-id", "", "version-4 UUID for idempotent retry")
	packageImportCmd.Flags().BoolVar(&packageImportPartial, "accept-partial", false, "retain supported records and report gaps")
	packageImportCmd.Flags().BoolVar(&packageImportSuppliedText, "index-supplied-text", false, "index package-supplied text")
	packageImportCmd.Flags().BoolVar(&packageImportJSON, "json", false, "emit machine-readable JSON")
	packageImportStatusCmd.Flags().BoolVar(&packageImportJSON, "json", false, "emit machine-readable JSON")
	packageImportCancelCmd.Flags().BoolVar(&packageImportJSON, "json", false, "emit machine-readable JSON")
	packageCmd.AddCommand(packagePreflightCmd)
	packageImportCmd.AddCommand(packageImportStatusCmd, packageImportCancelCmd)
	packageCmd.AddCommand(packageImportCmd)
	rootCmd.AddCommand(packageCmd)
}
