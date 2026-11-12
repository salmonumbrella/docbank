package api_test

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestTaxDocumentHTTPWorkflow(t *testing.T) {
	ts, s := newTestServer(t, nil)
	_, account := createTaxTestAccount(t, ts, nil)
	r := createTaxTestRecord(t, ts, map[string]any{"account_id": account.AccountID, "kind": "filing", "title": "Example"})
	path := "/api/v1/tax/records/" + r.RecordID
	n, err := s.CreateFile(t.Context(), s.RootID(), "synthetic-return.txt", testHash("return-document"), 1, "text/plain")
	require.NoError(t, err)
	resp, body := do(t, ts, http.MethodPost, path+"/advance", map[string]string{"If-Match": `"1"`}, map[string]any{"state": "filed", "occurred_on": "2026-01-01"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	resp, body = do(t, ts, http.MethodPost, path+"/documents", map[string]string{"If-Match": `"2"`}, map[string]any{"node_id": n.ID, "role": "return", "content_version_id": n.CurrentVersionID})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	assert.Equal(t, `"3"`, resp.Header.Get("ETag"))
	var m api.TaxDocumentMutation
	require.NoError(t, json.Unmarshal([]byte(body), &m))
	assert.Equal(t, n.BlobHash, m.Document.ContentSHA256)
	assert.True(t, m.Document.Intact)
	resp, body = get(t, ts, path, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, body, `"state":"complete"`)
	resp, body = do(t, ts, http.MethodPost, path+"/documents/"+m.Document.LinkID+"/retire", map[string]string{"If-Match": `"3"`}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	resp, body = get(t, ts, path, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, body, `"state":"missing"`)
}

func TestTaxDocumentHTTPRejectionsAndMasking(t *testing.T) {
	ts, s := newTestServer(t, nil)
	_, account := createTaxTestAccount(t, ts, nil)
	r := createTaxTestRecord(t, ts, map[string]any{"account_id": account.AccountID, "kind": "filing", "title": "Example"})
	path := "/api/v1/tax/records/" + r.RecordID
	n, err := s.CreateFile(t.Context(), s.RootID(), "synthetic-evidence.txt", testHash("link-rejection"), 1, "text/plain")
	require.NoError(t, err)
	resp, body := do(t, ts, http.MethodPost, path+"/advance", map[string]string{"If-Match": `"1"`}, map[string]any{"state": "filed", "occurred_on": "2026-01-01", "reference": "private-synthetic-reference"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	for _, tc := range []struct {
		header map[string]string
		body   map[string]any
		status int
	}{
		{nil, map[string]any{"node_id": n.ID, "role": "return"}, http.StatusPreconditionRequired},
		{map[string]string{"If-Match": `"1"`}, map[string]any{"node_id": n.ID, "role": "return"}, http.StatusPreconditionFailed},
		{map[string]string{"If-Match": `"2"`}, map[string]any{"node_id": n.ID, "role": "return", "content_sha256": "private-synthetic-value"}, http.StatusUnprocessableEntity},
		{map[string]string{"If-Match": `"2"`}, map[string]any{"node_id": n.ID, "role": "unknown"}, http.StatusUnprocessableEntity},
	} {
		resp, body = do(t, ts, http.MethodPost, path+"/documents?reveal=true", tc.header, tc.body)
		assert.Equal(t, tc.status, resp.StatusCode, body)
		assert.NotContains(t, body, "private-synthetic")
	}
	resp, body = do(t, ts, http.MethodPost, path+"/documents?reveal=true", map[string]string{"If-Match": `"2"`}, map[string]any{"node_id": n.ID, "role": "return"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	assert.NotContains(t, body, "private-synthetic-reference")
	resp, body = do(t, ts, http.MethodPost, path+"/documents", map[string]string{"If-Match": `"3"`}, map[string]any{"node_id": n.ID, "role": "return"})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	resp, body = get(t, ts, path+"?reveal=true", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, body, "private-synthetic-reference")
}
