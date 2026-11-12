package main

import (
	"bytes"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/query"
)

func TestTimelineQueryAndZoneOutput(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		syntax string
		text   string
	}{
		{nil, "simple", ""},
		{[]string{"name:report"}, "advanced", "name:report"},
		{[]string{`{"syntax":"simple","text":"report"}`}, "simple", "report"},
	} {
		raw, err := timelineQuery(tc.args)
		require.NoError(t, err)
		parsed, err := query.Parse(raw)
		require.NoError(t, err)
		require.Equal(t, tc.syntax, parsed.Syntax)
		require.Equal(t, tc.text, parsed.Text)
	}
	_, err := timelineQuery([]string{`{"unexpected":true}`})
	require.Error(t, err)
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)
	timelineZoneLine(cmd, "America/Toronto", true)
	timelineZoneLine(cmd, "UTC", false)
	require.Equal(t, "timezone: America/Toronto (default)\ntimezone: UTC (explicit)\n", stderr.String())
}

func TestTimelineCLIJSONAndTimezone(t *testing.T) {
	_ = setupVaultHome(t)
	for _, tc := range []struct {
		args []string
		line string
	}{
		{[]string{"timeline", "day", "2026-03-10"}, "timezone: UTC (default)\n"},
		{[]string{"timeline", "days", "--from", "2026-03-10", "--to", "2026-03-10", "--timezone", "UTC"}, "timezone: UTC (explicit)\n"},
	} {
		out, err := runCLI(t, tc.args...)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(out, tc.line), out)
		var value map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(out, tc.line)), &value))
		require.Equal(t, "UTC", value["timezone"])
		require.Equal(t, tc.line == "timezone: UTC (default)\n", value["timezone_defaulted"])
	}
}
