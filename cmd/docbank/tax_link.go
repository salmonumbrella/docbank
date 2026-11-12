package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/canonical"
)

func printTaxDocuments(cmd *cobra.Command, docs []api.TaxDocumentLink) error {
	for _, d := range docs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "document %s\trole: %s\tnode: %v\tpath: %v\tevent: %v\tversion: %v\tsha256: %s\tlive: %t\tpinned: %t\tintact: %t\tretired: %v\n", d.LinkID, d.Role, taxValue(d.NodeID), taxValue(d.Path), taxValue(d.EventID), taxValue(d.ContentVersionID), d.ContentSHA256, d.Live, d.Pinned, d.Intact, taxValue(d.RetiredAt)); err != nil {
			return fmt.Errorf("print tax document: %w", err)
		}
	}
	return nil
}
func printTaxDocumentMutation(cmd *cobra.Command, m api.TaxDocumentMutation) error {
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		return writeCLIJSON(cmd.OutOrStdout(), m)
	}
	if err := printTaxRecord(cmd, m.Record); err != nil {
		return err
	}
	return printTaxDocuments(cmd, []api.TaxDocumentLink{m.Document})
}
func newTaxLinkCommand() *cobra.Command {
	group := &cobra.Command{Use: "link", Short: "Manage tax document links", Args: taxGroupArgs, Annotations: map[string]string{helpOnlyAnnotation: helpOnlyMarker}, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	var in api.LinkTaxDocumentRequest
	var event, version string
	var revision int64
	add := &cobra.Command{Use: "add <record-id>", Short: "Link a document", Example: "docbank tax link add 11111111-1111-4111-8111-111111111111 --node-id 2 --role return", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if in.NodeID < 1 {
			return usageError(errors.New("node ID must be positive"))
		}
		if cmd.Flags().Changed("event-id") {
			if canonical.ValidateUUIDv4(event) != nil {
				return usageError(errors.New("invalid event ID"))
			}
			in.EventID = &event
		}
		if cmd.Flags().Changed("content-version-id") {
			if canonical.ValidateUUIDv4(version) != nil {
				return usageError(errors.New("invalid content version ID"))
			}
			in.ContentVersionID = &version
		}
		c, rev, err := eventConnection(cmd, args[0], revision)
		if err != nil {
			return err
		}
		m, err := c.LinkTaxDocument(cmd.Context(), args[0], rev, in)
		if err != nil {
			return err
		}
		return printTaxDocumentMutation(cmd, m)
	}}
	add.Flags().Int64Var(&in.NodeID, "node-id", 0, "file node ID")
	add.Flags().StringVar(&in.Role, "role", "", "document role")
	add.Flags().StringVar(&event, "event-id", "", "associated event ID")
	add.Flags().StringVar(&version, "content-version-id", "", "pinned version ID")
	add.Flags().StringVar(&in.FormCode, "form-code", "", "form code")
	add.Flags().StringVar(&in.Note, "note", "", "link note")
	add.Flags().Int64Var(&revision, "revision", 0, "expected revision (default: current)")
	_ = add.MarkFlagRequired("node-id")
	_ = add.MarkFlagRequired("role")
	var removeRevision int64
	remove := &cobra.Command{Use: "remove <record-id> <link-id>", Short: "Retire a document link", Example: "docbank tax link remove 11111111-1111-4111-8111-111111111111 33333333-3333-4333-8333-333333333333", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if canonical.ValidateUUIDv4(args[1]) != nil {
			return usageError(errors.New("invalid link ID"))
		}
		c, rev, err := eventConnection(cmd, args[0], removeRevision)
		if err != nil {
			return err
		}
		m, err := c.RetireTaxDocument(cmd.Context(), args[0], args[1], rev)
		if err != nil {
			return err
		}
		return printTaxDocumentMutation(cmd, m)
	}}
	remove.Flags().Int64Var(&removeRevision, "revision", 0, "expected revision (default: current)")
	group.AddCommand(add, remove)
	return group
}
