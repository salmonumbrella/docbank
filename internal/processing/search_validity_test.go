package processing

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
)

func TestSearchValidityReportsStaleVersion(t *testing.T) {
	t.Parallel()
	f := newMediaStateFixture(t)
	ctx := t.Context()
	n, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "lease", processingHash("old-stale"), 1, "text/plain")
	require.NoError(t, err)
	report := retrieval.Report{Results: []retrieval.Result{{Document: retrieval.DocumentIdentity{NodeID: n.ID, ContentVersionID: n.CurrentVersionID}}}}
	_, _, err = f.catalog.ReplaceContent(ctx, n.ID, n.Revision, processingHash("new-stale"), 1, "text/plain")
	require.NoError(t, err)
	_, err = f.service.applySearchValidity(ctx, report, []string{n.CurrentVersionID}, "2025-01-01", "")
	require.ErrorIs(t, err, store.ErrProcessingSourceFenceStaleVersion)
}

func FuzzPartitionValidity(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 4, 3, 2, 1, 0})
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0})
	f.Fuzz(func(t *testing.T, flags []byte) {
		if len(flags) > 100 {
			flags = flags[:100]
		}
		input := make([]retrieval.Result, len(flags))
		want := []int64{}
		for i, b := range flags {
			input[i] = retrieval.Result{Document: retrieval.DocumentIdentity{NodeID: int64(i + 1)}, Rank: i + 1, Score: float64(i+1) / 101}
			if b%5 != 0 {
				states := []document.ValidityState{document.ValidityCurrent, document.ValidityCurrent, document.ValidityExpired, document.ValiditySuperseded, document.ValidityNotYetEffective}
				input[i].Validity = &retrieval.ResultValidity{State: states[b%5]}
			}
		}
		for _, group := range []bool{true, false} {
			for i, r := range input {
				if (flags[i]%5 < 2) == group {
					want = append(want, r.Document.NodeID)
				}
			}
		}
		got := partitionValidity(input)
		require.Len(t, got, len(input))
		for i, r := range got {
			require.Equal(t, want[i], r.Document.NodeID)
			require.Equal(t, i+1, r.Rank)
			require.Equal(t, math.Float64bits(float64(r.Document.NodeID)/101), math.Float64bits(r.Score))
			require.Equal(t, i+1, input[i].Rank)
		}
	})
}

func TestProcessingSearchValidity(t *testing.T) {
	t.Parallel()
	f := newMediaStateFixture(t)
	ctx := t.Context()
	old, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "leaseold", processingHash("old"), 1, "text/plain")
	require.NoError(t, err)
	next, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "leasenew", processingHash("new"), 1, "text/plain")
	require.NoError(t, err)
	_, err = f.catalog.PutSupersession(ctx, next.ID, old.ID, old.Revision, "")
	require.NoError(t, err)
	_, err = f.catalog.PutEffectiveWindow(ctx, next.ID, next.Revision, "2025-01-01", "")
	require.NoError(t, err)
	request := SearchRequest{Query: "lease", Mode: "lexical", Profile: "speech", AsOf: "2025-06-01", Fence: SourceFence{VaultUID: f.catalog.VaultID(), ContentVersionIDs: []string{old.CurrentVersionID, next.CurrentVersionID}}}
	report, err := f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Len(t, report.Results, 2)
	require.Equal(t, next.ID, report.Results[0].Document.NodeID)
	require.Equal(t, "current", report.Validity.Status)
	require.Equal(t, 2, report.Validity.DeclaredResults)
	request.Query = "leaseold"
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "outdated_only", report.Validity.Status)
	require.Len(t, report.Validity.Successors, 1)
	require.Equal(t, next.ID, report.Validity.Successors[0].NodeID)
	request.AsOf = "2024-06-01"
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "current", report.Validity.Status)
	request.Query = "leasenew"
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "future_only", report.Validity.Status)
	request.Query = "leaseold"
	request.AsOf = "2025-06-01"
	request.Fence.ContentVersionIDs = []string{old.CurrentVersionID}
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Nil(t, report.Results[0].Validity)
	require.Empty(t, report.Validity.Successors)
	request.Query = "absentphrase"
	request.AsOf = ""
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "empty", report.Validity.Status)
	require.Equal(t, time.Now().Format("2006-01-02"), report.Validity.AsOf)
	for _, bad := range []SearchRequest{{Query: "lease", Profile: "speech", AsOf: "2025-02-30"}, {Query: "lease", Profile: "speech", Superseded: "drop"}} {
		require.Error(t, f.service.ValidateSearch(ctx, bad))
	}
}

