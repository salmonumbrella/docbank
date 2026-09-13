package document

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrimarySelectionDiffersPerScopeClass(t *testing.T) {
	events := []DocumentEventV1{
		{EventID: "sent", DateKind: "sent", SourceKey: "metadata/gen-1/email.sent", EvidenceKind: "source_metadata", ClaimBasis: "source_asserted", ParseConfidence: "exact"},
		{EventID: "other-package", DateKind: "document_date", SourceKey: "loadfile/pkg-0/row/column/0", EvidenceKind: "package_row"},
		{EventID: "package", DateKind: "document_date", SourceKey: "loadfile/pkg-1/row-9/column/12", EvidenceKind: "package_row", ClaimBasis: "source_asserted"},
	}
	got, ok := SelectPrimaryEvent(events, "email", "", "package:pkg-1", "full")
	require.True(t, ok)
	require.Equal(t, DocumentEventPrimaryV1{EventID: "package", RuleID: "primary-rule/v1", Reason: "package_supplied_document_date", ScopeClass: "package:pkg-1", Disclosure: "full"}, got)
	got, ok = SelectPrimaryEvent(events, "email", "", "vault", "full")
	require.True(t, ok)
	require.Equal(t, "sent", got.EventID)
	require.Equal(t, "email_date_header", got.Reason)
}

func TestPrimarySelectionSkipsUnreliableKinds(t *testing.T) {
	events := []DocumentEventV1{{DateKind: "accessed"}, {DateKind: "printed"}, {DateKind: "observed"}, {DateKind: "exported"}}
	_, ok := SelectPrimaryEvent(events, "other", "", "vault", "full")
	require.False(t, ok)
}

