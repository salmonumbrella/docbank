package main

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/daemonconn"
)

func recordAssignments(sets, clears []string, current map[string]any) (map[string]any, error) {
	fields := map[string]any{}
	seen := map[string]bool{}
	apply := func(key string, value any) error {
		if seen[key] || ((key == "term" && (seen["term.start"] || seen["term.end"])) || (strings.HasPrefix(key, "term.") && seen["term"])) {
			return fmt.Errorf("field %s is assigned more than once", key)
		}
		seen[key] = true
		switch key {
		case "contract_type", "signed_on", "summary", "purpose", "consideration", "termination", "term":
			fields[key] = value
		case "term.start", "term.end":
			m, ok := fields["term"].(map[string]any)
			if !ok {
				m = map[string]any{}
				if previous, ok := current["term"].(map[string]any); ok {
					for k, v := range previous {
						m[k] = v
					}
				}
				fields["term"] = m
			}
			_, part, _ := strings.Cut(key, ".")
			m[part] = value
		default:
			return fmt.Errorf("unknown field %s", key)
		}
		return nil
	}
	for _, entry := range sets {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("expected field=value")
		}
		if err := apply(key, value); err != nil {
			return nil, err
		}
	}
	for _, key := range clears {
		if err := apply(key, nil); err != nil {
			return nil, err
		}
	}
	return fields, nil
}
func recordUnknownOutcome(err error) error {
	if err == nil || !daemonconn.IsRetryableMutationError(err) {
		return err
	}
	return fmt.Errorf("%w; inspect the record before retrying because the outcome may be unknown", err)
}
func init() {
	var listType, listStatus, listQuery, listAsOf string
	var filters, before, after []string
	var limit, offset int
	var listJSON bool
	list := &cobra.Command{Use: "list", Short: "List live records of a type", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		page, err := c.ListRecords(cmd.Context(), daemonconn.RecordListOptions{TypeID: listType, Status: listStatus, Query: listQuery, AsOf: listAsOf, Fields: filters, Before: before, After: after, Limit: limit, Offset: offset})
		if err != nil {
			return err
		}
		if listJSON {
			return writeCLIJSON(cmd.OutOrStdout(), page)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tTITLE\tSTATUS\tREVISION")
		for _, r := range page.Records {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", r.RecordID, r.Title, r.EffectiveStatus, r.Revision)
		}
		if err = w.Flush(); err != nil {
			return err
		}
		if page.Offset+len(page.Records) < page.Total {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "More records: --offset %d\n", page.Offset+len(page.Records))
		}
		return nil
	}}
	list.Flags().StringVar(&listType, "type", "", "Record type")
	_ = list.MarkFlagRequired("type")
	list.Flags().StringVar(&listStatus, "status", "", "Effective status")
	list.Flags().StringVar(&listQuery, "q", "", "Literal title substring")
	list.Flags().StringVar(&listAsOf, "as-of", "", "Date for derived status")
	list.Flags().StringArrayVar(&filters, "field", nil, "Field equality selector field=value")
	list.Flags().StringArrayVar(&before, "before", nil, "Exclusive date selector field=YYYY-MM-DD")
	list.Flags().StringArrayVar(&after, "after", nil, "Exclusive date selector field=YYYY-MM-DD")
	list.Flags().IntVar(&limit, "limit", 100, "Maximum records")
	list.Flags().IntVar(&offset, "offset", 0, "Number to skip")
	list.Flags().BoolVar(&listJSON, "json", false, "Write JSON")
	var showJSON bool
	show := &cobra.Command{Use: "show <id>", Short: "Inspect a record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		r, err := c.GetRecord(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if showJSON {
			return writeCLIJSON(cmd.OutOrStdout(), r)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\nType: %s\nStatus: %s\nRevision: %d\nNote: %s\n", r.Title, r.RecordID, r.TypeID, r.EffectiveStatus, r.Revision, r.Note)
		keys := make([]string, 0, len(r.Fields))
		for k := range r.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %v\n", k, r.Fields[k])
		}
		return nil
	}}
	show.Flags().BoolVar(&showJSON, "json", false, "Write JSON")
	var createSets []string
	var createStatus, createNote string
	var createJSON bool
	create := &cobra.Command{Use: "create <type> <title>", Short: "Create a record", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		fields, err := recordAssignments(createSets, nil, nil)
		if err != nil {
			return err
		}
		body := map[string]any{"type_id": args[0], "title": args[1], "fields": fields}
		if cmd.Flags().Changed("status") {
			body["status"] = createStatus
		}
		if cmd.Flags().Changed("note") {
			body["note"] = createNote
		}
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		r, err := c.CreateRecord(cmd.Context(), body)
		if err != nil {
			return recordUnknownOutcome(err)
		}
		if createJSON {
			return writeCLIJSON(cmd.OutOrStdout(), r)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s)\n", r.Title, r.RecordID)
		return nil
	}}
	create.Flags().StringArrayVar(&createSets, "set", nil, "Set field=value")
	create.Flags().StringVar(&createStatus, "status", "", "Stored status")
	create.Flags().StringVar(&createNote, "note", "", "Note")
	create.Flags().BoolVar(&createJSON, "json", false, "Write JSON")
	var editSets, editClears []string
	var editTitle, editStatus, editNote string
	var editRevision int64
	var editJSON bool
	edit := &cobra.Command{Use: "edit <id>", Short: "Edit a record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("expect-revision") && editRevision < 1 {
			return fmt.Errorf("expected revision must be positive")
		}
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		old, err := c.GetRecord(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fields, err := recordAssignments(editSets, editClears, old.Fields)
		if err != nil {
			return err
		}
		body := map[string]any{}
		if len(fields) > 0 {
			body["fields"] = fields
		}
		if cmd.Flags().Changed("title") {
			body["title"] = editTitle
		}
		if cmd.Flags().Changed("status") {
			body["status"] = editStatus
		}
		if cmd.Flags().Changed("note") {
			body["note"] = editNote
		}
		if len(body) == 0 {
			return fmt.Errorf("no changes specified")
		}
		revision := old.Revision
		if cmd.Flags().Changed("expect-revision") {
			revision = editRevision
		}
		r, err := c.UpdateRecord(cmd.Context(), args[0], revision, body)
		if err != nil {
			return recordUnknownOutcome(err)
		}
		if editJSON {
			return writeCLIJSON(cmd.OutOrStdout(), r)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Updated %s (revision %d)\n", r.RecordID, r.Revision)
		return nil
	}}
	edit.Flags().StringArrayVar(&editSets, "set", nil, "Set field=value")
	edit.Flags().StringArrayVar(&editClears, "clear", nil, "Clear a field or date endpoint")
	edit.Flags().StringVar(&editTitle, "title", "", "Title")
	edit.Flags().StringVar(&editStatus, "status", "", "Stored status")
	edit.Flags().StringVar(&editNote, "note", "", "Note")
	edit.Flags().Int64Var(&editRevision, "expect-revision", 0, "Expected revision")
	edit.Flags().BoolVar(&editJSON, "json", false, "Write JSON")
	var retireRevision int64
	var retireJSON bool
	retire := &cobra.Command{Use: "retire <id>", Short: "Retire a record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("expect-revision") && retireRevision < 1 {
			return fmt.Errorf("expected revision must be positive")
		}
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		r, err := c.GetRecord(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		revision := r.Revision
		if cmd.Flags().Changed("expect-revision") {
			revision = retireRevision
		}
		r, err = c.RetireRecord(cmd.Context(), args[0], revision)
		if err != nil {
			return recordUnknownOutcome(err)
		}
		if retireJSON {
			return writeCLIJSON(cmd.OutOrStdout(), r)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Retired %s\n", r.RecordID)
		return nil
	}}
	retire.Flags().Int64Var(&retireRevision, "expect-revision", 0, "Expected revision")
	retire.Flags().BoolVar(&retireJSON, "json", false, "Write JSON")
	recordCmd.AddCommand(list, show, create, edit, retire)
}
