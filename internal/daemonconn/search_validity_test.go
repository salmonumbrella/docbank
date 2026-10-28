package daemonconn_test

import (
	"encoding/json/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/daemonconn"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchValidityClientRejectsMalformedAuthority(t *testing.T) {
	t.Parallel()
	const vault = "11111111-1111-4111-8111-111111111111"
	const version = "22222222-2222-4222-8222-222222222222"
	const next = "33333333-3333-4333-8333-333333333333"
	request := api.DocumentSearchRequest{Query: "old", Mode: "lexical", Profile: "private", AsOf: "2025-01-01", Fence: api.DocumentSourceFence{VaultUID: vault, ContentVersionIDs: []string{version, next}}}
	base := func() api.DocumentSearchReport {
		return api.DocumentSearchReport{RequestedMode: "lexical", ActualMode: "lexical", Coverage: api.DocumentSearchCoverage{ScopedDocuments: 2, CompleteDocuments: 2, State: "complete"}, Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 1, Successors: []api.DocumentValiditySuccessor{{NodeID: 2, ContentVersionID: next, Path: "/new", InFence: true, Undated: true}}}, Results: []api.DocumentSearchResult{{VaultUID: vault, NodeID: 1, ContentVersionID: version, Rank: 1, LexicalRank: 1, Score: .5, Path: "/old", Evidence: []api.DocumentEvidenceReference{{Kind: "node_name"}}, Validity: &api.DocumentResultValidity{State: "superseded", Undated: true, SupersededBy: []int64{2}}}}}
	}
	tests := []struct {
		name   string
		mutate func(*api.DocumentSearchReport)
	}{
		{"missing", func(r *api.DocumentSearchReport) { r.Validity = api.DocumentSearchValidity{} }},
		{"date", func(r *api.DocumentSearchReport) { r.Validity.AsOf = "2025-02-30" }},
		{"different date", func(r *api.DocumentSearchReport) { r.Validity.AsOf = "2024-01-01" }},
		{"status", func(r *api.DocumentSearchReport) { r.Validity.Status = "current" }},
		{"declared count", func(r *api.DocumentSearchReport) { r.Validity.DeclaredResults = 0 }},
		{"outside successor", func(r *api.DocumentSearchReport) {
			r.Validity.Successors[0].ContentVersionID = "44444444-4444-4444-8444-444444444444"
		}},
		{"false fence", func(r *api.DocumentSearchReport) { r.Validity.Successors[0].InFence = false }},
		{"duplicate successor", func(r *api.DocumentSearchReport) {
			r.Validity.Successors = append(r.Validity.Successors, r.Validity.Successors[0])
		}},
		{"successor path", func(r *api.DocumentSearchReport) { r.Validity.Successors[0].Path = "relative" }},
		{"successor date", func(r *api.DocumentSearchReport) { r.Validity.Successors[0].EffectiveFrom = "2026-01-01" }},
		{"state", func(r *api.DocumentSearchReport) { r.Results[0].Validity.State = "mystery" }},
		{"duplicate direct", func(r *api.DocumentSearchReport) { r.Results[0].Validity.SupersededBy = []int64{2, 2} }},
		{"window", func(r *api.DocumentSearchReport) { r.Results[0].Validity.EffectiveFrom = "2026-01-01" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mutate(&r)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.MarshalWrite(w, r))
			}))
			defer server.Close()
			_, err := daemonconn.New(server.URL, "key").SearchDocuments(t.Context(), request)
			require.Error(t, err)
		})
	}

	valid := base()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.MarshalWrite(w, valid))
	}))
	defer server.Close()
	client := daemonconn.New(server.URL, "key")
	_, err := client.SearchDocuments(t.Context(), request)
	require.NoError(t, err)
	valid.Results = append(valid.Results, api.DocumentSearchResult{VaultUID: vault, NodeID: 2, ContentVersionID: next, Rank: 2, LexicalRank: 2, Score: .25, Path: "/new", Evidence: []api.DocumentEvidenceReference{{Kind: "node_name"}}})
	valid.Validity.Status = "current"
	_, err = client.SearchDocuments(t.Context(), request)
	require.ErrorContains(t, err, "demotion")
	request.Superseded = "keep_order"
	_, err = client.SearchDocuments(t.Context(), request)
	require.NoError(t, err)
}
