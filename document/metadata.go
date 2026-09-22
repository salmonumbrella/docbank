package document

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxMetadataSchemaFields   = 64
	MaxMetadataValueBytes     = 16 << 10
	MaxMetadataDocumentBytes  = 128 << 10
	MaxMetadataLabelBytes     = 256
	maxMetadataJSONValueBytes = MaxMetadataValueBytes*9 + 2
)

// MetadataKind is a declared sidecar field type. Values reach validators only
// after JSON decoding; validators never coerce between kinds.
type MetadataKind string

const (
	MetadataKindString    MetadataKind = "string"
	MetadataKindNumber    MetadataKind = "number"
	MetadataKindBoolean   MetadataKind = "boolean"
	MetadataKindDate      MetadataKind = "date"
	MetadataKindEnum      MetadataKind = "enum"
	MetadataKindStringSet MetadataKind = "string_set"
)

// MetadataProvenanceLane keeps accepted user and source authority separate
// from machine proposals.
type MetadataProvenanceLane string

const (
	MetadataLaneUserOverride    MetadataProvenanceLane = "user_override"
	MetadataLaneSourceExtracted MetadataProvenanceLane = "source_extracted"
	MetadataLaneMachineProposal MetadataProvenanceLane = "machine_proposal"
)

// MetadataScope is an explicit application boundary. Scope kinds and their
// authorization semantics are owned by the integrating service.
type MetadataScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// MetadataField declares one stable typed key and its non-executable
// constraints.
type MetadataField struct {
	Key                string                   `json:"key"`
	Kind               MetadataKind             `json:"kind"`
	EnumValues         []string                 `json:"enum_values,omitzero"`
	RequiredForProfile bool                     `json:"required_for_profile"`
	Sensitive          bool                     `json:"sensitive"`
	ProvenanceLanes    []MetadataProvenanceLane `json:"provenance_lanes,omitzero"`
	Filterable         bool                     `json:"filterable"`
	KeywordSearchable  bool                     `json:"keyword_searchable"`
	EmbeddingEligible  bool                     `json:"embedding_eligible"`
}

// MetadataSchema is one immutable version of a sidecar schema.
type MetadataSchema struct {
	UID     string          `json:"uid"`
	Version int64           `json:"version"`
	Name    string          `json:"name"`
	Scope   MetadataScope   `json:"scope"`
	Fields  []MetadataField `json:"fields"`
}

// ValidateMetadataScalar rejects implicit coercion at the decoded JSON
// boundary. Null and missing values are not scalars.
func ValidateMetadataScalar(kind string, value any) error {
	valid := false
	switch kind {
	case "string":
		_, valid = value.(string)
	case "number":
		number, ok := value.(float64)
		valid = ok && !math.IsNaN(number) && !math.IsInf(number, 0)
	case "boolean":
		_, valid = value.(bool)
	}
	if !valid {
		return errors.New("metadata type mismatch")
	}
	return nil
}

// ValidateMetadataValue validates one already-decoded value against a field.
func ValidateMetadataValue(field MetadataField, value any) error {
	if value == nil {
		return errors.New("metadata value is null or missing")
	}
	var err error
	switch field.Kind {
	case MetadataKindString:
		err = ValidateMetadataScalar(string(field.Kind), value)
		if text, ok := value.(string); err == nil && ok {
			err = validateMetadataValueString(text)
		}
	case MetadataKindNumber, MetadataKindBoolean:
		err = ValidateMetadataScalar(string(field.Kind), value)
	case MetadataKindDate:
		err = ValidateMetadataDate(value)
	case MetadataKindEnum:
		err = ValidateMetadataEnum(value, field.EnumValues)
	case MetadataKindStringSet:
		err = ValidateMetadataStringSet(value)
	default:
		return fmt.Errorf("unknown metadata kind %q", field.Kind)
	}
	if err != nil {
		return err
	}
	if metadataValueBytes(value) > MaxMetadataValueBytes {
		return errors.New("metadata value is too large")
	}
	return nil
}

