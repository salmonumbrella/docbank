package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
)

func TestTimelineBrowserAllowlist(t *testing.T) {
	for _, path := range []string{"/api/v1/timeline/day", "/api/v1/timeline/days"} {
		require.True(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodPost, path, nil)))
		require.False(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodPost, path+"?disclosure=full", nil)))
		require.False(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodGet, path, nil)))
	}
}

func TestTimelinePageWhenAndCitation(t *testing.T) {
	minute := 570
	page := store.TimelineDayPage{Items: []store.TimelineItem{{NodeID: 7, VersionID: "11111111-1111-4111-8111-111111111111", BlobHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Rule: "date-kind-switch", AxisMinute: &minute, Event: document.DocumentEventV1{EventID: "event", DateKind: "sent", Precision: "minute", DateValue: "2026-03-11T09:30", TimezoneKind: "omitted", ClaimBasis: "source_asserted"}}}, DateOnly: []store.TimelineItem{{EndDate: "2026-03-12", Event: document.DocumentEventV1{DateKind: "started", Precision: "date", DateValue: "2026-03-10"}}}, MonthOnly: []store.TimelineItem{{Event: document.DocumentEventV1{DateKind: "created", Precision: "month", DateValue: "2026-03"}}}}
	out := timelinePageAPI("vault", "2026-03-11", page)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	item := value["items"].([]any)[0].(map[string]any)
	when := item["when"].(map[string]any)
	require.Equal(t, "floating", when["state"])
	require.Equal(t, "2026-03-11", when["day"])
	require.EqualValues(t, 570, when["axis_minute"])
	require.NotContains(t, when, "at")
	require.Equal(t, "date-kind-switch", when["placed_by"].(map[string]any)["rule"])
	require.Equal(t, map[string]any{"vault_uid": "vault", "node_id": float64(7), "content_version_id": "11111111-1111-4111-8111-111111111111", "content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, item["citation"])
	date := value["date_only"].([]any)[0].(map[string]any)["when"].(map[string]any)
	require.Equal(t, "2026-03-10", date["date"])
	require.Equal(t, "2026-03-12", date["end_date"])
	require.NotContains(t, date, "axis_minute")
	require.NotContains(t, date, "at")
	ended := timelinePageAPI("vault", "2026-03-12", store.TimelineDayPage{DateOnly: []store.TimelineItem{{Event: document.DocumentEventV1{DateKind: "ended", Precision: "date", DateValue: "2026-03-12"}}}}).DateOnly[0].When
	require.Equal(t, "2026-03-12", ended.Date)
	require.Empty(t, ended.EndDate)
	coarse := value["month_only"].([]any)[0].(map[string]any)["when"].(map[string]any)
	require.Equal(t, "2026-03", coarse["period"])
	require.NotContains(t, coarse, "day")
	require.NotContains(t, coarse, "axis_minute")
}

func TestTimelineWhenContractExamples(t *testing.T) {
	cases := []struct {
		file   string
		event  document.DocumentEventV1
		minute *int
		end    string
	}{
		{"example-1.json", document.DocumentEventV1{Precision: "second", DateValue: "2026-03-10T23:40:00", TimezoneKind: "offset", ZoneText: "-04:00", OffsetSeconds: new(-14400), UTCKey: "2026-03-11T03:40:00.000000000Z"}, new(1420), ""},
		{"example-2.json", document.DocumentEventV1{Precision: "second", DateValue: "2026-03-10T23:40:00", TimezoneKind: "offset", OffsetSeconds: new(-14400), UTCKey: "2026-03-11T03:40:00.000000000Z"}, new(1420), ""},
		{"example-3.json", document.DocumentEventV1{Precision: "second", DateValue: "2026-03-10T23:30:00", TimezoneKind: "omitted"}, new(1410), ""},
		{"example-4.json", document.DocumentEventV1{Precision: "date", DateValue: "2026-03-10"}, nil, ""},
		{"example-5.json", document.DocumentEventV1{Precision: "date", DateValue: "2026-03-09"}, nil, "2026-03-11"},
		{"example-6.json", document.DocumentEventV1{Precision: "second", DateValue: "2026-03-10T23:50:00", TimezoneKind: "utc", ZoneText: "UTC", UTCKey: "2026-03-10T23:50:00.000000000Z"}, new(1190), ""},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			tc.event.EventID, tc.event.DateKind, tc.event.ClaimBasis = "evt-55", "created", "source_asserted"
			if tc.end != "" {
				tc.event.DateKind = "started"
			}
			item := store.TimelineItem{TimelinePlacement: store.TimelinePlacement{AxisMinute: tc.minute, Rule: "primary-rule/v1"}, Event: tc.event, EndDate: tc.end}
			page := timelinePageAPI("vault", "2026-03-10", store.TimelineDayPage{Items: []store.TimelineItem{item}})
			actual, err := json.Marshal(page.Items[0].When)
			require.NoError(t, err)
			fixture, err := os.ReadFile(filepath.Join("testdata/when", tc.file))
			require.NoError(t, err)
			var got, want map[string]any
			require.NoError(t, json.Unmarshal(actual, &got))
			require.NoError(t, json.Unmarshal(fixture, &want))
			// Docbank stores individual claims, not timed end instants.
			delete(want, "end_at")
			delete(want, "end_local")
			want["placed_by"] = map[string]any{"source": string(tc.event.DateKind), "rule": "primary-rule/v1", "fallback": false, "event_id": "evt-55", "claim_basis": "source_asserted"}
			require.Equal(t, want, got)
		})
	}
}