func TestPrimarySelectionOrders(t *testing.T) {
	tests := []struct {
		kind, described DocumentKind
		order           []DateKind
	}{
		{"email", "", []DateKind{"sent", "received", "created", "modified", "document_date", "imported", "vault_recorded"}},
		{"message", "", []DateKind{"sent", "received", "authored", "created", "modified", "document_date", "imported", "vault_recorded"}},
		{"calendar", "", []DateKind{"started", "ended", "authored", "created", "modified", "document_date", "imported", "vault_recorded"}},
		{"image", "", []DateKind{"captured", "created", "authored", "modified", "document_date", "imported", "vault_recorded"}},
		{"audio_video", "", []DateKind{"captured", "authored", "created", "modified", "produced", "document_date", "imported", "vault_recorded"}},
		{"package_record", "email", []DateKind{"sent", "received", "created", "modified", "document_date", "imported", "vault_recorded"}},
		{"package_record", "", []DateKind{"authored", "created", "modified", "produced", "document_date", "imported", "vault_recorded"}},
		{"other", "", []DateKind{"authored", "created", "modified", "produced", "document_date", "imported", "vault_recorded"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"/"+string(tt.described), func(t *testing.T) {
			for i, want := range tt.order {
				var events []DocumentEventV1
				for _, kind := range tt.order[i:] {
					events = append(events, DocumentEventV1{EventID: string(kind), DateKind: kind})
				}
				slices.Reverse(events)
				got, ok := SelectPrimaryEvent(events, tt.kind, tt.described, "vault", "full")
				require.True(t, ok)
				require.Equal(t, string(want), got.EventID)
				require.NotEmpty(t, got.Reason)
			}
		})
	}
}

func TestPrimarySelectionDisclosureAndPackageIsolation(t *testing.T) {
	events := []DocumentEventV1{
		{EventID: "secret", DateKind: "document_date", SourceKey: "loadfile/pkg/row", EvidenceKind: "package_row", Sensitive: true},
		{EventID: "wrong-receipt", DateKind: "sent", SourceKey: "package/pkg-other/receipt", EvidenceKind: "output_receipt"},
		{EventID: "wrong-row", DateKind: "sent", SourceKey: "loadfile/pkg-other/row", EvidenceKind: "package_row"},
		{EventID: "original", DateKind: "received", SourceKey: "metadata/gen/received", EvidenceKind: "source_metadata"},
	}
	for _, tt := range []struct{ disclosure, want string }{{"safe", "original"}, {"full", "secret"}} {
		got, ok := SelectPrimaryEvent(events, "email", "", "package:pkg", tt.disclosure)
		require.True(t, ok)
		require.Equal(t, tt.want, got.EventID)
		require.Equal(t, tt.disclosure, got.Disclosure)
	}
	for _, tt := range []struct{ scope, disclosure string }{{"", "full"}, {"package:", "safe"}, {"package:pkg/row", "full"}, {"person:one", "full"}, {"vault", ""}, {"vault", "redacted"}} {
		_, ok := SelectPrimaryEvent(events, "email", "", tt.scope, tt.disclosure)
		require.False(t, ok, "%+v", tt)
	}
}

func TestPrimarySelectionReliabilityBeforeResolution(t *testing.T) {
	base := DocumentEventV1{EventID: "preferred", DateKind: "sent", SourceKey: "z", ClaimBasis: "source_asserted", ParseConfidence: "exact", Precision: "date"}
	tests := []struct {
		name             string
		preferred, other DocumentEventV1
	}{
		{"interpretation", base, DocumentEventV1{EventID: "supplied", DateKind: "sent", ClaimBasis: "source_asserted", ParseConfidence: "profile_interpreted", Precision: "fraction", UTCKey: "2020-01-01T00:00:00.000000000Z"}},
		{"observation", DocumentEventV1{EventID: "preferred", DateKind: "sent", ParseConfidence: "profile_interpreted"}, DocumentEventV1{EventID: "observed", DateKind: "sent", ClaimBasis: "docbank_observed", ParseConfidence: "exact", UTCKey: "instant"}},
		{"instant", DocumentEventV1{EventID: "preferred", DateKind: "sent", UTCKey: "instant", Precision: "minute"}, DocumentEventV1{EventID: "floating", DateKind: "sent", Precision: "fraction"}},
		{"precision", DocumentEventV1{EventID: "preferred", DateKind: "sent", Precision: "second"}, DocumentEventV1{EventID: "coarse", DateKind: "sent", Precision: "minute"}},
		{"source", DocumentEventV1{EventID: "preferred", DateKind: "sent", SourceKey: "a"}, DocumentEventV1{EventID: "other", DateKind: "sent", SourceKey: "b"}},
		{"event", DocumentEventV1{EventID: "a", DateKind: "sent"}, DocumentEventV1{EventID: "b", DateKind: "sent"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []DocumentEventV1{tt.other, tt.preferred}
			before := slices.Clone(events)
			for range 2 {
				got, ok := SelectPrimaryEvent(events, "email", "", "vault", "full")
				require.True(t, ok)
				require.Equal(t, tt.preferred.EventID, got.EventID)
				require.Equal(t, before, events)
				slices.Reverse(events)
				slices.Reverse(before)
			}
		})
	}
}

func TestPrimarySelectionOriginalBeforeExactPackageClaims(t *testing.T) {
	for _, evidence := range []EventEvidenceKind{"package_row", "output_receipt"} {
		t.Run(string(evidence), func(t *testing.T) {
			original := DocumentEventV1{EventID: "original", DateKind: "sent", EvidenceKind: "source_metadata", SourceKey: "metadata/gen/sent", ClaimBasis: "source_asserted", ParseConfidence: "profile_interpreted", Precision: "date"}
			supplied := DocumentEventV1{EventID: "supplied", DateKind: "sent", EvidenceKind: evidence, SourceKey: "loadfile/pkg/row/sent", ClaimBasis: "source_asserted", ParseConfidence: "exact", Precision: "second", UTCKey: "2020-01-01T00:00:00.000000000Z"}
			for _, scope := range []string{"vault", "package:pkg"} {
				got, ok := SelectPrimaryEvent([]DocumentEventV1{supplied, original}, "email", "", scope, "full")
				require.True(t, ok)
				require.Equal(t, "original", got.EventID)
			}
		})
	}
}
