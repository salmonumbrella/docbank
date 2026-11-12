package recordtype

import (
	"strings"
	"testing"
)

func TestContractValidation(t *testing.T) {
	def, ok := Builtin("contract")
	if !ok {
		t.Fatal("missing contract definition")
	}
	valid := Record{Title: " Agreement ", Status: "active", Fields: map[string]any{"contract_type": "service", "term": map[string]any{"start": "2026-01-01", "end": "2026-12-31"}, "summary": "first\r\nsecond"}}
	got, err := NormalizeRecord(def, valid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Agreement" || got.Fields["summary"] != "first\nsecond" {
		t.Fatalf("normalization: %#v", got)
	}
	for _, tc := range []struct {
		name string
		edit func(*Record)
	}{
		{"empty title", func(r *Record) { r.Title = " " }},
		{"title too long", func(r *Record) { r.Title = strings.Repeat("x", 301) }},
		{"control title", func(r *Record) { r.Title = "bad\x00title" }},
		{"control at title edge", func(r *Record) { r.Title = "\nAgreement" }},
		{"unknown field", func(r *Record) { r.Fields = map[string]any{"other": "x"} }},
		{"invalid label", func(r *Record) { r.Fields = map[string]any{"contract_type": "Bad label"} }},
		{"long terms", func(r *Record) { r.Fields = map[string]any{"summary": strings.Repeat("x", 8193)} }},
		{"year zero", func(r *Record) { r.Fields = map[string]any{"signed_on": "0000-01-01"} }},
		{"empty date", func(r *Record) { r.Fields = map[string]any{"signed_on": ""} }},
		{"empty date endpoint", func(r *Record) { r.Fields = map[string]any{"term": map[string]any{"end": ""}} }},
		{"invalid date", func(r *Record) { r.Fields = map[string]any{"signed_on": "2026-02-30"} }},
		{"reversed term", func(r *Record) {
			r.Fields = map[string]any{"term": map[string]any{"start": "2026-03-01", "end": "2026-02-01"}}
		}},
		{"derived status", func(r *Record) { r.Status = "expired" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.edit(&r)
			if _, err := NormalizeRecord(def, r); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestRecordTextLimitAfterNormalization(t *testing.T) {
	def, _ := Builtin("contract")
	r := Record{Title: "Example", Status: "draft", Note: strings.Repeat("e\u0301", 2731)}
	if _, err := NormalizeRecord(def, r); err != nil {
		t.Fatal(err)
	}
	r.Note = strings.Repeat("é", 4097)
	if _, err := NormalizeRecord(def, r); err == nil {
		t.Fatal("expected normalized length rejection")
	}
	r.Note = ""
	r.Fields = map[string]any{"contract_type": ""}
	got, err := NormalizeRecord(def, r)
	if err != nil || len(got.Fields) != 0 {
		t.Fatalf("empty optional label: %#v, %v", got.Fields, err)
	}
}

func TestEffectiveStatusExpiryBoundary(t *testing.T) {
	def, _ := Builtin("contract")
	fields := map[string]any{"term": map[string]any{"end": "2026-01-02"}}
	for _, tc := range []struct{ status, asOf, want string }{
		{"active", "2026-01-01", "active"}, {"active", "2026-01-02", "active"},
		{"active", "2026-01-03", "expired"}, {"draft", "2026-01-03", "draft"},
	} {
		if got := EffectiveStatus(def, tc.status, fields, tc.asOf); got != tc.want {
			t.Errorf("%s %s: got %s want %s", tc.status, tc.asOf, got, tc.want)
		}
	}
}
