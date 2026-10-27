package mcp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

// This client keeps the MCP mapping and batching real while replacing the daemon boundary.
type validityDocumentClient struct {
	documentClient

	resolution          api.DocumentSourceFenceResolution
	report              api.DocumentSearchReport
	validation          api.DocumentSearchValidation
	searchRequest       api.DocumentSearchRequest
	validationRequest   api.DocumentSearchValidationRequest
	batches             [][]api.DocumentIdentity
	duplicateRenditions bool
}

func (c *validityDocumentClient) ResolveDocumentSourceFence(context.Context, api.DocumentSourceFenceResolveRequest) (api.DocumentSourceFenceResolution, error) {
	return c.resolution, nil
}
func (c *validityDocumentClient) SearchDocuments(_ context.Context, r api.DocumentSearchRequest) (api.DocumentSearchReport, error) {
	c.searchRequest = r
	return c.report, nil
}
func (c *validityDocumentClient) ValidateDocumentSearch(_ context.Context, r api.DocumentSearchValidationRequest) (api.DocumentSearchValidation, error) {
	c.validationRequest = r
	return c.validation, nil
}
func (c *validityDocumentClient) ResolveDocumentSummaries(_ context.Context, r api.DocumentSummaryResolveRequest) ([]api.DocumentSummary, error) {
	c.batches = append(c.batches, slices.Clone(r.Identities))
	if len(r.Identities) > 100 {
		return nil, errors.New("summary batch exceeds 100")
	}
	out := make([]api.DocumentSummary, len(r.Identities))
	for i, id := range r.Identities {
		out[i] = api.DocumentSummary{NodeID: id.NodeID, ContentVersionID: id.ContentVersionID, Path: id.Path, Name: "synthetic.txt", MediaType: "text/plain", ModifiedAt: "2025-01-01T00:00:00Z", ActiveRenditions: []api.DocumentRenditionIdentity{{AttachmentID: testAttachmentID, BuildID: testBuildID, ProfileFingerprint: testProfileID}}}
		if c.duplicateRenditions {
			out[i].ActiveRenditions = append(out[i].ActiveRenditions, out[i].ActiveRenditions[0])
		}
	}
	return out, nil
}
func validityClient() *validityDocumentClient {
	return &validityDocumentClient{resolution: api.DocumentSourceFenceResolution{Fence: api.ResolvedDocumentSourceFence{VaultUID: testVaultID, ContentVersionIDs: []string{testVersionID}}, FenceFingerprint: testFenceFingerprint, ObservedScopeCount: 1}, report: api.DocumentSearchReport{RequestedMode: "lexical", ActualMode: "lexical", Coverage: api.DocumentSearchCoverage{State: "complete"}, Degradations: []string{}, Results: []api.DocumentSearchResult{}, Validity: api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "empty", Successors: []api.DocumentValiditySuccessor{}}}, validation: api.DocumentSearchValidation{Valid: true, AsOf: "2040-12-31"}}
}
func runValiditySearch(tb testing.TB, c *validityDocumentClient, args map[string]any) (searchDocumentsOutput, error) {
	tb.Helper()
	raw, err := json.Marshal(args)
	require.NoError(tb, err)
	out, _, err := searchDocuments(tb.Context(), func(ctx context.Context, call func(context.Context, documentClient) error) error { return call(ctx, c) }, raw)
	return out, err
}
func validityArgs() map[string]any {
	return map[string]any{"query": "synthetic", "profile": "local", "mode": "lexical", "filters": map[string]any{}}
}

