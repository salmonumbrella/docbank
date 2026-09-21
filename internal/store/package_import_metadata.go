package store

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"go.kenn.io/docbank/internal/canonical"
)

const (
	metadataPackageRecordType        = "package_record"
	metadataPackageLabelType         = "package_label"
	metadataPackageImportReceiptType = "package_import_receipt"
	metadataPackageImportHeadType    = "package_import_head"
)

type metadataPackageImportRow struct {
	Type          string         `json:"type"`
	CanonicalJSON jsontext.Value `json:"canonical_json"`
	Checksum      string         `json:"checksum"`
}

type packageImportHeadRow struct {
	PackageID string `json:"package_id"`
	RecordKey string `json:"record_key"`
	ReceiptID string `json:"receipt_id"`
}

func writePackageImportMetadataRow(write metadataWrite, kind string, value any) error {
	encoded, checksum, err := snapshotRowBytes(value)
	if err != nil {
		return err
	}
	return write(metadataPackageImportRow{Type: kind, CanonicalJSON: encoded, Checksum: checksum})
}

func exportPackageImportMetadata(ctx context.Context, q metadataQuerier, write metadataWrite) error {
	records, err := q.QueryContext(ctx, `SELECT package_id,row_id,load_file,row_ordinal,occurrence_id,
		raw_json,raw_sha256,sensitive FROM package_records ORDER BY package_id,row_id`)
	if err != nil {
		return err
	}
	defer func() { _ = records.Close() }()
	for records.Next() {
		var record PackageRecordRow
		if err := records.Scan(&record.PackageID, &record.RowID, &record.LoadFile, &record.RowOrdinal,
			&record.OccurrenceID, &record.RawJSON, &record.RawSHA256, &record.Sensitive); err != nil {
			_ = records.Close()
			return err
		}
		if err := writePackageImportMetadataRow(write, metadataPackageRecordType, record); err != nil {
			_ = records.Close()
			return err
		}
	}
	if err := records.Err(); err != nil {
		_ = records.Close()
		return err
	}
	if err := records.Close(); err != nil {
		return err
	}
	labels, err := q.QueryContext(ctx, `SELECT package_id,provenance,label_set,label,label_sort_key,
		occurrence_id,content_version_id,COALESCE(artifact_id,''),COALESCE(page_number,0),page_state,endpoint
		FROM package_labels ORDER BY package_id,provenance,label_set,label,endpoint,occurrence_id`)
	if err != nil {
		return err
	}
	defer func() { _ = labels.Close() }()
	for labels.Next() {
		var label PackageLabelRow
		if err := labels.Scan(&label.PackageID, &label.Provenance, &label.LabelSet, &label.Label,
			&label.LabelSortKey, &label.OccurrenceID, &label.ContentVersionID, &label.ArtifactID,
			&label.PageNumber, &label.PageState, &label.Endpoint); err != nil {
			_ = labels.Close()
			return err
		}
		if err := writePackageImportMetadataRow(write, metadataPackageLabelType, label); err != nil {
			_ = labels.Close()
			return err
		}
	}
	if err := labels.Err(); err != nil {
		_ = labels.Close()
		return err
	}
	if err := labels.Close(); err != nil {
		return err
	}
	receipts, err := q.QueryContext(ctx, `SELECT receipt_id,package_id,record_key,occurrence_id,
		COALESCE(content_version_id,''),state,receipt_json,recorded_at
		FROM package_import_receipts ORDER BY receipt_id`)
	if err != nil {
		return err
	}
	defer func() { _ = receipts.Close() }()
	for receipts.Next() {
		var receipt PackageImportReceipt
		if err := receipts.Scan(&receipt.ReceiptID, &receipt.PackageID, &receipt.RecordKey,
			&receipt.OccurrenceID, &receipt.ContentVersionID, &receipt.State, &receipt.ReceiptJSON,
			&receipt.RecordedAt); err != nil {
			_ = receipts.Close()
			return err
		}
		if err := writePackageImportMetadataRow(write, metadataPackageImportReceiptType, receipt); err != nil {
			_ = receipts.Close()
			return err
		}
	}
	if err := receipts.Err(); err != nil {
		_ = receipts.Close()
		return err
	}
	if err := receipts.Close(); err != nil {
		return err
	}
	heads, err := q.QueryContext(ctx, `SELECT package_id,record_key,receipt_id FROM package_import_heads
		ORDER BY package_id,record_key`)
	if err != nil {
		return err
	}
	defer func() { _ = heads.Close() }()
	for heads.Next() {
		var head packageImportHeadRow
		if err := heads.Scan(&head.PackageID, &head.RecordKey, &head.ReceiptID); err != nil {
			_ = heads.Close()
			return err
		}
		if err := writePackageImportMetadataRow(write, metadataPackageImportHeadType, head); err != nil {
			_ = heads.Close()
			return err
		}
	}
	if err := heads.Err(); err != nil {
		_ = heads.Close()
		return err
	}
	return heads.Close()
}

