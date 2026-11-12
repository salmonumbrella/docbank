package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordRequestAndPanicLogsRedactContent(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/record-types/synthetic%2Dsecret", "/api/v1/records/synthetic%2Dsecret"} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		handler := logMiddleware(recoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("sensitive panic") }), logger), logger)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.NotContains(t, logs.String(), "synthetic")
		require.NotContains(t, logs.String(), "sensitive panic")
		require.Contains(t, logs.String(), "panic in handler")
	}
}
