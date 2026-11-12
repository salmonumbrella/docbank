package daemonconn

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestTimelineClientWiring(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "synthetic-key", r.Header.Get("X-Api-Key"))
		var body map[string]any
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Equal(t, "UTC", body["timezone"])
		require.Equal(t, []any{"sent"}, body["date_kinds"])
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/timeline/day":
			require.Equal(t, "2026-03-10", body["date"])
			require.Equal(t, "opaque", body["cursor"])
			require.EqualValues(t, 5, body["limit"])
			_, _ = w.Write([]byte(`{"date":"2026-03-10","timezone":"UTC","timezone_defaulted":false,"items":[],"date_only":[],"month_only":[],"next_cursor":"next"}`))
		case "/api/v1/timeline/days":
			require.Equal(t, "2026-03-10", body["from"])
			require.Equal(t, "2026-03-11", body["to"])
			_, _ = w.Write([]byte(`{"timezone":"UTC","timezone_defaulted":false,"from":"2026-03-10","to":"2026-03-11","days":[{"date":"2026-03-10","count":3}],"months":[],"undated_count":1,"state":{"pending_versions":0}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c := New(server.URL, "synthetic-key")
	zone := "UTC"
	day, err := c.TimelineDay(t.Context(), api.TimelineDayRequest{Query: api.QueryPayload(`{}`), Timezone: &zone, Date: "2026-03-10", DateKinds: []string{"sent"}, Cursor: "opaque", Limit: 5})
	require.NoError(t, err)
	require.Equal(t, "next", day.NextCursor)
	require.Equal(t, "UTC", day.Timezone)
	require.False(t, day.TimezoneDefaulted)
	days, err := c.TimelineDays(t.Context(), api.TimelineDaysRequest{Query: api.QueryPayload(`{}`), Timezone: &zone, From: "2026-03-10", To: "2026-03-11", DateKinds: []string{"sent"}})
	require.NoError(t, err)
	require.Len(t, days.Days, 1)
	require.EqualValues(t, 3, days.Days[0].Count)
	require.EqualValues(t, 1, days.UndatedCount)
}
