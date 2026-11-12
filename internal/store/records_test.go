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