func TestSearchValidityStatusesAndSchema(t *testing.T) {
	for _, state := range []string{"current", "outdated_only", "future_only", "empty"} {
		t.Run(state, func(t *testing.T) {
			c := validityClient()
			c.report.Validity.Status = state
			if state != "empty" {
				c.report.Results = []api.DocumentSearchResult{{NodeID: 7, ContentVersionID: testVersionID, Path: "/synthetic.txt", Rank: 1, Evidence: []api.DocumentEvidenceReference{}}}
			}
			if state == "outdated_only" {
				c.report.Results[0].Validity = &api.DocumentResultValidity{State: "expired", EffectiveTo: "2024-12-31", SupersededBy: []int64{}}
				c.report.Validity.DeclaredResults = 1
			}
			if state == "future_only" {
				c.report.Results[0].Validity = &api.DocumentResultValidity{State: "not_yet_effective", EffectiveFrom: "2026-01-01", SupersededBy: []int64{}}
				c.report.Validity.DeclaredResults = 1
			}
			out, err := runValiditySearch(t, c, validityArgs())
			require.NoError(t, err)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			var mapped map[string]any
			require.NoError(t, json.Unmarshal(raw, &mapped))
			_, outputSchema := searchDocumentsSchemas()
			assertSchemaAccepts(t, outputSchema, mapped)
			validity := objectField(t, mapped, "validity")
			assert.Equal(t, state, validity["status"])
			assert.Equal(t, "2025-01-01", validity["as_of"])
			if state != "empty" {
				hits, ok := mapped["results"].([]any)
				require.True(t, ok)
				require.Len(t, hits, 1)
				hit, ok := hits[0].(map[string]any)
				require.True(t, ok)
				if state == "current" {
					assert.NotContains(t, hit, "validity")
				}
				if state == "future_only" {
					assert.Equal(t, "not_yet_effective", objectField(t, hit, "validity")["state"])
				}
			}
			delete(mapped, "validity")
			assertSchemaRejects(t, outputSchema, mapped)
		})
	}
}
func TestSearchValidityInputsAndEmptyFenceDate(t *testing.T) {
	inputSchema, _ := searchDocumentsSchemas()
	for _, policy := range []string{"demote", "keep_order"} {
		t.Run(policy, func(t *testing.T) {
			args := validityArgs()
			args["as_of"] = "2024-06-01"
			args["superseded"] = policy
			assertSchemaAccepts(t, inputSchema, args)
			c := validityClient()
			_, err := runValiditySearch(t, c, args)
			require.NoError(t, err)
			assert.Equal(t, "2024-06-01", c.searchRequest.AsOf)
			assert.Equal(t, policy, c.searchRequest.Superseded)
			c.resolution.Fence.ContentVersionIDs = []string{}
			c.validation.AsOf = "2024-06-01"
			out, err := runValiditySearch(t, c, args)
			require.NoError(t, err)
			assert.Equal(t, "2024-06-01", c.validationRequest.AsOf)
			assert.Equal(t, policy, c.validationRequest.Superseded)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			var mapped map[string]any
			require.NoError(t, json.Unmarshal(raw, &mapped))
			assert.Equal(t, "2024-06-01", objectField(t, mapped, "validity")["as_of"])
		})
	}
	c := validityClient()
	_, err := runValiditySearch(t, c, validityArgs())
	require.NoError(t, err)
	assert.Empty(t, c.searchRequest.Superseded)
	for _, value := range []string{"yesterday", "2025-1-01", "2025-01-01T00:00:00Z"} {
		args := validityArgs()
		args["as_of"] = value
		assertSchemaRejects(t, inputSchema, args)
	}
	args := validityArgs()
	args["superseded"] = "ignore"
	assertSchemaRejects(t, inputSchema, args)
}
func TestSearchValidityRejectsInvalidCalendarDates(t *testing.T) {
	for _, date := range []string{"", "2025-02-30", "2024-02-30", "2025-1-01", "2025-01-01T00:00:00Z"} {
		t.Run(date, func(t *testing.T) {
			args := validityArgs()
			args["as_of"] = date
			wireErr := decodeWireError(t, callToolWire(t, "search_documents", args))
			assert.Equal(t, int64(jsonrpc.CodeInvalidParams), wireErr.Code)
		})
	}
	c := validityClient()
	c.resolution.Fence.ContentVersionIDs = []string{}
	out, err := runValiditySearch(t, c, validityArgs())
	require.NoError(t, err)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	var mapped map[string]any
	require.NoError(t, json.Unmarshal(encoded, &mapped))
	v := objectField(t, mapped, "validity")
	assert.Equal(t, "2040-12-31", v["as_of"])
	assert.Equal(t, "empty", v["status"])
	_, schema := searchDocumentsSchemas()
	assertSchemaAccepts(t, schema, mapped)
}

