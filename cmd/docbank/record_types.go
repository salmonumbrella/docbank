package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/daemonconn"
)

func init() {
	var includeInactive, listJSON bool
	list := &cobra.Command{Use: "list", Short: "List record types", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		items, err := c.ListRecordTypes(cmd.Context(), includeInactive)
		if err != nil {
			return err
		}
		if listJSON {
			return writeCLIJSON(cmd.OutOrStdout(), items)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "TYPE\tACTIVE\tREVISION")
		for _, v := range items {
			_, _ = fmt.Fprintf(w, "%s\t%t\t%d\n", v.Definition.ID, v.Active, v.Revision)
		}
		return w.Flush()
	}}
	list.Flags().BoolVar(&includeInactive, "include-inactive", false, "Include inactive types")
	list.Flags().BoolVar(&listJSON, "json", false, "Write JSON")
	var showJSON bool
	show := &cobra.Command{Use: "show <type>", Short: "Inspect a record type", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		v, err := c.GetRecordType(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if showJSON {
			return writeCLIJSON(cmd.OutOrStdout(), v)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\nActive: %t\nRevision: %d\n", v.Definition.Label, v.Definition.ID, v.Active, v.Revision)
		for _, f := range v.Definition.Fields {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", f.ID, f.Type)
		}
		return nil
	}}
	show.Flags().BoolVar(&showJSON, "json", false, "Write JSON")
	recordTypesCmd.AddCommand(list, show)
	for _, active := range []bool{true, false} {
		name := "deactivate"
		if active {
			name = "activate"
		}
		var expected int64
		var asJSON bool
		command := &cobra.Command{Use: name + " <type>", Short: name + " a record type", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("expect-revision") && expected < 0 {
				return fmt.Errorf("expected revision cannot be negative")
			}
			c, err := daemonconn.Ensure(cmd.Context())
			if err != nil {
				return err
			}
			v, err := c.GetRecordType(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			revision := v.Revision
			if cmd.Flags().Changed("expect-revision") {
				revision = expected
			}
			v, err = c.SetRecordTypeActive(cmd.Context(), args[0], revision, active)
			if err != nil {
				return recordUnknownOutcome(err)
			}
			if asJSON {
				return writeCLIJSON(cmd.OutOrStdout(), v)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s active=%t (revision %d)\n", v.Definition.ID, v.Active, v.Revision)
			return nil
		}}
		command.Flags().Int64Var(&expected, "expect-revision", 0, "Expected revision")
		command.Flags().BoolVar(&asJSON, "json", false, "Write JSON")
		recordTypesCmd.AddCommand(command)
	}
}
