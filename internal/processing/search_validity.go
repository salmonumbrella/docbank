package processing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
)

func normalizeSearchValidity(request SearchRequest) (string, error) {
	if request.Superseded != "" && request.Superseded != "demote" && request.Superseded != "keep_order" {
		return "", fmt.Errorf("superseded must be demote or keep_order: %w", store.ErrInvalidSupersession)
	}
	asOf := request.AsOf
	if asOf == "" {
		asOf = time.Now().Format("2006-01-02")
	}
	if err := document.ValidateDate(asOf); err != nil {
		return "", fmt.Errorf("%w: %w", store.ErrInvalidSupersession, err)
	}
	return asOf, nil
}

// NormalizeSearchAsOf shares the daemon's calendar normalization with validation.
func NormalizeSearchAsOf(request SearchRequest) (string, error) {
	return normalizeSearchValidity(request)
}

func partitionValidity(results []retrieval.Result) []retrieval.Result {
	out := make([]retrieval.Result, 0, len(results))
	for _, current := range []bool{true, false} {
		for _, r := range results {
			if (r.Validity == nil || r.Validity.State == document.ValidityCurrent) == current {
				out = append(out, r)
			}
		}
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func (service *Service) applySearchValidity(ctx context.Context, report retrieval.Report, ids []string, asOf, policy string) (retrieval.Report, error) {
	graph, err := service.catalog.LoadSearchValidity(ctx, ids)
	if err != nil {
		return retrieval.Report{}, err
	}
	validity := retrieval.SearchValidity{AsOf: asOf, Status: "empty", Successors: []retrieval.ValiditySuccessor{}}
	states := map[int64]document.ValidityState{}
	for id, n := range graph {
		starts := []document.SuccessorStart{}
		for _, next := range n.Successors {
			starts = append(starts, document.SuccessorStart{From: graph[next].Window.From})
		}
		state, _ := document.EvaluateValidity(asOf, n.Window, starts)
		states[id] = state
	}
	allFuture, hasCurrent := len(report.Results) > 0, false
	queue := []int64{}
	for i := range report.Results {
		r := &report.Results[i]
		n, ok := graph[r.Document.NodeID]
		if !ok || n.ContentVersionID != r.Document.ContentVersionID {
			return retrieval.Report{}, fmt.Errorf("search validity source changed: %w", store.ErrProcessingSourceFenceStaleVersion)
		}
		state := states[n.NodeID]
		hasCurrent = hasCurrent || state == document.ValidityCurrent
		allFuture = allFuture && state == document.ValidityNotYetEffective
		if n.Declared {
			validity.DeclaredResults++
			v := &retrieval.ResultValidity{State: state, EffectiveFrom: n.Window.From, EffectiveTo: n.Window.To, SupersededBy: []int64{}}
			for _, next := range n.Successors {
				if graph[next].Window.From == "" || graph[next].Window.From <= asOf {
					v.SupersededBy = append(v.SupersededBy, next)
					if graph[next].Window.From == "" && state == document.ValiditySuperseded {
						v.Undated = true
					}
				}
			}
			r.Validity = v
		}
		if state != document.ValidityCurrent {
			queue = append(queue, n.Successors...)
		}
	}
	if hasCurrent {
		validity.Status = "current"
	} else if allFuture {
		validity.Status = "future_only"
	} else if len(report.Results) > 0 {
		validity.Status = "outdated_only"
	}
	seen := map[int64]bool{}
	found := []int64{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		n := graph[id]
		if states[id] == document.ValidityCurrent {
			found = append(found, id)
		} else {
			queue = append(queue, n.Successors...)
		}
	}
	slices.Sort(found)
	for _, id := range found[:min(16, len(found))] {
		n := graph[id]
		validity.Successors = append(validity.Successors, retrieval.ValiditySuccessor{NodeID: id, ContentVersionID: n.ContentVersionID, Path: n.Path, EffectiveFrom: n.Window.From, InFence: true, Undated: n.Window.From == ""})
	}
	if policy != "keep_order" {
		report.Results = partitionValidity(report.Results)
	}
	report.Validity = validity
	return report, nil
}
