package taxrecord

import (
	"slices"
	"strings"
)

// EvidenceLink is the live authority relevant to an evidence requirement.
type EvidenceLink struct {
	EventID *string
	Role    string
	Live    bool
}
type EvidenceRequirement struct {
	Requires string
	EventID  *string
}
type Evidence struct {
	State string
	Unmet []EvidenceRequirement
}

var EvidenceStates = []string{"complete", "partial", "missing", "not_applicable"}

func ValidEvidenceState(s string) bool { return slices.Contains(EvidenceStates, s) }

// EvaluateEvidence derives requirements from surviving claims in append order.
func EvaluateEvidence(events []EventFact, links []EvidenceLink) Evidence {
	requirements := []EvidenceRequirement{}
	var hasReturn, hasAssessment bool
	for _, e := range events {
		if e.Voided {
			continue
		}
		switch e.Kind {
		case "filed", "amended":
			hasReturn = true
		case "assessed", "decision_issued":
			hasAssessment = true
		}
	}
	if hasReturn {
		requirements = append(requirements, EvidenceRequirement{Requires: "return"})
	}
	if hasAssessment {
		requirements = append(requirements, EvidenceRequirement{Requires: "assessment"})
	}
	for _, e := range events {
		if !e.Voided && (e.Kind == "payment" || e.Kind == "refund") {
			id := e.EventID
			requirements = append(requirements, EvidenceRequirement{Requires: "settlement", EventID: &id})
		}
	}
	out := Evidence{State: "not_applicable", Unmet: []EvidenceRequirement{}}
	for _, r := range requirements {
		met := false
		for _, l := range links {
			if l.Live && l.Role == r.Requires && (r.EventID == nil || (l.EventID != nil && *l.EventID == *r.EventID)) {
				met = true
				break
			}
		}
		if !met {
			out.Unmet = append(out.Unmet, r)
		}
	}
	if len(requirements) > 0 {
		switch len(out.Unmet) {
		case 0:
			out.State = "complete"
		case len(requirements):
			out.State = "missing"
		default:
			out.State = "partial"
		}
	}
	return out
}

// DocumentValues is retained document-link authority.
type DocumentValues struct {
	EventID, ContentVersionID           *string
	ContentSHA256, Role, FormCode, Note string
}

func NormalizeDocument(v DocumentValues) (DocumentValues, error) {
	if err := text(v.FormCode, "form_code", 256, 0, false, false); err != nil {
		return v, err
	}
	if len(v.Note) > 8192 {
		return v, invalid("note", "invalid length or text")
	}
	v.FormCode, v.Note = label(v.FormCode), note(v.Note)
	return v, ValidateDocument(v)
}
func ValidateDocument(v DocumentValues) error {
	if !slices.Contains([]string{"return", "assessment", "settlement", "source", "working_paper", "correspondence", "registration", "other"}, v.Role) {
		return invalid("role", "unknown role")
	}
	if err := text(v.FormCode, "form_code", 256, 64, false, false); err != nil {
		return err
	}
	if label(v.FormCode) != v.FormCode {
		return invalid("form_code", "not canonical")
	}
	if err := text(v.Note, "note", 8192, 0, false, true); err != nil {
		return err
	}
	if note(v.Note) != v.Note {
		return invalid("note", "not canonical")
	}
	if (v.ContentVersionID == nil) != (v.ContentSHA256 == "") {
		return invalid("content_version_id", "invalid pin")
	}
	if v.ContentVersionID != nil && !validDocumentUUID(*v.ContentVersionID) {
		return invalid("content_version_id", "invalid pin")
	}
	if v.EventID != nil && !validDocumentUUID(*v.EventID) {
		return invalid("event_id", "invalid value")
	}
	if v.ContentVersionID != nil {
		if len(v.ContentSHA256) != 64 {
			return invalid("content_sha256", "invalid pin")
		}
		for _, c := range v.ContentSHA256 {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return invalid("content_sha256", "invalid pin")
			}
		}
	}
	return nil
}
func validDocumentUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s[14] != '4' || !strings.ContainsRune("89ab", rune(s[19])) {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