func TestProcessingSearchValidityKeepsScoresAndRequestedOrder(t *testing.T) {
	t.Parallel()
	f := newMediaStateFixture(t)
	ctx := t.Context()
	a, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "lease-a", processingHash("a"), 1, "text/plain")
	require.NoError(t, err)
	b, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "lease-b", processingHash("b"), 1, "text/plain")
	require.NoError(t, err)
	request := SearchRequest{Query: "lease", Mode: "lexical", Profile: "speech", AsOf: "2025-01-01", Fence: SourceFence{VaultUID: f.catalog.VaultID(), ContentVersionIDs: []string{a.CurrentVersionID, b.CurrentVersionID}}}
	baseline, err := f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Len(t, baseline.Results, 2)
	_, err = f.catalog.PutSupersession(ctx, baseline.Results[1].Document.NodeID, baseline.Results[0].Document.NodeID, 1, "")
	require.NoError(t, err)
	request.Superseded = "keep_order"
	kept, err := f.service.Search(ctx, request)
	require.NoError(t, err)
	for i := range baseline.Results {
		require.Equal(t, baseline.Results[i].Document, kept.Results[i].Document)
		require.Equal(t, math.Float64bits(baseline.Results[i].Score), math.Float64bits(kept.Results[i].Score))
		require.Equal(t, i+1, kept.Results[i].Rank)
	}
	request.Superseded = "demote"
	demoted, err := f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, baseline.Results[1].Document, demoted.Results[0].Document)
	require.Equal(t, math.Float64bits(baseline.Results[1].Score), math.Float64bits(demoted.Results[0].Score))
}

func TestMediaSearchValidity(t *testing.T) {
	t.Parallel()
	f := newMediaStateFixture(t)
	ctx := t.Context()
	retained, err := f.service.SubmitSuppliedMedia(ctx, f.suppliedRequest("b81095fb-f282-49e2-97c7-c648289bf823", &MediaProcessingRequest{Profile: "speech"}))
	require.NoError(t, err)
	require.NoError(t, f.run(t, retained.JobID))
	old, err := f.catalog.NodeByID(ctx, f.version.NodeID)
	require.NoError(t, err)
	next, err := f.catalog.CreateFile(ctx, f.catalog.RootID(), "replacement", processingHash("replacement"), 1, "text/plain")
	require.NoError(t, err)
	_, err = f.catalog.PutSupersession(ctx, next.ID, old.ID, old.Revision, "")
	require.NoError(t, err)
	request := SearchRequest{Query: "Synthetic worker output", Mode: "lexical", Profile: "speech", AsOf: "2025-01-01", Fence: SourceFence{VaultUID: f.catalog.VaultID(), ContentVersionIDs: []string{f.version.ID, next.CurrentVersionID}}, MediaSources: []retrieval.MediaSourceSelector{{SourceID: retained.SourceID, SourceVersionID: retained.SourceVersionID, ContentVersionID: retained.ContentVersionID}}}
	report, err := f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.Equal(t, "outdated_only", report.Validity.Status)
	require.Len(t, report.Validity.Successors, 1)
	require.Equal(t, next.ID, report.Validity.Successors[0].NodeID)
	request.Fence.ContentVersionIDs = []string{f.version.ID}
	report, err = f.service.Search(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "current", report.Validity.Status)
	require.Empty(t, report.Validity.Successors)
	require.Nil(t, report.Results[0].Validity)
}
