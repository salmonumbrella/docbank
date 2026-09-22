package document

import (
	"bytes"
	"encoding/json/v2"
	"math"
	"strings"
	"testing"
)

func TestValidateMetadataScalar(t *testing.T) {
	if ValidateMetadataScalar("number", "42") == nil {
		t.Fatal("numeric string coerced")
	}
	if err := ValidateMetadataScalar("number", float64(42)); err != nil {
		t.Fatal(err)
	}
	if ValidateMetadataScalar("boolean", nil) == nil {
		t.Fatal("missing became false")
	}
}

func TestValidateMetadataValueKindsAndConstraints(t *testing.T) {
	tests := []struct {
		name    string
		field   MetadataField
		value   any
		wantErr bool
	}{
		{name: "empty string is present", field: MetadataField{Key: "title", Kind: MetadataKindString}, value: ""},
		{name: "invalid UTF-8 string", field: MetadataField{Key: "title", Kind: MetadataKindString}, value: string([]byte{0xff}), wantErr: true},
		{name: "number", field: MetadataField{Key: "score", Kind: MetadataKindNumber}, value: float64(42)},
		{name: "numeric string", field: MetadataField{Key: "score", Kind: MetadataKindNumber}, value: "42", wantErr: true},
		{name: "not a number", field: MetadataField{Key: "score", Kind: MetadataKindNumber}, value: math.NaN(), wantErr: true},
		{name: "infinity", field: MetadataField{Key: "score", Kind: MetadataKindNumber}, value: math.Inf(1), wantErr: true},
		{name: "boolean", field: MetadataField{Key: "reviewed", Kind: MetadataKindBoolean}, value: false},
		{name: "null", field: MetadataField{Key: "reviewed", Kind: MetadataKindBoolean}, value: nil, wantErr: true},
		{name: "date", field: MetadataField{Key: "filed_on", Kind: MetadataKindDate}, value: "2026-02-28"},
		{name: "invalid date", field: MetadataField{Key: "filed_on", Kind: MetadataKindDate}, value: "2026-02-30", wantErr: true},
		{name: "timestamp is not a date", field: MetadataField{Key: "filed_on", Kind: MetadataKindDate}, value: "2026-02-28T00:00:00Z", wantErr: true},
		{name: "enum", field: MetadataField{Key: "state", Kind: MetadataKindEnum, EnumValues: []string{"draft", "final"}}, value: "final"},
		{name: "enum outside declaration", field: MetadataField{Key: "state", Kind: MetadataKindEnum, EnumValues: []string{"draft", "final"}}, value: "other", wantErr: true},
		{name: "empty set is present", field: MetadataField{Key: "topics", Kind: MetadataKindStringSet}, value: []string{}},
		{name: "string set", field: MetadataField{Key: "topics", Kind: MetadataKindStringSet}, value: []string{"alpha", "beta"}},
		{name: "duplicate set member", field: MetadataField{Key: "topics", Kind: MetadataKindStringSet}, value: []string{"alpha", "alpha"}, wantErr: true},
		{name: "wrong set representation", field: MetadataField{Key: "topics", Kind: MetadataKindStringSet}, value: []any{"alpha"}, wantErr: true},
		{name: "oversized value", field: MetadataField{Key: "title", Kind: MetadataKindString}, value: strings.Repeat("x", MaxMetadataValueBytes+1), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMetadataValue(test.field, test.value)
			if test.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeMetadataValueUsesStrictJSONTypes(t *testing.T) {
	number := MetadataField{Key: "score", Kind: MetadataKindNumber}
	got, err := DecodeMetadataValue(number, []byte("42"))
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(42) {
		t.Fatalf("decoded number = %#v", got)
	}
	for _, encoded := range []string{`"42"`, "1e10000", "NaN", "null", "", "42 true"} {
		if _, err := DecodeMetadataValue(number, []byte(encoded)); err == nil {
			t.Fatalf("accepted invalid number JSON %q", encoded)
		}
	}

	set := MetadataField{Key: "topics", Kind: MetadataKindStringSet}
	got, err = DecodeMetadataValue(set, []byte(`["beta","alpha"]`))
	if err != nil {
		t.Fatal(err)
	}
	values, ok := got.([]string)
	if !ok || len(values) != 2 || values[0] != "alpha" || values[1] != "beta" {
		t.Fatalf("decoded set = %#v", got)
	}
}

func TestDecodeMetadataValueAcceptsStringSetAtSemanticByteLimit(t *testing.T) {
	values := make([]string, 0, MaxMetadataValueBytes/3)
	for first := byte(0); first < 32 && len(values) < cap(values); first++ {
		for second := byte(0); second < 32 && len(values) < cap(values); second++ {
			for third := byte(0); third < 32 && len(values) < cap(values); third++ {
				values = append(values, string([]byte{first, second, third}))
			}
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= MaxMetadataValueBytes*6+2 {
		t.Fatalf("encoded control-character set = %d bytes", len(encoded))
	}

	decoded, err := DecodeMetadataValue(MetadataField{Key: "topics", Kind: MetadataKindStringSet}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	decodedValues, ok := decoded.([]string)
	if !ok || len(decodedValues) != len(values) {
		t.Fatal("decoded string set did not preserve the accepted members")
	}
}

func TestValidateMetadataSchemaAndDocumentBounds(t *testing.T) {
	schema := MetadataSchema{
		UID: "11111111-1111-4111-8111-111111111111", Version: 1, Name: "Research",
		Scope: MetadataScope{Kind: "collection", ID: "22222222-2222-4222-8222-222222222222"},
		Fields: []MetadataField{
			{Key: "題名", Kind: MetadataKindString, RequiredForProfile: true},
			{Key: "topics", Kind: MetadataKindStringSet},
		},
	}
	if err := ValidateMetadataSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMetadataDocument(schema, map[string]any{"題名": ""}); err != nil {
		t.Fatalf("empty string must remain present: %v", err)
	}
	if err := ValidateMetadataDocument(schema, map[string]any{"題名": "", "topics": []string{}}); err != nil {
		t.Fatalf("empty set must remain present: %v", err)
	}
	if ValidateMetadataDocument(schema, map[string]any{"題名": nil}) == nil {
		t.Fatal("null value accepted")
	}
	if ValidateMetadataDocument(schema, map[string]any{"topics": []string{}}) == nil {
		t.Fatal("missing required field accepted")
	}
	if ValidateMetadataDocument(schema, map[string]any{"題名": "ok", "unknown": "value"}) == nil {
		t.Fatal("undeclared field accepted")
	}

	tooMany := schema
	tooMany.Fields = make([]MetadataField, MaxMetadataSchemaFields+1)
	for index := range tooMany.Fields {
		tooMany.Fields[index] = MetadataField{Key: string(rune('a'+index%26)) + strings.Repeat("x", index/26), Kind: MetadataKindString}
	}
	if ValidateMetadataSchema(tooMany) == nil {
		t.Fatal("oversized schema accepted")
	}
	oversizedField := schema
	oversizedField.Fields = []MetadataField{{Key: strings.Repeat("x", MaxMetadataLabelBytes+1), Kind: MetadataKindString}}
	if ValidateMetadataSchema(oversizedField) == nil {
		t.Fatal("oversized field key accepted")
	}

	largeSchema := schema
	largeSchema.Fields = make([]MetadataField, 9)
	largeDocument := make(map[string]any, len(largeSchema.Fields))
	for index := range largeSchema.Fields {
		key := string(rune('a' + index))
		largeSchema.Fields[index] = MetadataField{Key: key, Kind: MetadataKindString}
		largeDocument[key] = strings.Repeat("x", MaxMetadataValueBytes)
	}
	if ValidateMetadataDocument(largeSchema, largeDocument) == nil {
		t.Fatal("oversized document metadata accepted")
	}
}

func TestCanonicalMetadataSchemaRejectsInvalidDeclarations(t *testing.T) {
	base := MetadataSchema{
		UID: "11111111-1111-4111-8111-111111111111", Version: 1, Name: "Research",
		Scope:  MetadataScope{Kind: "document", ID: "22222222-2222-4222-8222-222222222222"},
		Fields: []MetadataField{{Key: "state", Kind: MetadataKindEnum, EnumValues: []string{"final", "draft"}}},
	}
	encoded, err := CanonicalMetadataSchemaJSON(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Fields[0].EnumValues = []string{"draft", "final"}
	reorderedJSON, err := CanonicalMetadataSchemaJSON(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reorderedJSON) {
		t.Fatalf("canonical schemas differ:\n%s\n%s", encoded, reorderedJSON)
	}

	duplicate := base
	duplicate.Fields[0].EnumValues = []string{"draft", "draft"}
	if ValidateMetadataSchema(duplicate) == nil {
		t.Fatal("duplicate enum declaration accepted")
	}
	unknown := base
	unknown.Fields[0] = MetadataField{Key: "state", Kind: "integer"}
	if ValidateMetadataSchema(unknown) == nil {
		t.Fatal("unknown metadata kind accepted")
	}
	invalidKey := base
	invalidKey.Fields[0].Key = string([]byte{0xff})
	if ValidateMetadataSchema(invalidKey) == nil {
		t.Fatal("invalid UTF-8 field key accepted")
	}
}
