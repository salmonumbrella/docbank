package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.kenn.io/docbank/internal/recordtype"
)

const recordsStorageSchemaVersion = 49

type metadataRecordType struct {
	Type        string  `json:"type"`
	ID          string  `json:"type_id" db:"type_id"`
	Source      string  `json:"source" db:"source"`
	Version     int     `json:"version" db:"version"`
	Definition  *[]byte `json:"definition" db:"definition"`
	Fingerprint string  `json:"fingerprint" db:"fingerprint"`
	Active      int     `json:"active" db:"active"`
	Revision    int64   `json:"revision" db:"revision"`
	CreatedAt   string  `json:"created_at" db:"created_at"`
	UpdatedAt   string  `json:"updated_at" db:"updated_at"`
}
type metadataRecord struct {
	Type      string  `json:"type"`
	ID        string  `json:"record_id" db:"record_id"`
	TypeID    string  `json:"type_id" db:"type_id"`
	Title     string  `json:"title" db:"title"`
	Note      string  `json:"note" db:"note"`
	Status    string  `json:"status" db:"status"`
	Revision  int64   `json:"revision" db:"revision"`
	CreatedAt string  `json:"created_at" db:"created_at"`
	UpdatedAt string  `json:"updated_at" db:"updated_at"`
	RetiredAt *string `json:"retired_at" db:"retired_at"`
}
type metadataRecordValue struct {
	Type     string  `json:"type"`
	RecordID string  `json:"record_id" db:"record_id"`
	TypeID   string  `json:"type_id" db:"type_id"`
	FieldID  string  `json:"field_id" db:"field_id"`
	Text     *string `json:"value_text" db:"value_text"`
	Int      *int64  `json:"value_int" db:"value_int"`
	Date     *string `json:"value_date" db:"value_date"`
	DateEnd  *string `json:"value_date_end" db:"value_date_end"`
	Currency *string `json:"value_currency" db:"value_currency"`
}

var recordMetadataTables = []metadataRecordCodec{
	newMetadataTable(metadataTable[metadataRecordType]{record: metadataRecordType{Type: "record_type"}, table: "record_types", suffix: "ORDER BY type_id", validate: validateRecordTypeMetadata, checkExport: true}),
	newMetadataTable(metadataTable[metadataRecord]{record: metadataRecord{Type: "record"}, table: "records", suffix: "ORDER BY record_id", validate: validateRecordMetadata, checkExport: true}),
	newMetadataTable(metadataTable[metadataRecordValue]{record: metadataRecordValue{Type: "record_value"}, table: "record_values", suffix: "ORDER BY record_id,field_id", validate: validateRecordValueMetadata, checkExport: true}),
}

func validateRecordTypeMetadata(v metadataRecordType) error {
	def, ok := recordtype.Builtin(v.ID)
	if !ok || v.Source != "builtin" || v.Version < 1 || v.Version > def.Version || v.Definition != nil || v.Fingerprint != "" || (v.Active != 0 && v.Active != 1) || v.Revision < 1 {
		return errors.New("invalid record type metadata")
	}
	if err := validateMetadataTime("record type created_at", v.CreatedAt); err != nil {
		return err
	}
	return validateMetadataTime("record type updated_at", v.UpdatedAt)
}
func validateRecordMetadata(v metadataRecord) error {
	if err := validateUUIDv4(v.ID); err != nil {
		return err
	}
	if v.Revision < 1 {
		return errors.New("invalid record revision")
	}
	def, ok := recordtype.Builtin(v.TypeID)
	if !ok {
		return errors.New("unknown record type")
	}
	candidate := recordtype.Record{Title: v.Title, Note: v.Note, Status: v.Status}
	normalized, err := recordtype.NormalizeRecord(def, candidate)
	if err != nil || normalized.Title != v.Title || normalized.Note != v.Note {
		return errors.New("invalid record metadata")
	}
	if err = validateMetadataTime("record created_at", v.CreatedAt); err != nil {
		return err
	}
	if err = validateMetadataTime("record updated_at", v.UpdatedAt); err != nil {
		return err
	}
	if v.RetiredAt != nil {
		return validateMetadataTime("record retired_at", *v.RetiredAt)
	}
	return nil
}
func validateRecordValueMetadata(v metadataRecordValue) error {
	if v.Int != nil || v.Currency != nil {
		return errors.New("unsupported record value")
	}
	def, ok := recordtype.Builtin(v.TypeID)
	if !ok {
		return errors.New("unknown record type")
	}
	fieldType := recordFieldType(def, v.FieldID)
	if fieldType == "" {
		return errors.New("unknown record field")
	}
	var value any
	switch fieldType {
	case "date_range":
		if v.Text != nil || (v.Date == nil && v.DateEnd == nil) {
			return errors.New("invalid record value shape")
		}
		m := map[string]any{}
		if v.Date != nil {
			m["start"] = *v.Date
		}
		if v.DateEnd != nil {
			m["end"] = *v.DateEnd
		}
		value = m
	case "date":
		if v.Text != nil || v.Date == nil || v.DateEnd != nil {
			return errors.New("invalid record value shape")
		}
		value = *v.Date
	default:
		if v.Text == nil || v.Date != nil || v.DateEnd != nil {
			return errors.New("invalid record value shape")
		}
		value = *v.Text
	}
	normalized, err := recordtype.NormalizeRecord(def, recordtype.Record{Title: "x", Status: "draft", Fields: map[string]any{v.FieldID: value}})
	if err != nil || !reflect.DeepEqual(normalized.Fields[v.FieldID], value) {
		return errors.New("noncanonical record value")
	}
	return nil
}
func validateRecordMetadataState(ctx context.Context, q metadataQuerier) error {
	for _, codec := range recordMetadataTables {
		if err := codec.validateRows(ctx, q); err != nil {
			return err
		}
	}
	var invalid int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM record_values v JOIN records r ON r.record_id=v.record_id WHERE v.type_id<>r.type_id`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("invalid record value references")
	}
	return nil
}
