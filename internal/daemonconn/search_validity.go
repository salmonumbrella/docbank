package daemonconn

import (
	"errors"
	"fmt"
	"slices"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/api"
)

func validateSearchValidity(request api.DocumentSearchRequest, report api.DocumentSearchReport) error {
	v := report.Validity
	if v.AsOf == "" || document.ValidateDate(v.AsOf) != nil || (request.AsOf != "" && request.AsOf != v.AsOf) {
		return errors.New("validity evaluation date is invalid")
	}
	if request.Superseded != "" && request.Superseded != "demote" && request.Superseded != "keep_order" {
		return errors.New("validity ordering policy is invalid")
	}
	declared, current, allFuture := 0, false, len(report.Results) > 0
	outOfForce := false
	for _, r := range report.Results {
		state := document.ValidityCurrent
		if own := r.Validity; own != nil {
			declared++
			state = document.ValidityState(own.State)
			if document.ValidateEffectiveWindow(document.EffectiveWindow{From: own.EffectiveFrom, To: own.EffectiveTo}) != nil || len(own.SupersededBy) > 16 {
				return errors.New("result validity window or degree is invalid")
			}
			seen := map[int64]bool{}
			for _, id := range own.SupersededBy {
				if id <= 0 || id == r.NodeID || seen[id] {
					return errors.New("result validity successor identity is invalid")
				}
				seen[id] = true
			}
			future := own.EffectiveFrom != "" && own.EffectiveFrom > v.AsOf
			expired := own.EffectiveTo != "" && own.EffectiveTo < v.AsOf
			switch state {
			case document.ValidityCurrent:
				if future || expired || len(seen) > 0 {
					return errors.New("current result validity is inconsistent")
				}
			case document.ValidityNotYetEffective:
				if !future {
					return errors.New("future result validity is inconsistent")
				}
			case document.ValiditySuperseded:
				if future || len(seen) == 0 {
					return errors.New("superseded result validity is inconsistent")
				}
			case document.ValidityExpired:
				if future || !expired || len(seen) > 0 {
					return errors.New("expired result validity is inconsistent")
				}
			default:
				return errors.New("result validity state is invalid")
			}
			if own.Undated && state != document.ValiditySuperseded {
				return errors.New("result undated validity is inconsistent")
			}
		}
		if state == document.ValidityCurrent {
			current = true
			if outOfForce && request.Superseded != "keep_order" {
				return errors.New("validity result order does not honor demotion")
			}
		} else {
			outOfForce = true
		}
		allFuture = allFuture && state == document.ValidityNotYetEffective
	}
	status := "outdated_only"
	if len(report.Results) == 0 {
		status = "empty"
	} else if current {
		status = string(document.ValidityCurrent)
	} else if allFuture {
		status = "future_only"
	}
	if v.Status != status || v.DeclaredResults != declared {
		return errors.New("validity status or declared result count is inconsistent")
	}
	if len(v.Successors) > 0 && !outOfForce {
		return errors.New("validity successors have no out-of-force source result")
	}
	if len(v.Successors) > 16 {
		return errors.New("validity successor report exceeds its bound")
	}
	seenNodes := map[int64]bool{}
	seenVersions := map[string]bool{}
	for _, next := range v.Successors {
		if next.NodeID <= 0 || !validUUIDv4(next.ContentVersionID) || !slices.Contains(request.Fence.ContentVersionIDs, next.ContentVersionID) || !next.InFence {
			return errors.New("validity successor escaped the source fence")
		}
		if seenNodes[next.NodeID] || seenVersions[next.ContentVersionID] || !validDocumentSearchPath(next.Path) || len(next.Path) > 16384 {
			return errors.New("validity successor path or identity is invalid")
		}
		if document.ValidateDate(next.EffectiveFrom) != nil || next.EffectiveFrom > v.AsOf || next.Undated != (next.EffectiveFrom == "") {
			return errors.New("validity successor effective date is invalid")
		}
		for _, r := range report.Results {
			if (r.NodeID == next.NodeID || r.ContentVersionID == next.ContentVersionID) && (r.NodeID != next.NodeID || r.ContentVersionID != next.ContentVersionID || (r.Validity != nil && r.Validity.State != string(document.ValidityCurrent))) {
				return fmt.Errorf("validity successor %d conflicts with result identity", next.NodeID)
			}
		}
		seenNodes[next.NodeID] = true
		seenVersions[next.ContentVersionID] = true
	}
	return nil
}
