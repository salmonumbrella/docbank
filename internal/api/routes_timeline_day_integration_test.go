package api_test

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/go-pdf/fpdf"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
)

func TestTimelineDayRequestValidation(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	headers := map[string]string{"X-Api-Key": testAPIKey}
	for _, tc := range []struct {
		path, body, code string
		status           int
	}{
		{"/api/v1/timeline/day", `{"query":{},"date":"2011-12-30","timezone":"Pacific/Apia"}`, "date_not_in_zone", 400},
		{"/api/v1/timeline/day", `{"query":{},"date":"2026-02-30"}`, "invalid_date", 400},
		{"/api/v1/timeline/day", `{"query":{},"date":"2026-03-10","timezone":"Mars/Olympus"}`, "invalid_timezone", 400},
		{"/api/v1/timeline/day", `{"query":{},"date":"2026-03-10","disclosure":"full"}`, "unknown_parameter", 400},
		{"/api/v1/timeline/day", `{"query":{},"date":"2026-03-10","surprise":true}`, "unknown_parameter", 400},
		{"/api/v1/timeline/days", `{"query":{},"from":"2026-03-10","to":"2026-03-09"}`, "invalid_date", 400},
		{"/api/v1/timeline/days", `{"query":{},"from":"2011-12-30","to":"2011-12-31","timezone":"Pacific/Apia"}`, "date_not_in_zone", 400},
		{"/api/v1/timeline/days", `{"query":{},"from":"2026-01-01","to":"2026-04-03"}`, "range_too_large", 400},
	} {
		t.Run(fmt.Sprintf("%s/%s", tc.path, tc.code), func(t *testing.T) {
			response, body := rawJSONRequest(t, ts.URL, http.MethodPost, tc.path, headers, tc.body)
			require.Equal(t, tc.status, response.StatusCode, body)
			require.Equal(t, tc.code, decodeProblem(t, body).Code)
		})
	}
	response, body := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{"unexpected":true},"date":"2026-03-10"}`)
	require.GreaterOrEqual(t, response.StatusCode, http.StatusBadRequest, body)
	response, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/days", headers, `{"query":{},"from":"2026-01-01","to":"2026-04-02","timezone":"UTC"}`)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/days", headers, `{"query":{},"from":"2011-12-29","to":"2011-12-31","timezone":"Pacific/Apia"}`)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
}

func TestTimelineHTTPBoundariesAndEcho(t *testing.T) {
	ts, catalog := newTestServer(t, func(d *api.Deps) { d.Cfg.Web.TimelineTimezone = "America/Toronto" })
	headers := map[string]string{"X-Api-Key": testAPIKey}
	for _, path := range []string{"/api/v1/timeline/day", "/api/v1/timeline/days"} {
		body := `{"query":{},"date":"2026-03-10"}`
		if strings.HasSuffix(path, "/days") {
			body = `{"query":{},"from":"2026-03-10","to":"2026-03-10"}`
		}
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, path, headers, body)
		require.Equal(t, http.StatusOK, resp.StatusCode, raw)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &got))
		require.Equal(t, "America/Toronto", got["timezone"])
		require.Equal(t, true, got["timezone_defaulted"])
		body = strings.TrimSuffix(body, "}") + `,"timezone":"UTC"}`
		resp, raw = rawJSONRequest(t, ts.URL, http.MethodPost, path, headers, body)
		require.Equal(t, http.StatusOK, resp.StatusCode, raw)
		require.NoError(t, json.Unmarshal([]byte(raw), &got))
		require.Equal(t, "UTC", got["timezone"])
		require.Equal(t, false, got["timezone_defaulted"])
		resp, _ = rawJSONRequest(t, ts.URL, http.MethodPost, path, map[string]string{"X-Api-Key": "wrong"}, body)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp, raw = rawJSONRequest(t, ts.URL, http.MethodPost, path+"?disclosure=full", headers, body)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, raw)
		require.Equal(t, "unknown_parameter", decodeProblem(t, raw).Code)
		resp, _ = rawJSONRequest(t, ts.URL, http.MethodPost, path, headers, `{"padding":"`+strings.Repeat("x", 2<<20)+`"}`)
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	}
	for _, limit := range []int{1, 500} {
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, fmt.Sprintf(`{"query":{},"date":"2026-03-10","limit":%d}`, limit))
		require.Equal(t, http.StatusOK, resp.StatusCode, raw)
	}
	for _, limit := range []int{-1, 0, 501} {
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, fmt.Sprintf(`{"query":{},"date":"2026-03-10","limit":%d}`, limit))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, raw)
	}
	for _, count := range []int{16, 17} {
		kinds := strings.TrimSuffix(strings.Repeat(`"sent",`, count), ",")
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{},"date":"2026-03-10","date_kinds":[`+kinds+`]}`)
		if count == 16 {
			require.Equal(t, http.StatusOK, resp.StatusCode, raw)
		} else {
			require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, raw)
		}
	}
	for _, length := range []int{2048, 2049} {
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{},"date":"2026-03-10","cursor":"`+strings.Repeat("a", length)+`"}`)
		require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, raw)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, raw)
		require.Equal(t, "cursor_mismatch", decodeProblem(t, raw).Code)
	}
	_, err := catalog.CreateFile(t.Context(), catalog.RootID(), "pending.txt", strings.Repeat("a", 64), 1, "text/plain")
	require.NoError(t, err)
	for _, request := range []struct{ path, body string }{
		{"/api/v1/timeline/day", `{"query":{},"date":"2026-03-10"}`},
		{"/api/v1/timeline/days", `{"query":{},"from":"2026-03-10","to":"2026-03-10"}`},
	} {
		resp, raw := rawJSONRequest(t, ts.URL, http.MethodPost, request.path, headers, request.body)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, raw)
		require.Equal(t, "document_dates_pending", decodeProblem(t, raw).Code)
	}
}

func TestTimelineHTTPPublishedDatesAndFrozenPages(t *testing.T) {
	ts, s := newTestServer(t, func(d *api.Deps) { d.Cfg.Web.TimelineTimezone = "UTC" })
	var version string
	for i := range 2 {
		node, err := s.CreateFile(t.Context(), s.RootID(), fmt.Sprintf("report-%d.pdf", i), testHash(fmt.Sprintf("timeline-%d", i)), 1, "application/pdf")
		require.NoError(t, err)
		version = node.CurrentVersionID
		pdf := fpdf.New("P", "mm", "A4", "")
		pdf.SetCreationDate(time.Date(2026, 3, 10, 9, i, 0, 0, time.UTC))
		pdf.SetModificationDate(time.Date(2026, 3, 10, 10, i, 0, 0, time.UTC))
		pdf.AddPage()
		var encoded bytes.Buffer
		require.NoError(t, pdf.Output(&encoded))
		metadata, err := processing.ExtractSourceMetadata(t.Context(), t.TempDir(), encoded.Bytes())
		require.NoError(t, err)
		raw, _, err := document.MarshalSourceMetadataV1(metadata)
		require.NoError(t, err)
		_, err = s.PublishSourceMetadata(t.Context(), node.BlobHash, testHash("timeline-extractor"), raw)
		require.NoError(t, err)
	}
	targets, err := s.MissingDocumentEventTargetsAfter(t.Context(), store.DocumentEventsDeriverFingerprint, "", 100)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	for _, target := range targets {
		evidence, err := s.LoadDocumentEventEvidence(t.Context(), target)
		require.NoError(t, err)
		record, _, err := processing.DeriveDocumentEvents(evidence)
		require.NoError(t, err)
		raw, _, err := document.MarshalDocumentEventsV1(record)
		require.NoError(t, err)
		_, err = s.PublishDocumentEvents(t.Context(), target, store.DocumentEventsDeriverFingerprint, evidence.InputsSHA256, raw)
		require.NoError(t, err)
	}
	person, err := s.CreatePerson(t.Context(), "Synthetic Blind Recipient", "operator")
	require.NoError(t, err)
	_, err = s.AssertDocumentPerson(t.Context(), store.PersonDocumentAssertion{ContentVersionID: version, PersonID: person.PersonID, Role: "blind_copy", Action: "assert", Revision: 1})
	require.NoError(t, err)
	headers := map[string]string{"X-Api-Key": testAPIKey}
	request := `{"query":{},"date":"2026-03-10","limit":1,"date_kinds":["created"]}`
	resp, body := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, request)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var first api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &first))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	require.Equal(t, "created", string(first.Items[0].When.PlacedBy.Source))
	require.NotEmpty(t, first.Items[0].Citation.ContentSHA256)
	continuation := fmt.Sprintf(`{"query":{},"date":"2026-03-10","limit":1,"date_kinds":["created"],"cursor":%q}`, first.NextCursor)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, continuation)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var second api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &second))
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].NodeID, second.Items[0].NodeID)
	require.Equal(t, "UTC", second.Timezone)
	require.True(t, second.TimezoneDefaulted)
	explicitRequest := strings.TrimSuffix(request, "}") + `,"timezone":"UTC"}`
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, explicitRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var explicit api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &explicit))
	require.False(t, explicit.TimezoneDefaulted)
	omitted := fmt.Sprintf(`{"query":{},"date":"2026-03-10","limit":1,"date_kinds":["created"],"cursor":%q}`, explicit.NextCursor)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, omitted)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var pinned api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &pinned))
	require.Equal(t, "UTC", pinned.Timezone)
	require.False(t, pinned.TimezoneDefaulted)
	mismatch := strings.Replace(continuation, `"date":"2026-03-10"`, `"date":"2026-03-11"`, 1)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, mismatch)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	require.Equal(t, "cursor_mismatch", decodeProblem(t, body).Code)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/days", headers, `{"query":{},"from":"2026-03-10","to":"2026-03-10","date_kinds":["created"]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var counts api.TimelineDaysResponse
	require.NoError(t, json.Unmarshal([]byte(body), &counts))
	require.Len(t, counts.Days, 1)
	require.EqualValues(t, 2, counts.Days[0].Count)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{},"date":"2026-03-10","date_kinds":["modified"]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var switched api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &switched))
	require.Len(t, switched.Items, 2)
	require.Equal(t, "date-kind-switch", switched.Items[0].When.PlacedBy.Rule)
	createSavedQuery(t, ts.URL, "ZoneScope", `{"syntax":"advanced","text":"date:2026-03-10","timezone":"UTC"}`)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{"syntax":"advanced","text":"saved:ZoneScope"},"date":"2026-03-10","timezone":"UTC"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var saved api.TimelineDayResponse
	require.NoError(t, json.Unmarshal([]byte(body), &saved))
	require.Equal(t, "UTC", saved.Timezone)
	require.False(t, saved.TimezoneDefaulted)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/days", headers, `{"query":{"filters":{"paths":["/report-0.pdf"]}},"from":"2026-03-10","to":"2026-03-10"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var scoped api.TimelineDaysResponse
	require.NoError(t, json.Unmarshal([]byte(body), &scoped))
	require.Len(t, scoped.Days, 1)
	require.EqualValues(t, 1, scoped.Days[0].Count)
	resp, body = do(t, ts, http.MethodPost, "/api/daemon/web-session", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var session struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &session))
	web := map[string]string{"X-Api-Key": "", api.WebSessionHeader: session.Token}
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", web, `{"query":{},"date":"2026-03-10","limit":500}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NotContains(t, body, person.PersonID)
	require.NotContains(t, body, "Synthetic Blind Recipient")
	resp, _ = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day?disclosure=full", web, request)
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, `{"query":{},"date":"2026-03-10","limit":500}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NotContains(t, body, person.PersonID)
	// New snapshots evict the oldest owner handle without waiting for the clock.
	for range 9 {
		resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, request)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	resp, body = rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/day", headers, continuation)
	require.Equal(t, http.StatusGone, resp.StatusCode, body)
	require.Equal(t, "snapshot_gone", decodeProblem(t, body).Code)
}

func TestTimelineTextCoverageProfileAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []string
		status   int
	}{
		{name: "unconfigured", status: http.StatusUnprocessableEntity},
		{name: "configured", profiles: []string{"first"}, status: http.StatusOK},
		{name: "ambiguous", profiles: []string{"first", "second"}, status: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newTestServer(t, func(d *api.Deps) { collectionCoverageConfig(d, tc.profiles...) })
			headers := map[string]string{"X-Api-Key": testAPIKey}
			for _, request := range []struct{ route, body string }{
				{"/api/v1/timeline/days", `{"query":{"syntax":"advanced","text":"text_coverage:unprocessed"},"from":"2026-03-10","to":"2026-03-10"}`},
				{"/api/v1/timeline/day", `{"query":{"syntax":"advanced","text":"text_coverage:unprocessed"},"date":"2026-03-10"}`},
			} {
				resp, body := rawJSONRequest(t, ts.URL, http.MethodPost, request.route, headers, request.body)
				require.Equal(t, tc.status, resp.StatusCode, body)
				if tc.status != http.StatusOK {
					require.Contains(t, body, `"code":"invalid_profile"`)
				}
			}
		})
	}
}
