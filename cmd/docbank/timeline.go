package main

import (
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/query"
)

func timelineQuery(args []string) (api.QueryPayload, error) {
	raw := "{}"
	if len(args) > 0 {
		if strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
			raw = args[0]
		} else {
			encoded, err := json.Marshal(struct {
				Syntax string `json:"syntax"`
				Text   string `json:"text"`
			}{"advanced", args[0]})
			if err != nil {
				return nil, err
			}
			raw = string(encoded)
		}
	}
	if _, err := query.Parse([]byte(raw)); err != nil {
		return nil, usageError(err)
	}
	return api.QueryPayload(raw), nil
}
func timelineZone(cmd *cobra.Command, zone string) *string {
	if cmd.Flags().Changed("timezone") {
		return &zone
	}
	return nil
}
func timelineZoneLine(cmd *cobra.Command, zone string, defaulted bool) {
	origin := "explicit"
	if defaulted {
		origin = "default"
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "timezone: %s (%s)\n", zone, origin)
}

func newTimelineCommand() *cobra.Command {
	root := &cobra.Command{Use: "timeline", Short: "Read documents by civil day"}
	var from, to, zone string
	var kinds []string
	days := &cobra.Command{Use: "days [query]", Short: "Count documents per day", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		q, err := timelineQuery(args)
		if err != nil {
			return err
		}
		conn, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		out, err := conn.TimelineDays(cmd.Context(), api.TimelineDaysRequest{Query: q, Timezone: timelineZone(cmd, zone), From: from, To: to, DateKinds: kinds})
		if err != nil {
			return err
		}
		timelineZoneLine(cmd, out.Timezone, out.TimezoneDefaulted)
		return writeCLIJSON(cmd.OutOrStdout(), out)
	}}
	days.Flags().StringVar(&from, "from", "", "first inclusive date (YYYY-MM-DD)")
	days.Flags().StringVar(&to, "to", "", "last inclusive date (YYYY-MM-DD)")
	days.Flags().StringVar(&zone, "timezone", "", "IANA timeline timezone")
	days.Flags().StringSliceVar(&kinds, "date-kind", nil, "date kind to include (repeatable)")
	_ = days.MarkFlagRequired("from")
	_ = days.MarkFlagRequired("to")
	var dayZone, cursor string
	var dayKinds []string
	var limit int
	day := &cobra.Command{Use: "day <date> [query]", Short: "List documents on a day", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		q, err := timelineQuery(args[1:])
		if err != nil {
			return err
		}
		conn, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		out, err := conn.TimelineDay(cmd.Context(), api.TimelineDayRequest{Query: q, Timezone: timelineZone(cmd, dayZone), Date: args[0], DateKinds: dayKinds, Cursor: cursor, Limit: limit})
		if err != nil {
			return err
		}
		timelineZoneLine(cmd, out.Timezone, out.TimezoneDefaulted)
		return writeCLIJSON(cmd.OutOrStdout(), out)
	}}
	day.Flags().StringVar(&dayZone, "timezone", "", "IANA timeline timezone")
	day.Flags().StringSliceVar(&dayKinds, "date-kind", nil, "date kind to include (repeatable)")
	day.Flags().StringVar(&cursor, "cursor", "", "continuation cursor")
	day.Flags().IntVar(&limit, "limit", 200, "timed items per page (1–500)")
	root.AddCommand(days, day)
	return root
}
func init() { rootCmd.AddCommand(newTimelineCommand()) }
