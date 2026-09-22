package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"

	"go.kenn.io/docbank/document"
)

// AppendMetadataSchemaVersion persists one immutable sidecar schema version.
// Replaying the same canonical declaration is idempotent; a changed historical
// version or a non-monotonic addition is rejected.
func (s *Store) AppendMetadataSchemaVersion(ctx context.Context, candidate document.MetadataSchema) error {
	canonical, err := document.CanonicalMetadataSchemaJSON(candidate)
	if err != nil {
		return err
	}
	return s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		history, err := metadataSchemaVersionsTx(ctx, tx, candidate.UID)
		if err != nil {
			return err
		}
		updated, err := AppendMetadataSchemaVersion(history, candidate)
		if err != nil {
			return err
		}
		if len(updated) == len(history) {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_schema_versions(
			schema_uid,schema_version,canonical_json,created_at
		) VALUES(?,?,?,?)`, candidate.UID, candidate.Version, canonical, nowRFC3339())
		if err != nil {
			return fmt.Errorf("storing metadata schema version: %w", err)
		}
		return nil
	})
}

// MetadataSchemaVersions returns the immutable history for one schema UID.
func (s *Store) MetadataSchemaVersions(ctx context.Context, schemaUID string) ([]document.MetadataSchema, error) {
	if err := validateUUIDv4(schemaUID); err != nil {
		return nil, errors.New("metadata schema UID is invalid")
	}
	return metadataSchemaVersionsTx(ctx, s.db, schemaUID)
}

// MetadataSchemas returns every immutable schema version in stable identity
// order. Consumers choose a version explicitly; the store never substitutes a
// newer schema for retained values.
func (s *Store) MetadataSchemas(ctx context.Context) ([]document.MetadataSchema, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT schema_uid,schema_version,canonical_json
		FROM metadata_schema_versions ORDER BY schema_uid,schema_version`)
	if err != nil {
		return nil, fmt.Errorf("listing metadata schemas: %w", err)
	}
	defer func() { _ = rows.Close() }()
	schemas := make([]document.MetadataSchema, 0)
	for rows.Next() {
		var storedUID string
		var storedVersion int64
		var raw []byte
		if err := rows.Scan(&storedUID, &storedVersion, &raw); err != nil {
			return nil, fmt.Errorf("scanning metadata schema: %w", err)
		}
		schema, err := decodeStoredMetadataSchema(raw)
		if err != nil {
			return nil, err
		}
		if schema.UID != storedUID || schema.Version != storedVersion {
			return nil, errors.New("stored metadata schema identity does not match its key")
		}
		schemas = append(schemas, schema)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing metadata schemas: %w", err)
	}
	return schemas, nil
}

// PutMetadataValue persists a canonical typed value bound to one stored schema
// version. The exact provenance/revision record is immutable; callers append a
// newer revision rather than replacing accepted source evidence.
func (s *Store) PutMetadataValue(ctx context.Context, input MetadataValueRecord) (MetadataValueRecord, error) {
	var stored MetadataValueRecord
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		schema, err := metadataSchemaVersionTx(ctx, tx, input.SchemaUID, input.SchemaVersion)
		if err != nil {
			return err
		}
		stored, err = CanonicalMetadataValueRecord(schema, input)
		if err != nil {
			return err
		}
		if err := validateMetadataDocumentAuthorityTx(ctx, tx, stored.DocumentUID, stored.ContentVersionID); err != nil {
			return err
		}
		existing, found, err := metadataValueRecordTx(ctx, tx, stored)
		if err != nil {
			return err
		}
		if found {
			if !metadataValueRecordsEqual(existing, stored) {
				return errors.New("metadata value revision is immutable")
			}
			stored = existing
			return nil
		}
		accepted := 0
		if stored.Accepted {
			accepted = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_values(
			document_uid,content_version_id,schema_uid,schema_version,field_key,
			value_json,lane,accepted,producer,source_pointer,captured_at,revision
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			stored.DocumentUID, stored.ContentVersionID, stored.SchemaUID, stored.SchemaVersion, stored.FieldKey,
			stored.ValueJSON, stored.Lane, accepted, stored.Producer, stored.SourcePointer, stored.CapturedAt, stored.Revision)
		if err != nil {
			return fmt.Errorf("storing metadata value: %w", err)
		}
		return nil
	})
	if err != nil {
		return MetadataValueRecord{}, err
	}
	return cloneMetadataValueRecord(stored), nil
}

