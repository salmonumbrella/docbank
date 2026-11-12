package api_test

import (
	"fmt"
	"net/http"
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
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
	response, body := rawJSONRequest(t, ts.URL, http.MethodPost, "/api/v1/timeline/days", headers, `{"query":{},"from":"2026-01-01","to":"2026-04-02","timezone":"UTC"}`)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
}
