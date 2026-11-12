package store

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTimelineHydrationEmpty(t *testing.T) {
	s := newTestStore(t)
	err := s.withReadTx(t.Context(), func(tx *sql.Tx) error {
		items, err := hydrateTimelineItems(t.Context(), tx, nil, false)
		require.NoError(t, err)
		require.Empty(t, items)
		return nil
	})
	require.NoError(t, err)
}
