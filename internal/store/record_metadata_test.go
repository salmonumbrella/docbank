package store

import (
	"testing"

	"github.com/stretchr/testify/require"
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
