package daemonconn

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaxDocumentReceiptValidation(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	d := api.TaxDocumentLink{LinkID: id, RecordID: id, NodeID: new(int64(2)), Role: "return", RecordedAt: "2026-01-01T00:00:00Z", Live: true, Path: new("/return.txt")}
	assert.True(t, validTaxDocument(d, id))
	d.Intact = true
	assert.False(t, validTaxDocument(d, id))
}

func TestTaxDocumentClientContracts(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const linkID = "33333333-3333-4333-8333-333333333333"
	parent := strings.Replace(strings.Replace(eventRecordFixture, `"revision":1`, `"revision":2`, 1), `"updated_at":"2025-01-01T00:00:00Z"`, `"updated_at":"2026-01-01T00:00:00Z"`, 1)
	doc := `{"link_id":"` + linkID + `","record_id":"` + id + `","event_id":null,"node_id":2,"content_version_id":null,"content_sha256":"","role":"return","form_code":"","note":"","recorded_at":"2026-01-01T00:00:00Z","retired_at":null,"live":true,"pinned":false,"intact":false,"path":"/return.txt"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/tax/records/"+id+"/documents", r.URL.Path)
		assert.Equal(t, `"1"`, r.Header.Get("If-Match"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"2"`)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"record":` + parent + `,"document":` + doc + `}`))
	}))
	t.Cleanup(server.Close)
	got, err := New(server.URL, "").LinkTaxDocument(t.Context(), id, 1, api.LinkTaxDocumentRequest{NodeID: 2, Role: "return"})
	require.NoError(t, err)
	assert.Equal(t, linkID, got.Document.LinkID)
}
