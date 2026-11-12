package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestRecordTypeActivationRequiresRevisionZero(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodGet, "/api/v1/record-types/contract", nil, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	response, body = do(t, ts, http.MethodPost, "/api/v1/record-types/contract/activate", map[string]string{"If-Match": `"1"`}, nil)
	require.Equal(t, http.StatusPreconditionFailed, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, "/api/v1/record-types/contract/activate", map[string]string{"If-Match": `"0"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	require.Equal(t, `"1"`, response.Header.Get("ETag"))
}

func TestRecordCreateRejectsExplicitNullAndEmptyPatch(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodPost, "/api/v1/record-types/contract/activate", map[string]string{"If-Match": `"0"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, "/api/v1/records", nil, map[string]any{"type_id": "contract", "title": "Example", "status": nil})
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, "/api/v1/records", nil, map[string]any{"type_id": "contract", "title": "Default status"})
	require.Equal(t, http.StatusCreated, response.StatusCode, body)
	require.Contains(t, body, `"status":"draft"`)
	for _, field := range []string{"note", "fields"} {
		response, body = do(t, ts, http.MethodPost, "/api/v1/records", nil, map[string]any{"type_id": "contract", "title": "Example", field: nil})
		require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
	}
	for _, field := range []string{"title", "note", "status", "fields"} {
		response, body = do(t, ts, http.MethodPatch, "/api/v1/records/00000000-0000-4000-8000-000000000000", map[string]string{"If-Match": `"1"`}, map[string]any{field: nil})
		require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
	}
	response, body = do(t, ts, http.MethodPatch, "/api/v1/records/00000000-0000-4000-8000-000000000000", map[string]string{"If-Match": `"1"`}, map[string]any{"$schema": "https://example.com/schema"})
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
}

func TestRecordRoutesFenceEditAndRetirement(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodPost, "/api/v1/record-types/contract/activate", map[string]string{"If-Match": `"0"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, "/api/v1/records", nil, map[string]any{"type_id": "contract", "title": "Synthetic agreement", "status": "active", "fields": map[string]any{"term": map[string]any{"end": "2026-01-01"}}})
	require.Equal(t, http.StatusCreated, response.StatusCode, body)
	require.Equal(t, `"1"`, response.Header.Get("ETag"))
	var record struct {
		ID string `json:"record_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &record))
	path := "/api/v1/records/" + record.ID
	response, body = do(t, ts, http.MethodPatch, path, map[string]string{"If-Match": `"1"`}, map[string]any{"fields": map[string]any{"summary": nil}})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	require.Equal(t, `"1"`, response.Header.Get("ETag"))
	response, body = do(t, ts, http.MethodPatch, path, map[string]string{"If-Match": `"2"`}, map[string]any{"title": "Changed"})
	require.Equal(t, http.StatusPreconditionFailed, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, path+"/retire", map[string]string{"If-Match": `"1"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPatch, path, map[string]string{"If-Match": `"2"`}, map[string]any{"title": "Changed"})
	require.Equal(t, http.StatusConflict, response.StatusCode, body)
}

func TestRecordGetRejectsInvalidDate(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodGet, "/api/v1/records/00000000-0000-4000-8000-000000000000?as_of=2026-02-30", nil, nil)
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
}

func TestRecordValidationDoesNotExposeContent(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ts, _ := newTestServer(t, func(d *api.Deps) { d.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	for _, body := range []string{
		`{"type_id":"contract","title":"synthetic-secret",`,
		`{"type_id":"contract","title":"Example","note":{"synthetic-secret":1}}`,
		`{"type_id":"contract","title":{"synthetic-secret":1}}`,
		`{"type_id":"contract","title":"Example","fields":"synthetic-secret"}`,
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/records", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.GreaterOrEqual(t, resp.StatusCode, 400)
		require.Less(t, resp.StatusCode, 500)
		require.NotContains(t, string(data), "synthetic-secret")
	}
	require.NotContains(t, logs.String(), "synthetic-secret")
}
