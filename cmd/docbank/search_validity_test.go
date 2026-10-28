package main

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestSearchValidityCLIRejectsFallback(t *testing.T) {
	t.Setenv("DOCBANK_HOME", t.TempDir())
	for _, flags := range [][]string{{"--as-of", "2025-01-01"}, {"--mode", "lexical", "--keep-superseded-order"}} {
		_, err := runCLI(t, append([]string{"search", "lease"}, flags...)...)
		require.ErrorContains(t, err, "processing")
	}
	_, err := runCLI(t, "search", "lease", "--as-of", "2025-01-01", "--fuzzy")
	require.ErrorContains(t, err, "cannot be combined")
	options := documentSearchCLIOptions{Mode: "lexical", Profile: "private", Limit: 10, ContentVersionIDs: []string{processingTestVersionID}, AsOf: "2025-02-30"}
	require.ErrorContains(t, validateDocumentSearchOptions("lease", options), "date")
}

func TestSearchValidityCLIText(t *testing.T) {
	cmd, out := processingTestCommand()
	report := api.DocumentSearchReport{Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 1, Successors: []api.DocumentValiditySuccessor{{NodeID: 2, ContentVersionID: processingTestVersionID, Path: "/new.txt", InFence: true}}}, Results: []api.DocumentSearchResult{{NodeID: 1, Rank: 1, Path: "/old.txt", Validity: &api.DocumentResultValidity{State: "superseded", SupersededBy: []int64{2}}}, {NodeID: 3, Rank: 2, Path: "/expired.txt", Validity: &api.DocumentResultValidity{State: "expired"}}}}
	require.NoError(t, writeDocumentSearchReport(cmd, report, false, nil, false))
	require.Contains(t, out.String(), "(superseded by /new.txt)")
	require.Contains(t, out.String(), "(expired)")
	require.Contains(t, out.String(), "Only outdated documents matched; in force: /new.txt")
	cmd, out = processingTestCommand()
	report.Validity.Successors = []api.DocumentValiditySuccessor{}
	require.NoError(t, writeDocumentSearchReport(cmd, report, false, nil, false))
	require.Contains(t, out.String(), "no in-force successor inside the source fence")
}

func TestSearchValidityCLIAgent(t *testing.T) {
	cmd, out := processingTestCommand()
	report := api.DocumentSearchReport{Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 1}, Results: []api.DocumentSearchResult{{NodeID: 1, Rank: 1, Path: "/old", Validity: &api.DocumentResultValidity{State: "expired", EffectiveTo: "2024-12-31"}}}}
	require.NoError(t, writeDocumentSearchAgent(cmd, "lease", report, nil))
	require.Contains(t, out.String(), "validity=outdated_only")
	require.Contains(t, out.String(), "as_of=2025-01-01")
	require.Contains(t, out.String(), "validity=expired")
}

func TestAutomaticSearchValidityTrimming(t *testing.T) {
	originalLimit, originalJSON := searchLimit, searchJSON
	t.Cleanup(func() { searchLimit, searchJSON = originalLimit, originalJSON })
	searchJSON = true
	for _, tc := range []struct {
		name         string
		limit        int
		wantStatus   string
		wantDeclared int
	}{{"all files omitted", 1, "empty", 0}, {"outdated hit omitted", 2, "outdated_only", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			searchLimit = tc.limit
			cmd, out := processingTestCommand()
			report := api.DocumentSearchReport{Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 2, Successors: []api.DocumentValiditySuccessor{{NodeID: 3, Path: "/new", ContentVersionID: processingTestVersionID, InFence: true, Undated: true}}}, Results: []api.DocumentSearchResult{{NodeID: 1, Rank: 1, Path: "/old-one", Validity: &api.DocumentResultValidity{State: "expired", EffectiveTo: "2024-12-31"}}, {NodeID: 2, Rank: 2, Path: "/old-two", Validity: &api.DocumentResultValidity{State: "expired", EffectiveTo: "2024-12-31"}}}}
			if tc.limit == 1 {
				report.Results = report.Results[:1]
				report.Validity.DeclaredResults = 1
			}
			require.NoError(t, writeAutomaticSearch(cmd, "old", report, api.SearchReport{Hits: []api.SearchHit{{Node: api.Node{ID: 4, Kind: "dir"}, Path: "/folder"}}}))
			var got api.DocumentSearchReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Equal(t, tc.wantStatus, got.Validity.Status)
			require.Equal(t, tc.wantDeclared, got.Validity.DeclaredResults)
			require.Empty(t, got.Validity.Successors)
			require.Equal(t, "2025-01-01", got.Validity.AsOf)
		})
	}
}

func TestAutomaticSearchTrimmedSuccessorGuidance(t *testing.T) {
	oldLimit, oldJSON, oldAgent := searchLimit, searchJSON, searchAgent
	t.Cleanup(func() { searchLimit, searchJSON, searchAgent = oldLimit, oldJSON, oldAgent })
	searchLimit, searchJSON, searchAgent = 2, false, false
	cmd, out := processingTestCommand()
	report := api.DocumentSearchReport{
		Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 2, Successors: []api.DocumentValiditySuccessor{{NodeID: 3, Path: "/replacement", InFence: true}}},
		Results: []api.DocumentSearchResult{
			{NodeID: 1, Rank: 1, Path: "/old", Validity: &api.DocumentResultValidity{State: "superseded", SupersededBy: []int64{3}}},
			{NodeID: 2, Rank: 2, Path: "/other", Validity: &api.DocumentResultValidity{State: "expired"}},
		},
	}
	require.NoError(t, writeAutomaticSearch(cmd, "old", report, api.SearchReport{Hits: []api.SearchHit{{Node: api.Node{ID: 4, Kind: "dir"}, Path: "/folder"}}}))
	require.Contains(t, out.String(), "successor guidance omitted")
	require.NotContains(t, out.String(), "no in-force successor inside the source fence")
}

func TestAutomaticSearchTrimWithoutSuccessorGuidance(t *testing.T) {
	oldLimit, oldJSON, oldAgent := searchLimit, searchJSON, searchAgent
	t.Cleanup(func() { searchLimit, searchJSON, searchAgent = oldLimit, oldJSON, oldAgent })
	searchLimit, searchJSON, searchAgent = 2, false, false
	cmd, out := processingTestCommand()
	report := api.DocumentSearchReport{
		Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 2, Successors: []api.DocumentValiditySuccessor{}},
		Results: []api.DocumentSearchResult{
			{NodeID: 1, Rank: 1, Path: "/old", Validity: &api.DocumentResultValidity{State: "expired"}},
			{NodeID: 2, Rank: 2, Path: "/other", Validity: &api.DocumentResultValidity{State: "expired"}},
		},
	}
	require.NoError(t, writeAutomaticSearch(cmd, "old", report, api.SearchReport{Hits: []api.SearchHit{{Node: api.Node{ID: 4, Kind: "dir"}, Path: "/folder"}}}))
	require.Contains(t, out.String(), "no in-force successor inside the source fence")
	require.NotContains(t, out.String(), "successor guidance omitted")
}
