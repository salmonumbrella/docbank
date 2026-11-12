package store

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// This receipt exercises the otherwise untested schema-49 source gates.
func TestTaxSchema49PartyOnlySourceReceipt(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	party, err := s.CreateParty(ctx, PartyInput{Kind: "organization", DisplayName: "Example Filer", Own: true})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DROP TABLE tax_records`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DROP TABLE tax_accounts`)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, exportReleasedMetadataSnapshot(ctx, s.db, &out, 49))
	require.Contains(t, out.String(), `"type":"party"`)
	require.Contains(t, out.String(), party.PartyID)
	require.NotContains(t, out.String(), `"type":"tax_account"`)
	require.NotContains(t, out.String(), `"type":"tax_record"`)
}
