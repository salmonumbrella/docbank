package store

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"go.kenn.io/docbank/document"
)

const (
	metadataSchemaVersionType       = "metadata_schema_version"
	metadataValueType               = "metadata_value"
	metadataImportedFrontmatterType = "metadata_imported_frontmatter"
)

type metadataSchemaVersionRecord struct {
	Type      string                  `json:"type"`
	Schema    document.MetadataSchema `json:"schema"`
	CreatedAt string                  `json:"created_at"`
}

type metadataValueRecord struct {
	Type  string              `json:"type"`
	Value MetadataValueRecord `json:"value"`
}

type metadataImportedFrontmatterRecord struct {
	Type        string                    `json:"type"`
	Frontmatter ImportedFrontmatterRecord `json:"frontmatter"`
}

func isDocumentMetadataAuthorityType(kind string) bool {
	return kind == metadataSchemaVersionType || kind == metadataValueType || kind == metadataImportedFrontmatterType
}

func exportDocumentMetadataAuthority(ctx context.Context, q metadataQuerier, write metadataWrite) error {
	rows, err := q.QueryContext(ctx, `SELECT schema_uid,schema_version,canonical_json,created_at
		FROM metadata_schema_versions ORDER BY schema_uid,schema_version`)
	if err != nil {
		return fmt.Errorf("exporting metadata schema versions: %w", err)
	}
	defer func(current *sql.Rows) { _ = current.Close() }(rows)
	for rows.Next() {
		var storedUID string
		var storedVersion int64
		var raw []byte
		var record metadataSchemaVersionRecord
		record.Type = metadataSchemaVersionType
		if err := rows.Scan(&storedUID, &storedVersion, &raw, &record.CreatedAt); err != nil {
			return err
		}
		var decodeErr error
		record.Schema, decodeErr = decodeStoredMetadataSchema(raw)
		if decodeErr != nil {
			return decodeErr
		}
		if record.Schema.UID != storedUID || record.Schema.Version != storedVersion {
			return errors.New("stored metadata schema identity does not match its key")
		}
		if err := validateMetadataTime("metadata schema created_at", record.CreatedAt); err != nil {
			return err
		}
		if err := write(record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = q.QueryContext(ctx, `SELECT
		document_uid,content_version_id,schema_uid,schema_version,field_key,
		value_json,lane,accepted,producer,source_pointer,captured_at,revision
		FROM metadata_values
		ORDER BY document_uid,content_version_id,schema_uid,schema_version,field_key,lane,revision`)
	if err != nil {
		return fmt.Errorf("exporting metadata values: %w", err)
	}
	defer func(current *sql.Rows) { _ = current.Close() }(rows)
	for rows.Next() {
		var accepted int
		record := metadataValueRecord{Type: metadataValueType}
		if err := rows.Scan(&record.Value.DocumentUID, &record.Value.ContentVersionID, &record.Value.SchemaUID,
			&record.Value.SchemaVersion, &record.Value.FieldKey, &record.Value.ValueJSON,
			&record.Value.Lane, &accepted, &record.Value.Producer, &record.Value.SourcePointer,
			&record.Value.CapturedAt, &record.Value.Revision); err != nil {
			return err
		}
		record.Value.Accepted = accepted != 0
		schema, err := metadataSchemaVersionTx(ctx, q, record.Value.SchemaUID, record.Value.SchemaVersion)
		if err != nil {
			return err
		}
		canonical, err := CanonicalMetadataValueRecord(schema, record.Value)
		if err != nil {
			return fmt.Errorf("stored metadata value is invalid: %w", err)
		}
		if !metadataValueRecordsEqual(record.Value, canonical) {
			return errors.New("stored metadata value is not canonical")
		}
		if err := validateMetadataDocumentAuthorityTx(ctx, q, canonical.DocumentUID, canonical.ContentVersionID); err != nil {
			return err
		}
		record.Value = canonical
		if err := write(record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = q.QueryContext(ctx, `SELECT document_uid,content_version_id,raw,captured_at
		FROM metadata_imported_frontmatter ORDER BY document_uid,content_version_id`)
	if err != nil {
		return fmt.Errorf("exporting imported frontmatter: %w", err)
	}
	defer func(current *sql.Rows) { _ = current.Close() }(rows)
	for rows.Next() {
		record := metadataImportedFrontmatterRecord{Type: metadataImportedFrontmatterType}
		if err := rows.Scan(&record.Frontmatter.DocumentUID, &record.Frontmatter.ContentVersionID,
			&record.Frontmatter.Raw, &record.Frontmatter.CapturedAt); err != nil {
			return err
		}
		canonical, err := NewImportedFrontmatterRecord(record.Frontmatter.DocumentUID,
			record.Frontmatter.ContentVersionID, record.Frontmatter.Raw, record.Frontmatter.CapturedAt)
		if err != nil {
			return fmt.Errorf("stored imported frontmatter is invalid: %w", err)
		}
		if err := validateMetadataDocumentAuthorityTx(ctx, q, canonical.DocumentUID, canonical.ContentVersionID); err != nil {
			return err
		}
		record.Frontmatter = canonical
		if err := write(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func importDocumentMetadataAuthorityRecord(
	ctx context.Context, tx *sql.Tx, kind string, raw jsontext.Value,
) error {
	switch kind {
	case metadataSchemaVersionType:
		var record metadataSchemaVersionRecord
		if err := decodeMetadataRecord(raw, &record); err != nil {
			return err
		}
		if record.Type != metadataSchemaVersionType {
			return errors.New("invalid metadata schema record")
		}
		if err := validateMetadataTime("metadata schema created_at", record.CreatedAt); err != nil {
			return err
		}
		canonical, err := document.CanonicalMetadataSchemaJSON(record.Schema)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_schema_versions(
			schema_uid,schema_version,canonical_json,created_at
		) VALUES(?,?,?,?)`, record.Schema.UID, record.Schema.Version, canonical, record.CreatedAt)
		return err
	case metadataValueType:
		var record metadataValueRecord
		if err := decodeMetadataRecord(raw, &record); err != nil {
			return err
		}
		if record.Type != metadataValueType {
			return errors.New("invalid metadata value record")
		}
		schema, err := metadataSchemaVersionTx(ctx, tx, record.Value.SchemaUID, record.Value.SchemaVersion)
		if err != nil {
			return err
		}
		canonical, err := CanonicalMetadataValueRecord(schema, record.Value)
		if err != nil {
			return err
		}
		if !metadataValueRecordsEqual(record.Value, canonical) {
			return errors.New("metadata value record is not canonical")
		}
		if err := validateMetadataDocumentAuthorityTx(ctx, tx, canonical.DocumentUID, canonical.ContentVersionID); err != nil {
			return err
		}
		accepted := 0
		if canonical.Accepted {
			accepted = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_values(
			document_uid,content_version_id,schema_uid,schema_version,field_key,
			value_json,lane,accepted,producer,source_pointer,captured_at,revision
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, canonical.DocumentUID, canonical.ContentVersionID,
			canonical.SchemaUID, canonical.SchemaVersion, canonical.FieldKey, canonical.ValueJSON,
			canonical.Lane, accepted, canonical.Producer, canonical.SourcePointer, canonical.CapturedAt, canonical.Revision)
		return err
	case metadataImportedFrontmatterType:
		var record metadataImportedFrontmatterRecord
		if err := decodeMetadataRecord(raw, &record); err != nil {
			return err
		}
		if record.Type != metadataImportedFrontmatterType {
			return errors.New("invalid imported frontmatter record")
		}
		canonical, err := NewImportedFrontmatterRecord(record.Frontmatter.DocumentUID,
			record.Frontmatter.ContentVersionID, record.Frontmatter.Raw, record.Frontmatter.CapturedAt)
		if err != nil {
			return err
		}
		if err := validateMetadataDocumentAuthorityTx(ctx, tx, canonical.DocumentUID, canonical.ContentVersionID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_imported_frontmatter(
			document_uid,content_version_id,raw,captured_at
		) VALUES(?,?,?,?)`, canonical.DocumentUID, canonical.ContentVersionID, canonical.Raw, canonical.CapturedAt)
		return err
	default:
		return fmt.Errorf("unsupported document metadata type %q", kind)
	}
}

func validateDocumentMetadataAuthorityState(ctx context.Context, q metadataQuerier) error {
	return exportDocumentMetadataAuthority(ctx, q, func(any) error { return nil })
}
