package store

import (
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/civiltime"
)

func TestTimelineDayBoundsVectors(t *testing.T) {
	for _, tc := range []struct {
		zone, date, start string
		minutes, ticks    int
	}{
		{"America/Santiago", "2026-09-06", "2026-09-06T04:00:00Z", 1380, 23},
		{"America/Havana", "2026-03-08", "2026-03-08T05:00:00Z", 1380, 23},
		{"America/Toronto", "2026-03-08", "2026-03-08T05:00:00Z", 1380, 23},
		{"America/Toronto", "2026-11-01", "2026-11-01T04:00:00Z", 1500, 25},
		{"America/St_Johns", "2010-11-07", "2010-11-07T02:30:00Z", 1500, 26},
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			loc, err := civiltime.LoadZone(tc.zone)
			require.NoError(t, err)
			date, err := civiltime.ParseDate(tc.date)
			require.NoError(t, err)
			bounds, ok := timelineDayBounds(date, loc)
			require.True(t, ok)
			require.Equal(t, tc.start, bounds.Start.Format("2006-01-02T15:04:05Z"))
			require.Equal(t, tc.minutes, bounds.Minutes)
			require.Len(t, bounds.Ticks, tc.ticks)
			if tc.zone == "America/Toronto" && tc.date == "2026-11-01" {
				require.Equal(t, TimelineTick{Minute: 60, Label: "01:00 −04:00"}, bounds.Ticks[1])
				require.Equal(t, TimelineTick{Minute: 120, Label: "01:00 −05:00"}, bounds.Ticks[2])
			}
		})
	}
	loc, err := civiltime.LoadZone("Pacific/Apia")
	require.NoError(t, err)
	date, err := civiltime.ParseDate("2011-12-30")
	require.NoError(t, err)
	_, ok := timelineDayBounds(date, loc)
	require.False(t, ok)
}