// MetadataValues returns every retained document-scoped and version-bound
// value. Callers select active authority with PartitionMetadataValuesForContentVersion
// and ResolveAcceptedMetadataValue; this method never deletes history.
func (s *Store) MetadataValues(ctx context.Context, documentUID string) ([]MetadataValueRecord, error) {
	if err := validateUUIDv4(documentUID); err != nil {
		return nil, errors.New("metadata document UID is invalid")
	}
	if err := validateMetadataDocumentAuthorityTx(ctx, s.db, documentUID, ""); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		document_uid,content_version_id,schema_uid,schema_version,field_key,
		value_json,lane,accepted,producer,source_pointer,captured_at,revision
		FROM metadata_values WHERE document_uid=?
		ORDER BY document_uid,content_version_id,schema_uid,schema_version,field_key,lane,revision`, documentUID)
	if err != nil {
		return nil, fmt.Errorf("listing metadata values: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]MetadataValueRecord, 0)
	for rows.Next() {
		var record MetadataValueRecord
		var accepted int
		if err := rows.Scan(&record.DocumentUID, &record.ContentVersionID, &record.SchemaUID, &record.SchemaVersion,
			&record.FieldKey, &record.ValueJSON, &record.Lane, &accepted, &record.Producer,
			&record.SourcePointer, &record.CapturedAt, &record.Revision); err != nil {
			return nil, fmt.Errorf("scanning metadata value: %w", err)
		}
		record.Accepted = accepted != 0
		schema, err := metadataSchemaVersionTx(ctx, s.db, record.SchemaUID, record.SchemaVersion)
		if err != nil {
			return nil, err
		}
		canonical, err := CanonicalMetadataValueRecord(schema, record)
		if err != nil {
			return nil, fmt.Errorf("stored metadata value is invalid: %w", err)
		}
		if !metadataValueRecordsEqual(record, canonical) {
			return nil, errors.New("stored metadata value is not canonical")
		}
		values = append(values, canonical)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing metadata values: %w", err)
	}
	return values, nil
}

// PutImportedFrontmatter preserves source-owned bytes without parsing them as
// policy or turning them into accepted sidecar authority.
func (s *Store) PutImportedFrontmatter(ctx context.Context, input ImportedFrontmatterRecord) (ImportedFrontmatterRecord, error) {
	stored, err := NewImportedFrontmatterRecord(input.DocumentUID, input.ContentVersionID, input.Raw, input.CapturedAt)
	if err != nil {
		return ImportedFrontmatterRecord{}, err
	}
	err = s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		if err := validateMetadataDocumentAuthorityTx(ctx, tx, stored.DocumentUID, stored.ContentVersionID); err != nil {
			return err
		}
		var existing ImportedFrontmatterRecord
		err := tx.QueryRowContext(ctx, `SELECT document_uid,content_version_id,raw,captured_at
			FROM metadata_imported_frontmatter WHERE document_uid=? AND content_version_id=?`,
			stored.DocumentUID, stored.ContentVersionID).Scan(
			&existing.DocumentUID, &existing.ContentVersionID, &existing.Raw, &existing.CapturedAt)
		if err == nil {
			if !bytes.Equal(existing.Raw, stored.Raw) || existing.CapturedAt != stored.CapturedAt {
				return errors.New("imported frontmatter is immutable")
			}
			stored = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("looking up imported frontmatter: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata_imported_frontmatter(
			document_uid,content_version_id,raw,captured_at
		) VALUES(?,?,?,?)`, stored.DocumentUID, stored.ContentVersionID, stored.Raw, stored.CapturedAt)
		if err != nil {
			return fmt.Errorf("storing imported frontmatter: %w", err)
		}
		return nil
	})
	if err != nil {
		return ImportedFrontmatterRecord{}, err
	}
	stored.Raw = append([]byte(nil), stored.Raw...)
	return stored, nil
}

// ImportedFrontmatter returns the retained source-owned bytes for one exact
// document content version.
func (s *Store) ImportedFrontmatter(
	ctx context.Context, documentUID, contentVersionID string,
) (ImportedFrontmatterRecord, error) {
	if err := validateUUIDv4(documentUID); err != nil || validateUUIDv4(contentVersionID) != nil {
		return ImportedFrontmatterRecord{}, errors.New("imported frontmatter identity is invalid")
	}
	var record ImportedFrontmatterRecord
	err := s.db.QueryRowContext(ctx, `SELECT document_uid,content_version_id,raw,captured_at
		FROM metadata_imported_frontmatter WHERE document_uid=? AND content_version_id=?`,
		documentUID, contentVersionID).Scan(&record.DocumentUID, &record.ContentVersionID, &record.Raw, &record.CapturedAt)
	if err != nil {
		return ImportedFrontmatterRecord{}, err
	}
	return NewImportedFrontmatterRecord(record.DocumentUID, record.ContentVersionID, record.Raw, record.CapturedAt)
}

