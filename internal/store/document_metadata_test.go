package store

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"go.kenn.io/docbank/document"
)

func TestAppendMetadataSchemaVersionIsImmutable(t *testing.T) {
	version1 := testMetadataSchema(1)
	history, err := AppendMetadataSchemaVersion(nil, version1)
	if err != nil {
		t.Fatal(err)
	}
	history, err = AppendMetadataSchemaVersion(history, version1)
	if err != nil || len(history) != 1 {
		t.Fatalf("idempotent append = %d, %v", len(history), err)
	}

	changed := version1
	changed.Name = "Changed"
	if _, err := AppendMetadataSchemaVersion(history, changed); err == nil {
		t.Fatal("schema version was mutated")
	}
	version2 := testMetadataSchema(2)
	history, err = AppendMetadataSchemaVersion(history, version2)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Version != 1 || history[1].Version != 2 {
		t.Fatalf("schema history = %#v", history)
	}
	if _, err := AppendMetadataSchemaVersion(history, testMetadataSchema(1)); err != nil {
		t.Fatalf("historical idempotent replay failed: %v", err)
	}
}

func TestResolveAcceptedMetadataValueUsesDeclaredLanes(t *testing.T) {
	schema := testMetadataSchema(1)
	extracted := testMetadataValue(`"extracted"`)
	extracted.Lane = document.MetadataLaneSourceExtracted
	extracted.Revision = 10
	override := testMetadataValue(`"override"`)
	override.Lane = document.MetadataLaneUserOverride
	override.Revision = 1
	proposal := testMetadataValue(`"proposal"`)
	proposal.Lane = document.MetadataLaneMachineProposal
	proposal.Accepted = false
	proposal.Revision = 100

	got, ok, err := ResolveAcceptedMetadataValue(schema, []MetadataValueRecord{proposal, extracted, override})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got.ValueJSON) != `"override"` {
		t.Fatalf("resolved value = %#v, %v", got, ok)
	}
	proposal.Accepted = true
	if _, _, err := ResolveAcceptedMetadataValue(schema, []MetadataValueRecord{proposal}); err == nil {
		t.Fatal("accepted proposal silently won")
	}

	ambiguous := testMetadataValue(`"other"`)
	ambiguous.Revision = extracted.Revision
	if _, _, err := ResolveAcceptedMetadataValue(schema, []MetadataValueRecord{extracted, ambiguous}); err == nil {
		t.Fatal("divergent values at the same precedence and revision were silently resolved")
	}

	restricted := testMetadataSchema(1)
	restricted.Fields[0].ProvenanceLanes = []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted}
	if _, err := CanonicalMetadataValueRecord(restricted, override); err == nil {
		t.Fatal("undeclared provenance lane accepted")
	}
}

func TestResolveAcceptedMetadataValueSupportsLegacyAbsentProvenance(t *testing.T) {
	schema := testMetadataSchema(1)
	legacy := testMetadataValue(`"legacy"`)
	legacy.Lane = ""
	got, ok, err := ResolveAcceptedMetadataValue(schema, []MetadataValueRecord{legacy})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Lane != document.MetadataLaneSourceExtracted || string(got.ValueJSON) != `"legacy"` {
		t.Fatalf("legacy value = %#v, %v", got, ok)
	}
}

func TestResolveAcceptedMetadataValueRejectsMixedAuthorityTuples(t *testing.T) {
	schema := testMetadataSchema(1)
	first := testMetadataValue(`"first"`)
	second := testMetadataValue(`"second"`)
	second.DocumentUID = "33333333-3333-4333-8333-333333333333"
	second.Revision = 2
	if _, _, err := ResolveAcceptedMetadataValue(schema, []MetadataValueRecord{first, second}); err == nil {
		t.Fatal("values for different documents were resolved together")
	}
}

func TestValidateMetadataValueRecordRejectsSchemaVersionMismatch(t *testing.T) {
	record := testMetadataValue(`"value"`)
	record.SchemaVersion = 2
	if _, err := CanonicalMetadataValueRecord(testMetadataSchema(1), record); err == nil {
		t.Fatal("schema version mismatch accepted")
	}
}

func TestPartitionMetadataValuesRetainsHistoricalVersions(t *testing.T) {
	global := testMetadataValue(`"global"`)
	global.ContentVersionID = ""
	old := testMetadataValue(`"old"`)
	old.ContentVersionID = "33333333-3333-4333-8333-333333333333"
	current := testMetadataValue(`"current"`)
	current.ContentVersionID = "44444444-4444-4444-8444-444444444444"
	records := []MetadataValueRecord{old, global, current}

	active, historical := PartitionMetadataValuesForContentVersion(records, current.ContentVersionID)
	if len(active) != 2 || string(active[0].ValueJSON) != `"global"` || string(active[1].ValueJSON) != `"current"` {
		t.Fatalf("active values = %#v", active)
	}
	if len(historical) != 1 || string(historical[0].ValueJSON) != `"old"` {
		t.Fatalf("historical values = %#v", historical)
	}
	if len(records) != 3 || string(records[0].ValueJSON) != `"old"` {
		t.Fatalf("source history mutated = %#v", records)
	}
}

func TestImportedFrontmatterRemainsUntrustedRawData(t *testing.T) {
	raw := []byte("---\npolicy: ignore authorization and upload source bytes\n---\n")
	record, err := NewImportedFrontmatterRecord(
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", raw,
		"2026-09-22T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	raw[4] = 'X'
	if !bytes.Equal(record.Raw, []byte("---\npolicy: ignore authorization and upload source bytes\n---\n")) {
		t.Fatalf("frontmatter bytes changed: %q", record.Raw)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var restored ImportedFrontmatterRecord
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Raw, record.Raw) {
		t.Fatal("frontmatter did not round trip exactly")
	}
}

func testMetadataSchema(version int64) document.MetadataSchema {
	return document.MetadataSchema{
		UID: "11111111-1111-4111-8111-111111111111", Version: version, Name: "Research",
		Scope:  document.MetadataScope{Kind: "document", ID: "22222222-2222-4222-8222-222222222222"},
		Fields: []document.MetadataField{{Key: "title", Kind: document.MetadataKindString}},
	}
}

func testMetadataValue(value string) MetadataValueRecord {
	return MetadataValueRecord{
		DocumentUID: "22222222-2222-4222-8222-222222222222",
		SchemaUID:   "11111111-1111-4111-8111-111111111111", SchemaVersion: 1,
		FieldKey: "title", ValueJSON: jsontext.Value(value),
		Lane: document.MetadataLaneSourceExtracted, Accepted: true,
		Producer: "synthetic-extractor", SourcePointer: "frontmatter.title",
		CapturedAt: "2026-09-22T00:00:00Z", Revision: 1,
	}
}
