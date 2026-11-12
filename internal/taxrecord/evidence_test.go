package taxrecord

import (
	"reflect"
	"testing"
)

func TestTaxEvidenceRequirements(t *testing.T) {
	p, q := "payment-one", "payment-two"
	events := []EventFact{{Kind: "filed"}, {Kind: "amended"}, {Kind: "assessed"}, {Kind: "decision_issued"}, {Kind: "payment", EventID: p}, {Kind: "refund", EventID: q, Voided: true}}
	got := EvaluateEvidence(events, []EvidenceLink{{Role: "return", Live: true}, {Role: "assessment", Live: false}, {Role: "settlement", EventID: &q, Live: true}})
	want := Evidence{State: "partial", Unmet: []EvidenceRequirement{{Requires: "assessment"}, {Requires: "settlement", EventID: &p}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	got = EvaluateEvidence(events, []EvidenceLink{{Role: "return", Live: true}, {Role: "assessment", Live: true}, {Role: "settlement", EventID: &p, Live: true}})
	if got.State != "complete" || len(got.Unmet) != 0 {
		t.Fatalf("complete: %#v", got)
	}
	if got = EvaluateEvidence(nil, nil); got.State != "not_applicable" || len(got.Unmet) != 0 {
		t.Fatalf("empty: %#v", got)
	}
	if got = EvaluateEvidence(events, nil); got.State != "missing" || len(got.Unmet) != 3 {
		t.Fatalf("missing: %#v", got)
	}
	for _, state := range []string{"complete", "partial", "missing", "not_applicable"} {
		if !ValidEvidenceState(state) {
			t.Errorf("rejected %q", state)
		}
	}
	if ValidEvidenceState("Complete") || ValidEvidenceState("") {
		t.Fatal("accepted unknown state")
	}
}

func TestTaxDocumentValidation(t *testing.T) {
	id := "123e4567-e89b-42d3-a456-426614174000"
	good := DocumentValues{Role: "return", ContentVersionID: &id, ContentSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := ValidateDocument(good); err != nil {
		t.Fatal(err)
	}
	for _, v := range []DocumentValues{{Role: "bad"}, {Role: "return", ContentVersionID: &id}, {Role: "return", ContentSHA256: good.ContentSHA256}, {Role: "return", Note: "bad\x00text"}} {
		if ValidateDocument(v) == nil {
			t.Errorf("accepted %#v", v)
		}
	}
}
