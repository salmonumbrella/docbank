package store

import (
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/kit/civiltime"
)

func TestTimelineDaysUndatedAndPending(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "synthetic.txt", fakeHash("a1"), 10, "text/plain")
	require.NoError(t, err)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
	require.ErrorIs(t, err, ErrDocumentDatesPending)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "d1")
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
	require.NoError(t, err)
	require.Len(t, days.Days, 1)
	require.EqualValues(t, 1, days.Days[0].Count)
	require.Zero(t, days.PendingVersions)
	require.Zero(t, days.Undated)
}
