package store

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/docbank/document"
)

const MaxImportedFrontmatterBytes = 256 << 10

// MetadataValueRecord is the repository-facing sidecar authority record. The
// database layout and logical export registration are separate integration
// concerns; ValueJSON is canonical JSON for the field's declared type.
type MetadataValueRecord struct {
	DocumentUID      string                          `json:"document_uid"`
	ContentVersionID string                          `json:"content_version_id,omitzero"`
	SchemaUID        string                          `json:"schema_uid"`
	SchemaVersion    int64                           `json:"schema_version"`
	FieldKey         string                          `json:"field_key"`
	ValueJSON        jsontext.Value                  `json:"value"`
	Lane             document.MetadataProvenanceLane `json:"lane,omitzero"`
	Accepted         bool                            `json:"accepted"`
	Producer         string                          `json:"producer"`
	SourcePointer    string                          `json:"source_pointer,omitzero"`
	CapturedAt       string                          `json:"captured_at"`
	Revision         int64                           `json:"revision"`
}

// ImportedFrontmatterRecord retains source-owned bytes as data. No field in
// this record is interpreted as policy or as an accepted metadata override.
type ImportedFrontmatterRecord struct {
	DocumentUID      string `json:"document_uid"`
	ContentVersionID string `json:"content_version_id"`
	Raw              []byte `json:"raw"`
	CapturedAt       string `json:"captured_at"`
}

// AppendMetadataSchemaVersion appends a newer immutable version. Replaying a
// byte-equivalent historical version is idempotent; changing one is rejected.
func AppendMetadataSchemaVersion(
	history []document.MetadataSchema,
	candidate document.MetadataSchema,
) ([]document.MetadataSchema, error) {
	candidateJSON, err := document.CanonicalMetadataSchemaJSON(candidate)
	if err != nil {
		return nil, err
	}
	result := cloneMetadataSchemas(history)
	maximum := int64(0)
	for _, existing := range history {
		if existing.UID != candidate.UID {
			continue
		}
		if existing.Version > maximum {
			maximum = existing.Version
		}
		if existing.Version != candidate.Version {
			continue
		}
		existingJSON, encodeErr := document.CanonicalMetadataSchemaJSON(existing)
		if encodeErr != nil {
			return nil, fmt.Errorf("existing metadata schema is invalid: %w", encodeErr)
		}
		if !bytes.Equal(existingJSON, candidateJSON) {
			return nil, errors.New("metadata schema version is immutable")
		}
		return result, nil
	}
	if candidate.Version <= maximum {
		return nil, errors.New("metadata schema version must increase")
	}
	result = append(result, cloneMetadataSchema(candidate))
	slices.SortFunc(result, func(a, b document.MetadataSchema) int {
		if compared := strings.Compare(a.UID, b.UID); compared != 0 {
			return compared
		}
		if a.Version < b.Version {
			return -1
		}
		if a.Version > b.Version {
			return 1
		}
		return 0
	})
	return result, nil
}

// CanonicalMetadataValueRecord validates the schema binding, provenance, and
// typed JSON value, then returns an owned canonical record. An absent legacy
// lane is read as source-extracted authority.
func CanonicalMetadataValueRecord(
	schema document.MetadataSchema,
	record MetadataValueRecord,
) (MetadataValueRecord, error) {
	if err := document.ValidateMetadataSchema(schema); err != nil {
		return MetadataValueRecord{}, err
	}
	if validateUUIDv4(record.DocumentUID) != nil ||
		record.ContentVersionID != "" && validateUUIDv4(record.ContentVersionID) != nil ||
		validateUUIDv4(record.SchemaUID) != nil || record.Revision < 1 {
		return MetadataValueRecord{}, errors.New("metadata value identity is invalid")
	}
	if record.SchemaUID != schema.UID || record.SchemaVersion != schema.Version {
		return MetadataValueRecord{}, errors.New("metadata value schema version does not match")
	}
	var field document.MetadataField
	found := false
	for _, declared := range schema.Fields {
		if declared.Key == record.FieldKey {
			field, found = declared, true
			break
		}
	}
	if !found {
		return MetadataValueRecord{}, errors.New("metadata value field is not declared")
	}
	value, err := document.DecodeMetadataValue(field, record.ValueJSON)
	if err != nil {
		return MetadataValueRecord{}, fmt.Errorf("metadata value: %w", err)
	}
	canonicalValue, err := json.Marshal(value)
	if err != nil {
		return MetadataValueRecord{}, fmt.Errorf("encoding metadata value: %w", err)
	}
	if record.Lane == "" {
		record.Lane = document.MetadataLaneSourceExtracted
	}
	if !document.ValidMetadataProvenanceLane(record.Lane) {
		return MetadataValueRecord{}, errors.New("metadata provenance lane is invalid")
	}
	if len(field.ProvenanceLanes) != 0 && !slices.Contains(field.ProvenanceLanes, record.Lane) {
		return MetadataValueRecord{}, errors.New("metadata provenance lane is not allowed for the field")
	}
	if record.Lane == document.MetadataLaneMachineProposal && record.Accepted {
		return MetadataValueRecord{}, errors.New("metadata proposal cannot be an accepted value")
	}
	if err := validateMetadataRecordText(record.Producer, "producer", false); err != nil {
		return MetadataValueRecord{}, err
	}
	if err := validateMetadataRecordText(record.SourcePointer, "source pointer", true); err != nil {
		return MetadataValueRecord{}, err
	}
	capturedAt, err := canonicalMetadataCaptureTime(record.CapturedAt)
	if err != nil {
		return MetadataValueRecord{}, err
	}
	record.CapturedAt = capturedAt
	record.ValueJSON = append(jsontext.Value(nil), canonicalValue...)
	return record, nil
}

