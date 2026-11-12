package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordTypeActivationRequiresRevisionZero(t *testing.T) {
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
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodPost, "/api/v1/record-types/contract/activate", map[string]string{"If-Match": `"0"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPost, "/api/v1/records", nil, map[string]any{"type_id": "contract", "title": "Example", "status": nil})
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
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
	response, body = do(t, ts, http.MethodPatch, path, map[string]string{"If-Match": `"1"`}, map[string]any{"title": "Synthetic agreement"})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	require.Equal(t, `"1"`, response.Header.Get("ETag"))
	response, body = do(t, ts, http.MethodPost, path+"/retire", map[string]string{"If-Match": `"1"`}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	response, body = do(t, ts, http.MethodPatch, path, map[string]string{"If-Match": `"2"`}, map[string]any{"title": "Changed"})
	require.Equal(t, http.StatusConflict, response.StatusCode, body)
}

func TestRecordGetRejectsYearZero(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	response, body := do(t, ts, http.MethodGet, "/api/v1/records/00000000-0000-4000-8000-000000000000?as_of=0000-01-01", nil, nil)
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
}
