package daemonconn

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordBodyEditorPreservesFields(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", nil)
	require.NoError(t, err)
	err = recordBodyEditor(map[string]any{"title": "Example", "fields": map[string]any{"term": map[string]any{"end": "2027-01-01"}}})(context.Background(), req)
	require.NoError(t, err)
	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"title":"Example","fields":{"term":{"end":"2027-01-01"}}}`, string(data))
	require.Equal(t, int64(len(data)), req.ContentLength)
}
