package store

import (
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
	_, err = s.CreateRecord(ctx, "contract", recordtype.Record{Title: "Another", Status: "draft"})
	require.ErrorIs(t, err, ErrRecordTypeInactive)
}

func TestRecordLifecycle(t *testing.T) { checkRecordLifecycle(t, newTestStore) }

func TestRecordListRejectsInvalidSelectors(t *testing.T) {
	s := newTestStore(t)
	for _, opts := range []RecordListOptions{
		{TypeID: "contract", AsOf: "2026-02-30", Limit: 100},
		{TypeID: "contract", AsOf: "0000-01-01", Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Status: "not_a_status", Limit: 100},
		{TypeID: "contract", AsOf: "2026-01-01", Fields: map[string]any{"not_a_field": "x"}, Limit: 100},
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
		{Title: "A", Status: "active", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}, "contract_type": "service"}},
		{Title: "B", Status: "active", Fields: map[string]any{"term": map[string]any{"end": "2026-01-02"}, "contract_type": "service"}},
		{Title: "Éclair %", Status: "draft", Fields: map[string]any{"term": map[string]any{"end": "2026-01-01"}, "contract_type": "lease"}},
	} {
		_, err = s.CreateRecord(ctx, "contract", input)
		require.NoError(t, err)
	}
	page, total, err := s.ListRecords(ctx, RecordListOptions{TypeID: "contract", Status: "expired", AsOf: "2026-01-02", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, page, 1)
	require.Equal(t, "A", page[0].Title)
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Fields: map[string]any{"contract_type": "service"}, Before: map[string]string{"term.end": "2026-01-02"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "A", page[0].Title)
	page, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", Query: "ÉCLAIR %", AsOf: "2026-01-02", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "Éclair %", page[0].Title)
	state, err := s.GetRecordType(ctx, "contract")
	require.NoError(t, err)
	_, err = s.DeactivateRecordType(ctx, "contract", state.Revision)
	require.NoError(t, err)
	_, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10})
	require.NoError(t, err)
	require.Zero(t, total)
	_, total, err = s.ListRecords(ctx, RecordListOptions{TypeID: "contract", AsOf: "2026-01-02", Limit: 10, IncludeInactive: true})
	require.NoError(t, err)
	require.Equal(t, 3, total)
}

func TestRecordListFiltersEffectiveStatusBeforePaging(t *testing.T) {
	checkRecordListFiltersEffectiveStatusBeforePaging(t, newTestStore)
}

func TestRecordFieldStorageUsesDefinitionTypes(t *testing.T) {
	def := recordtype.Definition{Fields: []recordtype.Field{{ID: "any_date", Type: "date"}, {ID: "any_range", Type: "date_range"}, {ID: "any_label", Type: "label"}}}
	require.Equal(t, "value_date", recordValueColumn(recordFieldType(def, "any_date")))
	require.Equal(t, "date_range", recordFieldType(def, "any_range"))
	require.Equal(t, "value_text", recordValueColumn(recordFieldType(def, "any_label")))
}
