package main

import (
	"bytes"
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