func importPackageImportMetadata(ctx context.Context, tx *sql.Tx, kind string, raw jsontext.Value) error {
	var row metadataPackageImportRow
	if err := decodeMetadataRecord(raw, &row); err != nil {
		return err
	}
	if row.Type != kind || pageChecksum(row.CanonicalJSON) != row.Checksum {
		return ErrPackageConflict
	}
	switch kind {
	case metadataPackageRecordType:
		record, err := canonical.Decode[PackageRecordRow](row.CanonicalJSON)
		if err != nil || validatePackageRecord(record) != nil {
			return ErrPackageConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO package_records(package_id,row_id,load_file,row_ordinal,
			occurrence_id,raw_json,raw_sha256,sensitive) VALUES(?,?,?,?,?,?,?,?)`, record.PackageID,
			record.RowID, record.LoadFile, record.RowOrdinal, record.OccurrenceID, record.RawJSON,
			record.RawSHA256, record.Sensitive)
		return err
	case metadataPackageLabelType:
		label, err := canonical.Decode[PackageLabelRow](row.CanonicalJSON)
		if err != nil || validatePackageLabel(label) != nil {
			return ErrPackageConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO package_labels(package_id,provenance,label_set,label,
			label_sort_key,occurrence_id,content_version_id,artifact_id,page_number,page_state,endpoint)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, label.PackageID, label.Provenance, label.LabelSet, label.Label,
			label.LabelSortKey, label.OccurrenceID, label.ContentVersionID, packageNullable(label.ArtifactID),
			packagePageNullable(label.PageNumber), label.PageState, label.Endpoint)
		return err
	case metadataPackageImportReceiptType:
		receipt, err := canonical.Decode[PackageImportReceipt](row.CanonicalJSON)
		if err != nil || validatePackageImportReceipt(receipt) != nil || receipt.RecordedAt == "" {
			return ErrPackageConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO package_import_receipts(receipt_id,package_id,record_key,
			occurrence_id,content_version_id,state,receipt_json,recorded_at) VALUES(?,?,?,?,?,?,?,?)`,
			receipt.ReceiptID, receipt.PackageID, receipt.RecordKey, receipt.OccurrenceID,
			packageNullable(receipt.ContentVersionID), receipt.State, receipt.ReceiptJSON, receipt.RecordedAt)
		return err
	case metadataPackageImportHeadType:
		head, err := canonical.Decode[packageImportHeadRow](row.CanonicalJSON)
		if err != nil || validateUUIDv4(head.PackageID) != nil || !canonical.IsSHA256Hex(head.RecordKey) ||
			validateUUIDv4(head.ReceiptID) != nil {
			return ErrPackageConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO package_import_heads(package_id,record_key,receipt_id)
			VALUES(?,?,?)`, head.PackageID, head.RecordKey, head.ReceiptID)
		return err
	default:
		return errors.New("unknown package import authority record")
	}
}

func validatePackageImportMetadataState(ctx context.Context, q metadataQuerier) error {
	if err := exportPackageImportMetadata(ctx, q, func(any) error { return nil }); err != nil {
		return err
	}
	var invalid bool
	queries := []string{
		`SELECT EXISTS(SELECT 1 FROM package_records r JOIN packages p ON p.package_id=r.package_id
			WHERE p.direction<>'received' OR p.state='purged')`,
		`SELECT EXISTS(SELECT 1 FROM package_labels l LEFT JOIN package_records r
			ON r.package_id=l.package_id AND r.occurrence_id=l.occurrence_id
			WHERE l.provenance='received' AND (r.row_id IS NULL OR l.label_sort_key<>l.label))`,
		`SELECT EXISTS(SELECT 1 FROM package_import_receipts r LEFT JOIN package_records p
			ON p.package_id=r.package_id AND p.row_id=r.record_key
			WHERE r.state='committed' AND (p.row_id IS NULL OR p.occurrence_id<>r.occurrence_id))`,
		`SELECT EXISTS(SELECT 1 FROM package_import_heads h JOIN package_import_receipts r ON r.receipt_id=h.receipt_id
			WHERE h.package_id<>r.package_id OR h.record_key<>r.record_key)`,
		`SELECT EXISTS(SELECT 1 FROM package_import_receipts r JOIN packages p ON p.package_id=r.package_id
			WHERE r.state='committed' AND NOT EXISTS (
				SELECT 1 FROM provenance_version_bindings b JOIN provenance v ON v.identity=b.provenance_identity
				WHERE b.content_version_id=r.content_version_id AND v.ingest_id=p.ingest_id))`,
		`SELECT EXISTS(SELECT 1 FROM package_labels l JOIN package_import_receipts r
			ON r.package_id=l.package_id AND r.occurrence_id=l.occurrence_id
			WHERE l.provenance='received' AND r.state='committed' AND l.content_version_id<>r.content_version_id)`,
	}
	for _, query := range queries {
		if err := q.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return ErrPackageConflict
		}
	}
	// Portable receipts are identity claims, not a queue. Every head must resolve.
	var heads, resolved int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM package_import_heads`).Scan(&heads); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM package_import_heads h
		JOIN package_import_receipts r ON r.receipt_id=h.receipt_id`).Scan(&resolved); err != nil {
		return err
	}
	if heads != resolved {
		return fmt.Errorf("%w: missing package receipt head", ErrPackageConflict)
	}
	return nil
}
