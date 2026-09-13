package document

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// PrimaryRuleV1 identifies the scope-aware primary-date policy.
const PrimaryRuleV1 = "primary-rule/v1"

var primaryRuleOrders = map[DocumentKind][]DateKind{
	"email":          {"sent", "received", "created", "modified"},
	"message":        {"sent", "received", "authored", "created", "modified"},
	"calendar":       {"started", "ended", "authored", "created", "modified"},
	"image":          {"captured", "created", "authored", "modified"},
	"audio_video":    {"captured", "authored", "created", "modified", "produced"},
	"package_record": {}, // Resolved through the frozen described kind.
	"other":          {"authored", "created", "modified", "produced"},
}

var primaryReasons = map[DateKind]string{
	"sent":           "email_date_header",
	"received":       "received_date",
	"document_date":  "supplied_document_date",
	"authored":       "authored_date",
	"created":        "created_date",
	"modified":       "modified_date",
	"captured":       "captured_date",
	"started":        "started_date",
	"ended":          "ended_date",
	"produced":       "produced_date",
	"imported":       "imported_fallback",
	"vault_recorded": "docbank_recorded_fallback",
}

// SelectPrimaryEvent selects an explainable date from already validated events.
// Disclosure and package boundaries are applied before ranking candidates.
// It does not modify the supplied events.
func SelectPrimaryEvent(events []DocumentEventV1, kind, describedKind DocumentKind, scopeClass, disclosure string) (DocumentEventPrimaryV1, bool) {
	if !validPrimaryScope(scopeClass) || (disclosure != "safe" && disclosure != "full") || !ValidDocumentKind(kind) {
		return DocumentEventPrimaryV1{}, false
	}
	if kind == "package_record" {
		kind = describedKind
		if kind == "" {
			kind = "other"
		}
		if !ValidDocumentKind(kind) || kind == "package_record" {
			return DocumentEventPrimaryV1{}, false
		}
	}
	packageScope := strings.HasPrefix(scopeClass, "package:")
	order := make([]DateKind, 0, len(primaryRuleOrders[kind])+3)
	if packageScope {
		order = append(order, "document_date")
	}
	order = append(order, primaryRuleOrders[kind]...)
	if !packageScope {
		order = append(order, "document_date")
	}
	order = append(order, "imported", "vault_recorded")
	candidates := primaryScopeEvents(events, scopeClass, disclosure == "safe")
	for _, dateKind := range order {
		var best *DocumentEventV1
		for i := range candidates {
			event := &candidates[i]
			if event.DateKind != dateKind {
				continue
			}
			if best == nil || comparePrimaryEvents(*event, *best) < 0 {
				best = event
			}
		}
		if best != nil {
			reason := primaryReasons[dateKind]
			if packageScope && dateKind == "document_date" {
				reason = "package_supplied_document_date"
			}
			return DocumentEventPrimaryV1{EventID: best.EventID, Reason: reason, RuleID: PrimaryRuleV1, ScopeClass: scopeClass, Disclosure: disclosure}, true
		}
	}
	return DocumentEventPrimaryV1{}, false
}

func validPrimaryScope(scope string) bool {
	if scope == "vault" {
		return true
	}
	id, ok := strings.CutPrefix(scope, "package:")
	return ok && id != "" && !strings.ContainsAny(id, "/\\:") && !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func primaryScopeEvents(events []DocumentEventV1, scope string, safe bool) []DocumentEventV1 {
	result := make([]DocumentEventV1, 0, len(events))
	packageID, isPackage := strings.CutPrefix(scope, "package:")
	for _, e := range events {
		if safe && e.Sensitive {
			continue
		}
		if isPackage && (e.EvidenceKind == "package_row" || e.EvidenceKind == "output_receipt") &&
			!strings.HasPrefix(e.SourceKey, "loadfile/"+packageID+"/") && !strings.HasPrefix(e.SourceKey, "package/"+packageID+"/") {
			continue
		}
		result = append(result, e)
	}
	return result
}

func primaryReliability(e DocumentEventV1) int {
	if e.ClaimBasis == "docbank_observed" {
		return 2
	}
	if e.EvidenceKind == "package_row" || e.EvidenceKind == "output_receipt" {
		return 1
	}
	return 0
}

func comparePrimaryEvents(a, b DocumentEventV1) int {
	if c := cmp.Compare(primaryReliability(a), primaryReliability(b)); c != 0 {
		return c
	}
	if (a.ParseConfidence == "profile_interpreted") != (b.ParseConfidence == "profile_interpreted") {
		if a.ParseConfidence == "profile_interpreted" {
			return 1
		}
		return -1
	}
	if (a.UTCKey != "") != (b.UTCKey != "") {
		if a.UTCKey != "" {
			return -1
		}
		return 1
	}
	if c := cmp.Compare(slices.Index(eventPrecisions, b.Precision), slices.Index(eventPrecisions, a.Precision)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.SourceKey, b.SourceKey); c != 0 {
		return c
	}
	return cmp.Compare(a.EventID, b.EventID)
}
