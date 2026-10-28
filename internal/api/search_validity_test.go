package api_test

import (
	"encoding/json/v2"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/daemonconn"
	"maps"
	"net/http"
	"testing"
	"time"
)

func TestSearchValidityHTTP(t *testing.T) {
	t.Parallel()
	ts, catalog := newTestServer(t, configureProcessingTestService(t))
	ctx := t.Context()
	old := createFileWithContent(t, ts, catalog, "/old.txt", "synthetic ancient lease\n")
	next := createFileWithContent(t, ts, catalog, "/new.txt", "synthetic replacement lease\n")
	_, err := catalog.PutSupersession(ctx, next.ID, old.ID, old.Revision, "")
	require.NoError(t, err)
	c := daemonconn.New(ts.URL, testAPIKey)
	request := api.DocumentSearchRequest{Query: "old", Mode: "lexical", Profile: "private", AsOf: "2025-06-01", Superseded: "demote", Fence: api.DocumentSourceFence{VaultUID: catalog.VaultID(), ContentVersionIDs: []string{old.CurrentVersionID, next.CurrentVersionID}}}
	report, err := c.SearchDocuments(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "outdated_only", report.Validity.Status)
	require.Equal(t, "2025-06-01", report.Validity.AsOf)
	require.Len(t, report.Validity.Successors, 1)
	require.Equal(t, "/new.txt", report.Validity.Successors[0].Path)
	require.True(t, report.Validity.Successors[0].InFence)
	require.Equal(t, []int64{next.ID}, report.Results[0].Validity.SupersededBy)
	readerValidation, err := api.NewDocumentReader(catalog.Store, catalog.Blobs, "", nil).ValidateDocumentSearch(ctx, api.DocumentSearchValidationRequest{})
	require.Error(t, err)
	require.False(t, readerValidation.Valid)
	for _, asOf := range []string{"", "2024-01-01"} {
		clientValidation, err := c.ValidateDocumentSearch(ctx, api.DocumentSearchValidationRequest{Query: "synthetic", Mode: "lexical", Profile: "private", AsOf: asOf, Superseded: "keep_order"})
		require.NoError(t, err)
		require.NotEmpty(t, clientValidation.AsOf)
		input := map[string]any{"query": "synthetic", "mode": "lexical", "profile": "private", "superseded": "keep_order"}
		if asOf != "" {
			input["as_of"] = asOf
		}
		response, body := do(t, ts, http.MethodPost, "/api/v1/search/validate", nil, input)
		require.Equal(t, http.StatusOK, response.StatusCode, body)
		var validation api.DocumentSearchValidation
		require.NoError(t, json.Unmarshal([]byte(body), &validation))
		require.True(t, validation.Valid)
		if asOf == "" {
			asOf = time.Now().Format("2006-01-02")
		}
		require.Equal(t, asOf, validation.AsOf)
	}
	for _, invalid := range []map[string]any{{"as_of": "2025-02-30"}, {"superseded": "drop"}} {
		input := map[string]any{"query": "synthetic", "mode": "lexical", "profile": "private"}
		maps.Copy(input, invalid)
		response, _ := do(t, ts, http.MethodPost, "/api/v1/search/validate", nil, input)
		require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	}
}