func metadataSchemaVersionsTx(
	ctx context.Context, q metadataQuerier, schemaUID string,
) ([]document.MetadataSchema, error) {
	rows, err := q.QueryContext(ctx, `SELECT schema_uid,schema_version,canonical_json
		FROM metadata_schema_versions WHERE schema_uid=? ORDER BY schema_version`, schemaUID)
	if err != nil {
		return nil, fmt.Errorf("listing metadata schema versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	history := make([]document.MetadataSchema, 0)
	for rows.Next() {
		var storedUID string
		var storedVersion int64
		var raw []byte
		if err := rows.Scan(&storedUID, &storedVersion, &raw); err != nil {
			return nil, fmt.Errorf("scanning metadata schema version: %w", err)
		}
		schema, err := decodeStoredMetadataSchema(raw)
		if err != nil {
			return nil, err
		}
		if schema.UID != storedUID || schema.Version != storedVersion {
			return nil, errors.New("stored metadata schema identity does not match its key")
		}
		history = append(history, schema)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing metadata schema versions: %w", err)
	}
	return history, nil
}

func metadataSchemaVersionTx(
	ctx context.Context, q metadataQuerier, schemaUID string, schemaVersion int64,
) (document.MetadataSchema, error) {
	if validateUUIDv4(schemaUID) != nil || schemaVersion < 1 {
		return document.MetadataSchema{}, errors.New("metadata schema identity is invalid")
	}
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT canonical_json FROM metadata_schema_versions
		WHERE schema_uid=? AND schema_version=?`, schemaUID, schemaVersion).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return document.MetadataSchema{}, errors.New("metadata schema version is unavailable")
	}
	if err != nil {
		return document.MetadataSchema{}, fmt.Errorf("reading metadata schema version: %w", err)
	}
	schema, err := decodeStoredMetadataSchema(raw)
	if err != nil {
		return document.MetadataSchema{}, err
	}
	if schema.UID != schemaUID || schema.Version != schemaVersion {
		return document.MetadataSchema{}, errors.New("stored metadata schema identity does not match its key")
	}
	return schema, nil
}

func decodeStoredMetadataSchema(raw []byte) (document.MetadataSchema, error) {
	var schema document.MetadataSchema
	if err := json.Unmarshal(raw, &schema, json.RejectUnknownMembers(true)); err != nil {
		return document.MetadataSchema{}, fmt.Errorf("decoding stored metadata schema: %w", err)
	}
	canonical, err := document.CanonicalMetadataSchemaJSON(schema)
	if err != nil {
		return document.MetadataSchema{}, fmt.Errorf("stored metadata schema is invalid: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return document.MetadataSchema{}, errors.New("stored metadata schema is not canonical")
	}
	return cloneMetadataSchema(schema), nil
}

func metadataValueRecordTx(
	ctx context.Context, q metadataQuerier, key MetadataValueRecord,
) (MetadataValueRecord, bool, error) {
	var record MetadataValueRecord
	var accepted int
	err := q.QueryRowContext(ctx, `SELECT
		document_uid,content_version_id,schema_uid,schema_version,field_key,
		value_json,lane,accepted,producer,source_pointer,captured_at,revision
		FROM metadata_values WHERE document_uid=? AND content_version_id=?
		AND schema_uid=? AND schema_version=? AND field_key=? AND lane=? AND revision=?`,
		key.DocumentUID, key.ContentVersionID, key.SchemaUID, key.SchemaVersion, key.FieldKey, key.Lane, key.Revision).Scan(
		&record.DocumentUID, &record.ContentVersionID, &record.SchemaUID, &record.SchemaVersion, &record.FieldKey,
		&record.ValueJSON, &record.Lane, &accepted, &record.Producer, &record.SourcePointer, &record.CapturedAt, &record.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return MetadataValueRecord{}, false, nil
	}
	if err != nil {
		return MetadataValueRecord{}, false, fmt.Errorf("reading metadata value: %w", err)
	}
	record.Accepted = accepted != 0
	return record, true, nil
}

func validateMetadataDocumentAuthorityTx(
	ctx context.Context, q metadataQuerier, documentUID, contentVersionID string,
) error {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM document_identities WHERE document_uid=?)`, documentUID).Scan(&exists); err != nil {
		return fmt.Errorf("checking metadata document identity: %w", err)
	}
	if !exists {
		return ErrDocumentIdentityUnavailable
	}
	if contentVersionID == "" {
		return nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM document_identities i JOIN content_versions v ON v.node_id=i.node_id
		WHERE i.document_uid=? AND v.version_id=?)`, documentUID, contentVersionID).Scan(&exists); err != nil {
		return fmt.Errorf("checking metadata content version: %w", err)
	}
	if !exists {
		return errors.New("metadata content version does not belong to its document")
	}
	return nil
}
