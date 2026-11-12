package api_test

import (
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
	response, body = do(t, ts, http.MethodPatch, "/api/v1/records/00000000-0000-4000-8000-000000000000", map[string]string{"If-Match": `"1"`}, map[string]any{"$schema": "https://example.com/schema"})
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
}