// ResolveAcceptedMetadataValue applies the fixed accepted-value precedence:
// explicit user override, then accepted extracted value. Proposals are never
// returned. Revision breaks ties within a lane; divergent exact ties fail.
func ResolveAcceptedMetadataValue(
	schema document.MetadataSchema,
	records []MetadataValueRecord,
) (MetadataValueRecord, bool, error) {
	var selected MetadataValueRecord
	selectedRank := 0
	found := false
	var documentUID, contentVersionID, fieldKey string
	tupleSet := false
	for _, input := range records {
		record, err := CanonicalMetadataValueRecord(schema, input)
		if err != nil {
			return MetadataValueRecord{}, false, err
		}
		if !tupleSet {
			documentUID, contentVersionID, fieldKey = record.DocumentUID, record.ContentVersionID, record.FieldKey
			tupleSet = true
		} else if record.DocumentUID != documentUID || record.ContentVersionID != contentVersionID ||
			record.FieldKey != fieldKey {
			return MetadataValueRecord{}, false, errors.New("metadata values do not share one authority tuple")
		}
		if !record.Accepted || record.Lane == document.MetadataLaneMachineProposal {
			continue
		}
		rank := 1
		if record.Lane == document.MetadataLaneUserOverride {
			rank = 2
		}
		if !found || rank > selectedRank || rank == selectedRank && record.Revision > selected.Revision {
			selected, selectedRank, found = record, rank, true
			continue
		}
		if rank == selectedRank && record.Revision == selected.Revision && !metadataValueRecordsEqual(selected, record) {
			return MetadataValueRecord{}, false, errors.New("accepted metadata values are ambiguous")
		}
	}
	return selected, found, nil
}

// PartitionMetadataValuesForContentVersion selects document-scoped and exact
// current-version records while retaining other version-bound history.
func PartitionMetadataValuesForContentVersion(
	records []MetadataValueRecord,
	contentVersionID string,
) (active, historical []MetadataValueRecord) {
	for _, record := range records {
		cloned := cloneMetadataValueRecord(record)
		if record.ContentVersionID == "" || record.ContentVersionID == contentVersionID {
			active = append(active, cloned)
		} else {
			historical = append(historical, cloned)
		}
	}
	return active, historical
}

// NewImportedFrontmatterRecord clones source bytes without parsing them.
func NewImportedFrontmatterRecord(
	documentUID, contentVersionID string,
	raw []byte,
	capturedAt string,
) (ImportedFrontmatterRecord, error) {
	if validateUUIDv4(documentUID) != nil || validateUUIDv4(contentVersionID) != nil {
		return ImportedFrontmatterRecord{}, errors.New("imported frontmatter identity is invalid")
	}
	if len(raw) == 0 || len(raw) > MaxImportedFrontmatterBytes {
		return ImportedFrontmatterRecord{}, errors.New("imported frontmatter is empty or too large")
	}
	canonicalTime, err := canonicalMetadataCaptureTime(capturedAt)
	if err != nil {
		return ImportedFrontmatterRecord{}, err
	}
	return ImportedFrontmatterRecord{
		DocumentUID: documentUID, ContentVersionID: contentVersionID,
		Raw: append([]byte(nil), raw...), CapturedAt: canonicalTime,
	}, nil
}

func cloneMetadataSchemas(values []document.MetadataSchema) []document.MetadataSchema {
	result := make([]document.MetadataSchema, len(values))
	for index, value := range values {
		result[index] = cloneMetadataSchema(value)
	}
	return result
}

func cloneMetadataSchema(value document.MetadataSchema) document.MetadataSchema {
	value.Fields = append([]document.MetadataField(nil), value.Fields...)
	for index := range value.Fields {
		value.Fields[index].EnumValues = append([]string(nil), value.Fields[index].EnumValues...)
		value.Fields[index].ProvenanceLanes = append(
			[]document.MetadataProvenanceLane(nil), value.Fields[index].ProvenanceLanes...)
	}
	return value
}

func cloneMetadataValueRecord(value MetadataValueRecord) MetadataValueRecord {
	value.ValueJSON = append(jsontext.Value(nil), value.ValueJSON...)
	return value
}

func metadataValueRecordsEqual(a, b MetadataValueRecord) bool {
	return a.DocumentUID == b.DocumentUID && a.ContentVersionID == b.ContentVersionID &&
		a.SchemaUID == b.SchemaUID && a.SchemaVersion == b.SchemaVersion &&
		a.FieldKey == b.FieldKey && bytes.Equal(a.ValueJSON, b.ValueJSON) &&
		a.Lane == b.Lane && a.Accepted == b.Accepted && a.Producer == b.Producer &&
		a.SourcePointer == b.SourcePointer && a.CapturedAt == b.CapturedAt &&
		a.Revision == b.Revision
}

func canonicalMetadataCaptureTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("invalid metadata capture time: %w", err)
	}
	return parsed.UTC().Format(timestampLayout), nil
}

func validateMetadataRecordText(value, name string, optional bool) error {
	if optional && value == "" {
		return nil
	}
	if value == "" || !utf8.ValidString(value) || len(value) > document.MaxMetadataValueBytes {
		return fmt.Errorf("metadata %s must be bounded UTF-8", name)
	}
	return nil
}
