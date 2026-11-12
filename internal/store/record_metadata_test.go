package store

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/recordtype"
)

func TestRecordMetadataRejectsUnsupportedAndMalformedValues(t *testing.T) {
	text, date, badDate, currency := "service", "2026-01-02", "2026-02-30", "USD"
	integer := int64(1)
	for _, tc := range []struct {
		name  string
		value metadataRecordValue
		valid bool
	}{
		{"label", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "contract_type", Text: &text}, true},
		{"date", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "signed_on", Date: &date}, true},
		{"bad date", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "signed_on", Date: &badDate}, false},
		{"unused integer", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "contract_type", Text: &text, Int: &integer}, false},
		{"unused currency", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "contract_type", Text: &text, Currency: &currency}, false},
		{"unknown field", metadataRecordValue{RecordID: "record", TypeID: "contract", FieldID: "unknown", Text: &text}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRecordValueMetadata(tc.value)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestRecordMetadataRoundTripAndRejectCorruption(t *testing.T) {
	ctx := t.Context()
	source := newTestStore(t)
	_, err := source.ActivateRecordType(ctx, "contract", 0)
	require.NoError(t, err)
	original, err := source.CreateRecord(ctx, "contract", recordtype.Record{Title: "Example", Status: "active", Fields: map[string]any{"contract_type": "service", "term": map[string]any{"end": "2027-01-01"}}})
	require.NoError(t, err)
	var stream bytes.Buffer
	require.NoError(t, source.ExportMetadata(ctx, &stream))
	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(ctx, bytes.NewReader(stream.Bytes())))
	restored, err := target.GetRecord(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, original, restored)
	var exported bytes.Buffer
	require.NoError(t, target.ExportMetadata(ctx, &exported))
	require.Equal(t, stream.Bytes(), exported.Bytes())

	for _, change := range []struct{ old, new string }{
		{`"version":1,"definition":null`, `"version":999,"definition":null`},
		{`"value_int":null`, `"value_int":1`},
		{`"value_text":"service"`, `"value_text":"SERVICE"`},
	} {
		input := strings.Replace(stream.String(), change.old, change.new, 1)
		require.NotEqual(t, stream.String(), input)
		bad := newTestStore(t)
		require.Error(t, bad.ImportMetadata(ctx, strings.NewReader(input)))
	}
}

func TestRecordWritesRefuseAuditedVault(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	state, err := s.ActivateRecordType(ctx, "contract", 0)
	require.NoError(t, err)
	record, err := s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Example", Status: "draft"})
	require.NoError(t, err)
	seedInitialAuditAuthority(t, s, s.RootID())
	_, err = s.ActivateRecordType(ctx, "contract", state.Revision)
	require.ErrorIs(t, err, ErrAuditMutationUnsupported)
	_, err = s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Another", Status: "draft"})
	require.ErrorIs(t, err, ErrAuditMutationUnsupported)
	title := "Changed"
	_, err = s.UpdateRecord(ctx, record.ID, record.Revision, RecordPatch{Title: &title})
	require.ErrorIs(t, err, ErrAuditMutationUnsupported)
	_, err = s.RetireRecord(ctx, record.ID, record.Revision)
	require.ErrorIs(t, err, ErrAuditMutationUnsupported)
	_, err = s.GetRecord(ctx, record.ID)
	require.NoError(t, err)
}
