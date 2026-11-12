package store

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func checkTaxDocumentLinksLifecycle(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	p, err := s.CreateParty(ctx, PartyInput{Kind: "organization", DisplayName: "Synthetic", Own: true})
	require.NoError(t, err)
	r, err := s.CreateTaxRecord(ctx, TaxRecordInput{FilerPartyID: &p.PartyID, Country: new("ZZ"), TaxKind: new("example"), Kind: "filing", Title: "Example"})
	require.NoError(t, err)
	e, err := s.AdvanceTaxRecord(ctx, r.RecordID, r.Revision, TaxEventInput{Kind: "filed", OccurredOn: "2025-01-01"})
	require.NoError(t, err)
	n, err := s.CreateFile(ctx, s.RootID(), "return.txt", fakeHash("a1"), 1, "text/plain")
	require.NoError(t, err)
	m, err := s.LinkTaxDocument(ctx, r.RecordID, e.Record.Revision, TaxDocumentInput{NodeID: n.ID, Role: "return", ContentVersionID: &n.CurrentVersionID})
	require.NoError(t, err)
	assert.Equal(t, e.Record.Revision+1, m.Record.Revision)
	assert.True(t, m.Document.Live)
	assert.True(t, m.Document.Pinned)
	assert.True(t, m.Document.Intact)
	d, err := s.GetTaxRecordDetail(ctx, r.RecordID)
	require.NoError(t, err)
	assert.Equal(t, "complete", d.Evidence.State)
	_, err = s.LinkTaxDocument(ctx, r.RecordID, m.Record.Revision, TaxDocumentInput{NodeID: n.ID, Role: "return"})
	require.ErrorIs(t, err, ErrExists)
	removed, err := s.RetireTaxDocument(ctx, r.RecordID, m.Document.LinkID, m.Record.Revision)
	require.NoError(t, err)
	_, err = s.RetireTaxDocument(ctx, r.RecordID, m.Document.LinkID, removed.Record.Revision)
	require.ErrorIs(t, err, ErrTaxRetired)
	d, err = s.GetTaxRecordDetail(ctx, r.RecordID)
	require.NoError(t, err)
	assert.Equal(t, "missing", d.Evidence.State)
}
func TestTaxDocumentLinksLifecycle(t *testing.T) { checkTaxDocumentLinksLifecycle(t, newTestStore) }
