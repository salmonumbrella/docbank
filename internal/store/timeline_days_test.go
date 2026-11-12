package store

import (
	"fmt"
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/kit/civiltime"
)

func TestTimelineDaysUndatedAndPending(t *testing.T) {
	checkTimelineDaysUndatedAndPending(t, newTestStore)
}
func checkTimelineDaysUndatedAndPending(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
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
	primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
	require.True(t, ok)
	record.Primaries = append(record.Primaries, primary)
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
	require.NoError(t, err)
	require.Len(t, days.Days, 1)
	require.EqualValues(t, 1, days.Days[0].Count)
	require.Zero(t, days.PendingVersions)
	require.Zero(t, days.Undated)
	page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Len(t, page.DateOnly, 1)
	require.Equal(t, node.ID, page.DateOnly[0].NodeID)
	require.Equal(t, "2019-03-04", page.DateOnly[0].Event.DateValue)
	_, _, err = s.Trash(ctx, node.ID, node.Revision)
	require.NoError(t, err)
	days, err = svc.TimelineDays(ctx, "owner", scope, date, date)
	require.NoError(t, err)
	require.Empty(t, days.Days)
	require.Zero(t, days.Undated)
	page, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.DateOnly)
}

func TestTimelineScopeRejectsBeforeDatabase(t *testing.T) {
	base := snapshotTestQuery(t, `{}`)
	store := &Store{postgresSchema: "synthetic"}
	svc := NewQuerySnapshotService(store)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2026-03-10")
	require.NoError(t, err)
	for _, text := range []string{"hello", "person:synthetic", "tag:synthetic", "date:2026-03-10", "saved:synthetic"} {
		t.Run(text, func(t *testing.T) {
			q := base
			q.Text = text
			scope := TimelineScope{Query: q, Zone: "UTC"}
			_, err := svc.TimelineDays(t.Context(), "owner", scope, date, date)
			require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
			_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Cursor: "invalid", Limit: 1})
			require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
		})
	}
	q := base
	q.Filters.Paths = []string{"/synthetic"}
	require.ErrorIs(t, validatePostgresTimelineScope(q), ErrTimelineScopeUnsupported)
	q = base
	q.Filters.CollapseDuplicates = true
	require.NoError(t, validatePostgresTimelineScope(q))
	q.Sort.Field = "relevance"
	require.Error(t, validatePostgresTimelineScope(q))
}

func checkTimelineScopeRejected(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2026-03-10")
	require.NoError(t, err)
	q := snapshotTestQuery(t, `{}`)
	q.Filters.Paths = []string{"/synthetic"}
	scope := TimelineScope{Query: q, Zone: "UTC"}
	_, err = svc.TimelineDays(t.Context(), "owner", scope, date, date)
	require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
	_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Cursor: "invalid", Limit: 1})
	require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
}

// A terminal failed attempt can retain an old generation. Neither that head
// nor its absence is evidence that the current document is undated.
func checkTimelineFreshnessTerminalAttempts(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "terminal.txt", fakeHash("f01"), 1, "text/plain")
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "f02")
	primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
	require.True(t, ok)
	record.Primaries = append(record.Primaries, primary)
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	for i, state := range []string{"failed", "unavailable"} {
		_, err = s.StartDocumentEventRebuild(ctx, fmt.Sprintf("90000000-0000-4000-8000-%012d", i+1), fakeHash(state))
		require.NoError(t, err)
		_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
		require.ErrorIs(t, err, ErrDocumentDatesPending, "a rebuild invalidates even an unfiltered query")
		target = requireDocumentEventTarget(t, s, node.CurrentVersionID)
		require.NoError(t, s.RecordDocumentEventAttempt(ctx, target, requireDocumentEventInputsSHA256(t, s, target), state, []byte(`[]`)))
		days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
		require.NoError(t, err)
		require.Empty(t, days.Days)
		require.Zero(t, days.Undated, state)
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
		require.NoError(t, err)
		require.Empty(t, page.Items)
		require.Empty(t, page.DateOnly)
	}
}
