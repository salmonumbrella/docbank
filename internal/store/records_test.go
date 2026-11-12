package store

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/recordtype"
)

func checkRecordLifecycle(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	state, err := s.GetRecordType(ctx, "contract")
	require.NoError(t, err)
	require.False(t, state.Active)
	require.Zero(t, state.Revision)
	_, err = s.ActivateRecordType(ctx, "contract", 1)
	require.ErrorIs(t, err, ErrStaleRevision)
	state, err = s.ActivateRecordType(ctx, "contract", 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.Revision)
	r, err := s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Sample agreement", Status: "active", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Revision)
	_, err = s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{})
	require.ErrorIs(t, err, ErrInvalidRecord)
	unchanged, err := s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{Title: &r.Title})
	require.NoError(t, err)
	require.Equal(t, r.Revision, unchanged.Revision)
	r, err = s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{Fields: map[string]any{"term": map[string]any{"start": "2025-01-01"}}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"start": "2025-01-01", "end": "2026-01-01"}, r.Fields["term"])
	r, err = s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{Fields: map[string]any{"term": map[string]any{"end": nil}}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"start": "2025-01-01"}, r.Fields["term"])
	r, err = s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{Fields: map[string]any{"term": nil}})
	require.NoError(t, err)
	require.NotContains(t, r.Fields, "term")
	_, err = s.UpdateRecord(ctx, r.ID, 0, RecordPatch{Title: &r.Title})
	require.ErrorIs(t, err, ErrStaleRevision)
	retired, err := s.RetireRecord(ctx, r.ID, r.Revision)
	require.NoError(t, err)
	require.NotEmpty(t, retired.RetiredAt)
	_, err = s.UpdateRecord(ctx, r.ID, retired.Revision, RecordPatch{Title: &r.Title})
	require.ErrorIs(t, err, ErrRecordRetired)
	state, err = s.DeactivateRecordType(ctx, "contract", state.Revision)
	require.NoError(t, err)
	require.False(t, state.Active)
	_, err = s.UpdateRecord(ctx, r.ID, r.Revision, RecordPatch{Title: &r.Title})
	require.ErrorIs(t, err, ErrRecordTypeInactive)
	_, err = s.RetireRecord(ctx, r.ID, r.Revision)
	require.ErrorIs(t, err, ErrRecordTypeInactive)
	_, err = s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Another", Status: "draft"})
	require.ErrorIs(t, err, ErrRecordTypeInactive)
}

func TestRecordListRejectsInvalidSelectors(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	for _, opts := range []RecordListOptions{
		{TypeID: "contract", AsOf: "2026-02-30", Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Status: "not_a_status", Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Fields: map[string]any{"not_a_field": "x"}, Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Fields: map[string]any{"summary": "x"}, Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Fields: map[string]any{"signed_on": "2026-01-01"}, Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}}, Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Before: map[string]string{"term.end": "2026-02-30"}, Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Before: map[string]string{"summary": "2026-01-01"}, Limit: 100},
	} {
		_, _, err := s.ListRecords(t.Context(), opts)
		require.ErrorIs(t, err, ErrInvalidRecord)
	}
}

func checkRecordListFiltersEffectiveStatusBeforePaging(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	_, err := s.ActivateRecordType(ctx, "contract", 0)
	require.NoError(t, err)
	for _, input := range []recordtype.Record{
		{Title: "B", Status: "active", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}, "contract_type": "service"}},
		{Title: "A", Status: "active", Fields: map[string]any{"term": map[string]any{"end": "2026-01-02"}, "contract_type": "service"}},
		{Title: "ÉCLAIR decoy", Status: "draft"},
		{Title: "ÉCLAIR %", Status: "draft", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}, "contract_type": "lease"}},
	} {
		_, err = s.CreateRecord(ctx, "contract", input)
		require.NoError(t, err)
	}
	page, total, err := s.ListRecords(ctx, RecordListOptions{TypeID: "contract", Status: "expired", AsOf: "2026-01-02", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, page, 1)
	require.Equal(t, "B", page[0].Title)
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Fields: map[string]any{"contract_type": "service"}, Before: map[string]string{"term.end": "2026-01-02"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "B", page[0].Title)
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", Query: "Éclair %", AsOf: "2026-01-02", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "ÉCLAIR %", page[0].Title)
	for _, query := range []string{"bad\x00query", "bad\nquery"} {
		_, _, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", Query: query, AsOf: "2026-01-02", Limit: 10})
		require.ErrorIs(t, err, ErrInvalidRecord)
	}
	for _, tc := range []struct {
		opts         RecordListOptions
		field, value string
	}{
		{RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10, Fields: map[string]any{"contract_type": "Bad secret"}}, "contract_type", "Bad secret"},
		{RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10, Before: map[string]string{"term.end": "secret date"}}, "term.end", "secret date"},
	} {
		_, _, err = s.ListRecords(ctx, tc.opts)
		require.ErrorIs(t, err, ErrInvalidRecord)
		require.ErrorContains(t, err, tc.field)
		require.NotContains(t, err.Error(), tc.value)
	}
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", After: map[string]string{"term.end": "2026-01-01"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "A", page[0].Title)
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, 4, total)
	require.Equal(t, "B", page[0].Title)
	state, err := s.GetRecordType(ctx, "contract")
	require.NoError(t, err)
	_, err = s.DeactivateRecordType(ctx, "contract", state.Revision)
	require.NoError(t, err)
	_, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10})
	require.NoError(t, err)
	require.Zero(t, total)
	_, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10, IncludeInactive: true})
	require.NoError(t, err)
	require.Equal(t, 4, total)
}

// checkRecordLongTextBackend exercises incompressible values on each backend.
func checkRecordLongTextBackend(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	_, err := s.ActivateRecordType(t.Context(), "contract", 0)
	require.NoError(t, err)
	var text strings.Builder
	for i := range 100 {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		fmt.Fprintf(&text, "%x", sum)
	}
	record, err := s.CreateRecord(t.Context(), "contract", recordtype.Record{Title: "Long text", Status: "draft", Fields: map[string]any{"summary": text.String()}})
	require.NoError(t, err)
	got, err := s.GetRecord(t.Context(), record.ID)
	require.NoError(t, err)
	require.Equal(t, text.String(), got.Fields["summary"])
}

func TestRecordRejectsNewerBuiltinVersion(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	state, err := s.ActivateRecordType(ctx, "contract", 0)
	require.NoError(t, err)
	record, err := s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Example", Status: "draft"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE record_types SET version=2 WHERE type_id='contract'`)
	require.NoError(t, err)
	_, err = s.GetRecordType(ctx, "contract")
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.ListRecordTypes(ctx, true)
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, _, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-01", Limit: 10, IncludeInactive: true})
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.GetRecord(ctx, record.ID)
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Other", Status: "draft"})
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.UpdateRecord(ctx, record.ID, record.Revision, RecordPatch{Title: &record.Title})
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.RetireRecord(ctx, record.ID, record.Revision)
	require.ErrorIs(t, err, ErrInvalidRecord)
	_, err = s.DeactivateRecordType(ctx, "contract", state.Revision)
	require.ErrorIs(t, err, ErrInvalidRecord)
	var version, revision int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT version,revision FROM record_types WHERE type_id='contract'`).Scan(&version, &revision))
	require.Equal(t, 2, version)
	require.Equal(t, 1, revision)
}
