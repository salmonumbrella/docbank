package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"go.kenn.io/docbank/internal/recordtype"
)

var (
	ErrRecordTypeInactive = errors.New("record type is inactive")
	ErrRecordRetired      = errors.New("record is retired")
	ErrInvalidRecord      = errors.New("invalid record")
)

type RecordTypeState struct {
	Definition           recordtype.Definition
	Active               bool
	Revision             int64
	CreatedAt, UpdatedAt string
}
type Record struct {
	ID, TypeID, Title, Note, Status, EffectiveStatus string
	Fields                                           map[string]any
	Revision                                         int64
	CreatedAt, UpdatedAt                             string
	RetiredAt                                        *string
}
type RecordPatch struct {
	Title, Note, Status *string
	Fields              map[string]any
}
type RecordListOptions struct {
	TypeID, Status, Query, AsOf string
	Fields                      map[string]any
	Before, After               map[string]string
	Limit, Offset               int
}

type recordQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) GetRecordType(ctx context.Context, id string) (RecordTypeState, error) {
	return recordTypeState(ctx, s.db, id, false)
}
func recordTypeState(ctx context.Context, q recordQueryer, id string, lock bool) (RecordTypeState, error) {
	def, ok := recordtype.Builtin(id)
	if !ok {
		return RecordTypeState{}, ErrNotFound
	}
	state := RecordTypeState{Definition: def}
	query := `SELECT active,revision,created_at,updated_at FROM record_types WHERE type_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var active int
	err := q.QueryRowContext(ctx, query, id).Scan(&active, &state.Revision, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return RecordTypeState{}, err
	}
	state.Active = active == 1
	return state, nil
}
func (s *Store) ListRecordTypes(ctx context.Context, includeInactive bool) ([]RecordTypeState, error) {
	var out []RecordTypeState
	for _, def := range recordtype.Builtins() {
		state, err := s.GetRecordType(ctx, def.ID)
		if err != nil {
			return nil, err
		}
		if state.Active || includeInactive {
			out = append(out, state)
		}
	}
	return out, nil
}
func (s *Store) ActivateRecordType(ctx context.Context, id string, revision int64) (RecordTypeState, error) {
	return s.setRecordTypeActive(ctx, id, revision, true)
}
func (s *Store) DeactivateRecordType(ctx context.Context, id string, revision int64) (RecordTypeState, error) {
	return s.setRecordTypeActive(ctx, id, revision, false)
}
func (s *Store) setRecordTypeActive(ctx context.Context, id string, revision int64, active bool) (RecordTypeState, error) {
	var out RecordTypeState
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		current, err := recordTypeState(ctx, tx, id, s.postgresSchema != "")
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		out = current
		if current.Active == active {
			return nil
		}
		if revision == math.MaxInt64 {
			return ErrInvalidRecord
		}
		stamp := nowRFC3339()
		value := 0
		if active {
			value = 1
		}
		if revision == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO record_types(type_id,source,version,definition,fingerprint,active,revision,created_at,updated_at) VALUES($1,$2,$3,NULL,$4,$5,1,$6,$7)`, id, "builtin", current.Definition.Version, "", value, stamp, stamp)
			out.CreatedAt = stamp
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE record_types SET active=$1,revision=$2,updated_at=$3 WHERE type_id=$4 AND revision=$5`, value, revision+1, stamp, id, revision)
			if err == nil {
				var n int64
				n, err = result.RowsAffected()
				if err == nil && n != 1 {
					return ErrStaleRevision
				}
			}
		}
		if err != nil {
			if s.isUniqueViolation(err) {
				return ErrStaleRevision
			}
			return err
		}
		out.Active = active
		out.Revision = revision + 1
		out.UpdatedAt = stamp
		return nil
	})
	return out, err
}
func (s *Store) CreateRecord(ctx context.Context, id string, input recordtype.Record) (Record, error) {
	def, ok := recordtype.Builtin(id)
	if !ok {
		return Record{}, ErrNotFound
	}
	normalized, err := recordtype.NormalizeRecord(def, input)
	if err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	uuid, err := newUUIDv4()
	if err != nil {
		return Record{}, err
	}
	stamp := nowRFC3339()
	out := Record{ID: uuid, TypeID: id, Title: normalized.Title, Note: normalized.Note, Status: normalized.Status, Fields: normalized.Fields, Revision: 1, CreatedAt: stamp, UpdatedAt: stamp}
	err = s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		state, e := recordTypeState(ctx, tx, id, s.postgresSchema != "")
		if e != nil {
			return e
		}
		if !state.Active {
			return ErrRecordTypeInactive
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO records(record_id,type_id,title,note,status,revision,created_at,updated_at,retired_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULL)`, out.ID, id, out.Title, out.Note, out.Status, out.Revision, stamp, stamp)
		if e != nil {
			return e
		}
		return writeRecordValues(ctx, tx, out)
	})
	return out, err
}
func readRecord(ctx context.Context, q recordQueryer, id string) (Record, error) {
	var r Record
	err := q.QueryRowContext(ctx, `SELECT record_id,type_id,title,note,status,revision,created_at,updated_at,retired_at FROM records WHERE record_id=$1`, id).Scan(&r.ID, &r.TypeID, &r.Title, &r.Note, &r.Status, &r.Revision, &r.CreatedAt, &r.UpdatedAt, &r.RetiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT field_id,value_text,value_date,value_date_end FROM record_values WHERE record_id=$1 ORDER BY field_id`, id)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = rows.Close() }()
	r.Fields = map[string]any{}
	for rows.Next() {
		var field string
		var text, date, end sql.NullString
		if err := rows.Scan(&field, &text, &date, &end); err != nil {
			return Record{}, err
		}
		if text.Valid {
			r.Fields[field] = text.String
		} else if field == "term" {
			m := map[string]any{}
			if date.Valid {
				m["start"] = date.String
			}
			if end.Valid {
				m["end"] = end.String
			}
			r.Fields[field] = m
		} else if date.Valid {
			r.Fields[field] = date.String
		}
	}
	if err := rows.Err(); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s *Store) GetRecord(ctx context.Context, id string) (Record, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := readRecord(ctx, tx, id)
	if err != nil {
		return Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return r, nil
}
func writeRecordValues(ctx context.Context, tx *sql.Tx, r Record) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_values WHERE record_id=$1`, r.ID); err != nil {
		return err
	}
	for id, value := range r.Fields {
		var text, date, end any
		switch v := value.(type) {
		case string:
			if id == "signed_on" {
				date = v
			} else {
				text = v
			}
		case map[string]any:
			date = v["start"]
			end = v["end"]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO record_values(record_id,type_id,field_id,value_text,value_date,value_date_end) VALUES($1,$2,$3,$4,$5,$6)`, r.ID, r.TypeID, id, text, date, end); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) UpdateRecord(ctx context.Context, id string, revision int64, patch RecordPatch) (Record, error) {
	var out Record
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		current, err := readRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		if current.RetiredAt != nil {
			return ErrRecordRetired
		}
		state, err := recordTypeState(ctx, tx, current.TypeID, s.postgresSchema != "")
		if err != nil {
			return err
		}
		if !state.Active {
			return ErrRecordTypeInactive
		}
		candidate := recordtype.Record{Title: current.Title, Note: current.Note, Status: current.Status, Fields: map[string]any{}}
		for k, v := range current.Fields {
			candidate.Fields[k] = v
		}
		if patch.Title != nil {
			candidate.Title = *patch.Title
		}
		if patch.Note != nil {
			candidate.Note = *patch.Note
		}
		if patch.Status != nil {
			candidate.Status = *patch.Status
		}
		for k, v := range patch.Fields {
			candidate.Fields[k] = v
		}
		normalized, err := recordtype.NormalizeRecord(state.Definition, candidate)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRecord, err)
		}
		out = current
		if normalized.Title == current.Title && normalized.Note == current.Note && normalized.Status == current.Status && reflect.DeepEqual(normalized.Fields, current.Fields) {
			return nil
		}
		if revision == math.MaxInt64 {
			return ErrInvalidRecord
		}
		out.Title = normalized.Title
		out.Note = normalized.Note
		out.Status = normalized.Status
		out.Fields = normalized.Fields
		out.Revision++
		out.UpdatedAt = nowRFC3339()
		result, err := tx.ExecContext(ctx, `UPDATE records SET title=$1,note=$2,status=$3,revision=$4,updated_at=$5 WHERE record_id=$6 AND revision=$7`, out.Title, out.Note, out.Status, out.Revision, out.UpdatedAt, id, revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStaleRevision
		}
		return writeRecordValues(ctx, tx, out)
	})
	return out, err
}
func (s *Store) RetireRecord(ctx context.Context, id string, revision int64) (Record, error) {
	var out Record
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		current, err := readRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		if current.RetiredAt != nil {
			return ErrRecordRetired
		}
		state, err := recordTypeState(ctx, tx, current.TypeID, s.postgresSchema != "")
		if err != nil {
			return err
		}
		if !state.Active {
			return ErrRecordTypeInactive
		}
		if revision == math.MaxInt64 {
			return ErrInvalidRecord
		}
		stamp := nowRFC3339()
		result, err := tx.ExecContext(ctx, `UPDATE records SET retired_at=$1,updated_at=$2,revision=$3 WHERE record_id=$4 AND revision=$5`, stamp, stamp, revision+1, id, revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStaleRevision
		}
		out = current
		out.Revision++
		out.UpdatedAt = stamp
		out.RetiredAt = &stamp
		return nil
	})
	return out, err
}

// ListRecords applies selectors before paging inside one consistent read transaction.
func (s *Store) ListRecords(ctx context.Context, opts RecordListOptions) ([]Record, int, error) {
	def, ok := recordtype.Builtin(opts.TypeID)
	if !ok {
		return nil, 0, ErrNotFound
	}
	if err := validatePage(opts.Limit, opts.Offset); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT record_id FROM records WHERE type_id=$1 AND retired_at IS NULL ORDER BY title,record_id`, opts.TypeID)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, err
	}
	if err = rows.Close(); err != nil {
		return nil, 0, err
	}
	var result []Record
	total := 0
	for _, id := range ids {
		r, e := readRecord(ctx, tx, id)
		if e != nil {
			return nil, 0, e
		}
		r.EffectiveStatus = recordtype.EffectiveStatus(def, r.Status, r.Fields, opts.AsOf)
		if opts.Status != "" && r.EffectiveStatus != opts.Status {
			continue
		}
		if opts.Query != "" && !strings.Contains(asciiLower(r.Title), asciiLower(opts.Query)) {
			continue
		}
		matches := true
		for field, value := range opts.Fields {
			if !reflect.DeepEqual(r.Fields[field], value) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		for field, date := range opts.Before {
			v := recordDate(r.Fields, field)
			if v == "" || v >= date {
				matches = false
				break
			}
		}
		for field, date := range opts.After {
			v := recordDate(r.Fields, field)
			if v == "" || v <= date {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if total >= opts.Offset && len(result) < opts.Limit {
			result = append(result, r)
		}
		total++
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}
func recordDate(fields map[string]any, path string) string {
	parts := strings.Split(path, ".")
	v := fields[parts[0]]
	if len(parts) == 2 {
		m, _ := v.(map[string]any)
		v = m[parts[1]]
	} else if len(parts) != 1 {
		return ""
	}
	s, _ := v.(string)
	return s
}
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