func TestSearchValiditySuccessorLinksAndFence(t *testing.T) {
	const next = "33333333-3333-4333-8333-333333333333"
	for _, inFence := range []bool{true, false} {
		t.Run(strconv.FormatBool(inFence), func(t *testing.T) {
			c := validityClient()
			c.duplicateRenditions = true
			c.resolution.Fence.ContentVersionIDs = append(c.resolution.Fence.ContentVersionIDs, next)
			c.report.Results = []api.DocumentSearchResult{{NodeID: 7, ContentVersionID: testVersionID, Path: "/old.txt", Rank: 1, Evidence: []api.DocumentEvidenceReference{}, Validity: &api.DocumentResultValidity{State: "superseded", Undated: true, SupersededBy: []int64{8}}}}
			c.report.Validity = api.DocumentSearchValidity{AsOf: "2025-01-01", Status: "outdated_only", DeclaredResults: 1, Successors: []api.DocumentValiditySuccessor{{NodeID: 8, ContentVersionID: next, Path: "/new.txt", InFence: inFence, Undated: true}}}
			raw, err := json.Marshal(validityArgs())
			require.NoError(t, err)
			out, links, err := searchDocuments(t.Context(), func(ctx context.Context, call func(context.Context, documentClient) error) error { return call(ctx, c) }, raw)
			if !inFence {
				require.Error(t, err)
				assert.Empty(t, c.batches)
				return
			}
			require.NoError(t, err)
			assert.Len(t, links, 2)
			assert.Equal(t, "docbank://vaults/"+testVaultID+"/documents/8/versions/"+next+"/renditions/"+testAttachmentID, links[len(links)-1].URI)
			assert.Len(t, c.batches, 1)
			assert.Len(t, c.batches[0], 2)
			encoded, err := json.Marshal(out)
			require.NoError(t, err)
			var mapped map[string]any
			require.NoError(t, json.Unmarshal(encoded, &mapped))
			_, schema := searchDocumentsSchemas()
			assertSchemaAccepts(t, schema, mapped)
			c.batches = nil
			c.report.Results = append(c.report.Results, api.DocumentSearchResult{NodeID: 8, ContentVersionID: next, Path: "/new.txt", Rank: 2, Evidence: []api.DocumentEvidenceReference{}})
			_, links, err = searchDocuments(t.Context(), func(ctx context.Context, call func(context.Context, documentClient) error) error { return call(ctx, c) }, raw)
			require.NoError(t, err)
			assert.Len(t, links, 2)
			require.Len(t, c.batches, 1)
			assert.Len(t, c.batches[0], 2)
			// A terminal successor outside the version fence must fail before summary reads.
			c.batches = nil
			c.resolution.Fence.ContentVersionIDs = []string{testVersionID}
			c.report.Results = c.report.Results[:1]
			_, err = runValiditySearch(t, c, validityArgs())
			require.Error(t, err)
			assert.Empty(t, c.batches)
		})
	}
}

func FuzzSearchValiditySummaryBatches(f *testing.F) {
	f.Add(uint8(100), uint8(16), false)
	f.Add(uint8(100), uint8(16), true)
	f.Add(uint8(0), uint8(0), false)
	f.Fuzz(func(t *testing.T, hits, successors uint8, duplicate bool) {
		c := validityClient()
		c.resolution.Fence.ContentVersionIDs = nil
		hitCount, nextCount := int(hits)%101, int(successors)%17
		if hitCount == 0 {
			nextCount = 0
		}
		want := []int64{}
		for i := range hitCount {
			id := int64(i + 1)
			v := fmt.Sprintf("22222222-2222-4222-8222-%012d", id)
			c.resolution.Fence.ContentVersionIDs = append(c.resolution.Fence.ContentVersionIDs, v)
			c.report.Results = append(c.report.Results, api.DocumentSearchResult{NodeID: id, ContentVersionID: v, Path: fmt.Sprintf("/%d.txt", id)})
			want = append(want, id)
		}
		for i := range nextCount {
			id := int64(hitCount + i + 1)
			if duplicate && hitCount > 0 {
				id = int64(i%hitCount + 1)
			}
			v := fmt.Sprintf("22222222-2222-4222-8222-%012d", id)
			c.resolution.Fence.ContentVersionIDs = append(c.resolution.Fence.ContentVersionIDs, v)
			c.report.Validity.Successors = append(c.report.Validity.Successors, api.DocumentValiditySuccessor{NodeID: id, ContentVersionID: v, Path: fmt.Sprintf("/%d.txt", id), InFence: true})
			if !duplicate || hitCount == 0 {
				want = append(want, id)
			}
		}
		_, err := runValiditySearch(t, c, validityArgs())
		require.NoError(t, err)
		got := []int64{}
		require.LessOrEqual(t, len(c.batches), 2)
		for _, batch := range c.batches {
			require.NotEmpty(t, batch)
			require.LessOrEqual(t, len(batch), 100)
			for _, id := range batch {
				got = append(got, id.NodeID)
			}
		}
		assert.Equal(t, want, got)
	})
}