// ValidateMetadataDate accepts only a canonical calendar date.
func ValidateMetadataDate(value any) error {
	date, ok := value.(string)
	if !ok {
		return errors.New("metadata date type mismatch")
	}
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil || parsed.Format(time.DateOnly) != date {
		return errors.New("metadata date is invalid")
	}
	return nil
}

// ValidateMetadataEnum requires exact membership in the declared values.
func ValidateMetadataEnum(value any, allowed []string) error {
	text, ok := value.(string)
	if !ok {
		return errors.New("metadata enum type mismatch")
	}
	if !slices.Contains(allowed, text) {
		return errors.New("metadata enum value is not declared")
	}
	return nil
}

// ValidateMetadataStringSet rejects duplicate members and non-string slices.
func ValidateMetadataStringSet(value any) error {
	values, ok := value.([]string)
	if !ok {
		return errors.New("metadata string set type mismatch")
	}
	seen := make(map[string]struct{}, len(values))
	for _, member := range values {
		if !utf8.ValidString(member) {
			return errors.New("metadata string set contains invalid UTF-8")
		}
		if _, exists := seen[member]; exists {
			return errors.New("metadata string set contains a duplicate")
		}
		seen[member] = struct{}{}
	}
	return nil
}

// DecodeMetadataValue decodes exactly one JSON value, normalizes JSON numbers
// to float64, and returns the declared Go representation. It does not coerce
// strings, booleans, numbers, or null.
func DecodeMetadataValue(field MetadataField, encoded []byte) (any, error) {
	if len(encoded) == 0 || len(encoded) > maxMetadataJSONValueBytes {
		return nil, errors.New("metadata JSON value is empty or too large")
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decoding metadata value: %w", err)
	}
	switch field.Kind {
	case MetadataKindStringSet:
		raw, ok := decoded.([]any)
		if !ok {
			return nil, errors.New("metadata string set type mismatch")
		}
		values := make([]string, len(raw))
		for index, item := range raw {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("metadata string set contains a non-string")
			}
			values[index] = text
		}
		slices.Sort(values)
		decoded = values
	default:
		// ValidateMetadataValue reports unknown kinds below.
	}
	if err := ValidateMetadataValue(field, decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// ValidateMetadataSchema validates bounded, non-executable declarations.
func ValidateMetadataSchema(schema MetadataSchema) error {
	if !metadataUUID(schema.UID) || schema.Version < 1 {
		return errors.New("metadata schema identity is invalid")
	}
	if err := validateMetadataLabel(schema.Name, "schema name"); err != nil {
		return err
	}
	if err := validateMetadataLabel(schema.Scope.Kind, "scope kind"); err != nil {
		return err
	}
	if err := validateMetadataLabel(schema.Scope.ID, "scope ID"); err != nil {
		return err
	}
	if len(schema.Fields) > MaxMetadataSchemaFields {
		return errors.New("metadata schema has too many fields")
	}
	seenFields := make(map[string]struct{}, len(schema.Fields))
	for index, field := range schema.Fields {
		if err := validateMetadataLabel(field.Key, "field key"); err != nil {
			return fmt.Errorf("metadata field %d: %w", index, err)
		}
		if _, exists := seenFields[field.Key]; exists {
			return fmt.Errorf("metadata schema has duplicate field %q", field.Key)
		}
		seenFields[field.Key] = struct{}{}
		if err := validateMetadataField(field); err != nil {
			return fmt.Errorf("metadata field %q: %w", field.Key, err)
		}
	}
	return nil
}

// CanonicalMetadataSchemaJSON provides stable equality bytes for immutable
// schema-version records.
func CanonicalMetadataSchemaJSON(schema MetadataSchema) ([]byte, error) {
	if err := ValidateMetadataSchema(schema); err != nil {
		return nil, err
	}
	canonical := schema
	canonical.Fields = append([]MetadataField(nil), schema.Fields...)
	for index := range canonical.Fields {
		field := &canonical.Fields[index]
		field.EnumValues = append([]string(nil), field.EnumValues...)
		slices.Sort(field.EnumValues)
		field.ProvenanceLanes = append([]MetadataProvenanceLane(nil), field.ProvenanceLanes...)
		slices.Sort(field.ProvenanceLanes)
	}
	slices.SortFunc(canonical.Fields, func(a, b MetadataField) int {
		return strings.Compare(a.Key, b.Key)
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encoding metadata schema: %w", err)
	}
	if len(encoded) > MaxMetadataDocumentBytes {
		return nil, errors.New("metadata schema is too large")
	}
	return encoded, nil
}

// ValidateMetadataDocument distinguishes an absent map entry from a present
// empty value. Present null values are always invalid.
func ValidateMetadataDocument(schema MetadataSchema, values map[string]any) error {
	if err := ValidateMetadataSchema(schema); err != nil {
		return err
	}
	fields := make(map[string]MetadataField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.Key] = field
		if field.RequiredForProfile {
			if _, present := values[field.Key]; !present {
				return fmt.Errorf("required metadata field %q is missing", field.Key)
			}
		}
	}
	total := 0
	for key, value := range values {
		field, declared := fields[key]
		if !declared {
			return fmt.Errorf("metadata field %q is not declared", key)
		}
		if err := ValidateMetadataValue(field, value); err != nil {
			return fmt.Errorf("metadata field %q: %w", key, err)
		}
		total += len(key) + metadataValueBytes(value)
		if total > MaxMetadataDocumentBytes {
			return errors.New("document metadata is too large")
		}
	}
	return nil
}

func validateMetadataField(field MetadataField) error {
	switch field.Kind {
	case MetadataKindString, MetadataKindNumber, MetadataKindBoolean,
		MetadataKindDate, MetadataKindStringSet:
		if len(field.EnumValues) != 0 {
			return errors.New("non-enum field declares enum values")
		}
	case MetadataKindEnum:
		if len(field.EnumValues) == 0 {
			return errors.New("enum field has no declared values")
		}
		seen := make(map[string]struct{}, len(field.EnumValues))
		for _, value := range field.EnumValues {
			if err := validateMetadataValueString(value); err != nil {
				return fmt.Errorf("enum declaration: %w", err)
			}
			if _, exists := seen[value]; exists {
				return errors.New("enum declaration contains a duplicate")
			}
			seen[value] = struct{}{}
		}
	default:
		return fmt.Errorf("unknown metadata kind %q", field.Kind)
	}
	seenLanes := make(map[MetadataProvenanceLane]struct{}, len(field.ProvenanceLanes))
	for _, lane := range field.ProvenanceLanes {
		if !ValidMetadataProvenanceLane(lane) {
			return fmt.Errorf("unknown provenance lane %q", lane)
		}
		if _, exists := seenLanes[lane]; exists {
			return errors.New("provenance rules contain a duplicate lane")
		}
		seenLanes[lane] = struct{}{}
	}
	return nil
}

// ValidMetadataProvenanceLane reports whether a non-legacy lane is declared.
func ValidMetadataProvenanceLane(lane MetadataProvenanceLane) bool {
	switch lane {
	case MetadataLaneUserOverride, MetadataLaneSourceExtracted, MetadataLaneMachineProposal:
		return true
	default:
		return false
	}
}

func validateMetadataLabel(value, name string) error {
	if value == "" || len(value) > MaxMetadataLabelBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value || norm.NFC.String(value) != value {
		return fmt.Errorf("metadata %s must be bounded canonical UTF-8", name)
	}
	return nil
}

func validateMetadataValueString(value string) error {
	if !utf8.ValidString(value) || len(value) > MaxMetadataValueBytes {
		return errors.New("value must be bounded UTF-8")
	}
	return nil
}

func metadataValueBytes(value any) int {
	switch typed := value.(type) {
	case string:
		return len(typed)
	case float64:
		return 8
	case bool:
		return 1
	case []string:
		total := 0
		for _, member := range typed {
			total += len(member)
		}
		return total
	default:
		return MaxMetadataValueBytes + 1
	}
}

func metadataUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == value
}
