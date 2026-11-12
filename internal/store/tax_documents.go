package store

import (
	"context"
	"database/sql"
	"errors"

	"go.kenn.io/docbank/internal/taxrecord"
)

type TaxDocumentLink struct {
	taxrecord.DocumentValues
	LinkID, RecordID, RecordedAt string
	NodeID                       *int64
	RetiredAt                    *string
}
type TaxDocumentInput struct {
	NodeID                    int64
	EventID, ContentVersionID *string
	Role, FormCode, Note      string
}
type TaxDocumentLinkView struct {
	TaxDocumentLink
	Live, Pinned, Intact bool
	Path                 *string
}
type TaxDocumentMutation struct {
	Record   TaxRecord
	Document TaxDocumentLinkView
}

const taxDocumentColumns = `link_id,record_id,event_id,node_id,content_version_id,content_sha256,role,form_code,note,recorded_at,retired_at`

func taxDocumentViews(ctx context.Context, q metadataQuerier, recordID string) ([]TaxDocumentLinkView, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.`+`link_id,d.record_id,d.event_id,d.node_id,d.content_version_id,d.content_sha256,d.role,d.form_code,d.note,d.recorded_at,d.retired_at,`+`CASE WHEN d.retired_at IS NULL AND n.id IS NOT NULL AND n.kind='file' AND n.trashed_at IS NULL THEN 1 ELSE 0 END, CASE WHEN v.version_id IS NOT NULL AND v.node_id=d.node_id AND v.blob_hash=d.content_sha256 THEN 1 ELSE 0 END, CASE WHEN n.id IS NOT NULL AND n.trashed_at IS NULL THEN 1 ELSE 0 END FROM tax_record_documents d LEFT JOIN nodes n ON n.id=d.node_id LEFT JOIN content_versions v ON v.version_id=d.content_version_id WHERE d.record_id=$1 ORDER BY d.link_id`, recordID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TaxDocumentLinkView{}
	paths := []bool{}
	for rows.Next() {
		var d TaxDocumentLinkView
		var live, intact, path int
		if err = rows.Scan(&d.LinkID, &d.RecordID, &d.EventID, &d.NodeID, &d.ContentVersionID, &d.ContentSHA256, &d.Role, &d.FormCode, &d.Note, &d.RecordedAt, &d.RetiredAt, &live, &intact, &path); err != nil {
			return nil, err
		}
		d.Live = live != 0
		d.Pinned = d.ContentVersionID != nil
		d.Intact = intact != 0
		out = append(out, d)
		paths = append(paths, path != 0)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for i := range out {
		if paths[i] {
			p, err := pathOf(ctx, q, *out[i].NodeID)
			if err != nil {
				return nil, err
			}
			out[i].Path = &p
		}
	}
	return out, nil
}
func (s *Store) LinkTaxDocument(ctx context.Context, id string, revision int64, in TaxDocumentInput) (TaxDocumentMutation, error) {
	var result TaxDocumentMutation
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		r, err := getTaxRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return ErrStaleRevision
		}
		if r.RetiredAt != nil {
			return ErrTaxRetired
		}
		n, err := nodeByIDTx(ctx, tx, in.NodeID)
		if err != nil || n.Kind != nodeKindFile || n.TrashedAt != nil {
			return &taxrecord.FieldError{Field: "node_id", Reason: "invalid value"}
		}
		v := taxrecord.DocumentValues{EventID: in.EventID, ContentVersionID: in.ContentVersionID, Role: in.Role, FormCode: in.FormCode, Note: in.Note}
		if in.EventID != nil {
			var parent string
			var voided *string
			err = tx.QueryRowContext(ctx, `SELECT record_id,voided_at FROM tax_events WHERE event_id=$1`, *in.EventID).Scan(&parent, &voided)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (parent != id || voided != nil) {
				return &taxrecord.FieldError{Field: "event_id", Reason: "invalid value"}
			}
			if err != nil {
				return err
			}
		}
		if in.ContentVersionID != nil {
			var owner int64
			err = tx.QueryRowContext(ctx, `SELECT node_id,blob_hash FROM content_versions WHERE version_id=$1`, *in.ContentVersionID).Scan(&owner, &v.ContentSHA256)
			if errors.Is(err, sql.ErrNoRows) || err == nil && owner != n.ID {
				return &taxrecord.FieldError{Field: "content_version_id", Reason: "invalid value"}
			}
			if err != nil {
				return err
			}
		}
		v, err = taxrecord.NormalizeDocument(v)
		if err != nil {
			return err
		}
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tax_record_documents WHERE record_id=$1 AND node_id=$2 AND role=$3 AND retired_at IS NULL AND ((event_id IS NULL AND $4 IS NULL) OR event_id=$4))`, id, n.ID, v.Role, v.EventID).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			return ErrExists
		}
		if err = advanceTaxRevision(&r.Revision); err != nil {
			return err
		}
		r.UpdatedAt = max(nowRFC3339(), r.UpdatedAt)
		linkID, err := newUUIDv4()
		if err != nil {
			return err
		}
		if err = taxInsert(ctx, tx, "tax_record_documents", taxDocumentColumns, []any{linkID, id, v.EventID, n.ID, v.ContentVersionID, v.ContentSHA256, v.Role, v.FormCode, v.Note, r.UpdatedAt, nil}); err != nil {
			return err
		}
		if err = updateTaxEventParent(ctx, tx, r, revision); err != nil {
			return err
		}
		views, err := taxDocumentViews(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, d := range views {
			if d.LinkID == linkID {
				result = TaxDocumentMutation{Record: r, Document: d}
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return TaxDocumentMutation{}, err
	}
	return result, nil
}
func (s *Store) RetireTaxDocument(ctx context.Context, recordID, linkID string, revision int64) (TaxDocumentMutation, error) {
	var result TaxDocumentMutation
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		r, err := getTaxRecord(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return ErrStaleRevision
		}
		if r.RetiredAt != nil {
			return ErrTaxRetired
		}
		var retired *string
		err = tx.QueryRowContext(ctx, `SELECT retired_at FROM tax_record_documents WHERE record_id=$1 AND link_id=$2`, recordID, linkID).Scan(&retired)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if retired != nil {
			return ErrTaxRetired
		}
		if err = advanceTaxRevision(&r.Revision); err != nil {
			return err
		}
		r.UpdatedAt = max(nowRFC3339(), r.UpdatedAt)
		if _, err = tx.ExecContext(ctx, `UPDATE tax_record_documents SET retired_at=$1 WHERE link_id=$2`, r.UpdatedAt, linkID); err != nil {
			return err
		}
		if err = updateTaxEventParent(ctx, tx, r, revision); err != nil {
			return err
		}
		views, err := taxDocumentViews(ctx, tx, recordID)
		if err != nil {
			return err
		}
		for _, d := range views {
			if d.LinkID == linkID {
				result = TaxDocumentMutation{Record: r, Document: d}
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return TaxDocumentMutation{}, err
	}
	return result, nil
}
