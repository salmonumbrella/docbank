package store

import (
	"fmt"
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/kit/civiltime"
)

func TestTimelineDayBoundsVectors(t *testing.T) {
	for _, tc := range []struct {
		zone, date, start string
		minutes, ticks    int
	}{
		{"America/Santiago", "2026-09-06", "2026-09-06T04:00:00Z", 1380, 23},
		{"America/Havana", "2026-03-08", "2026-03-08T05:00:00Z", 1380, 23},
		{"America/Toronto", "2026-03-08", "2026-03-08T05:00:00Z", 1380, 23},
		{"America/Toronto", "2026-11-01", "2026-11-01T04:00:00Z", 1500, 25},
		{"America/St_Johns", "2010-11-07", "2010-11-07T02:30:00Z", 1500, 26},
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			loc, err := civiltime.LoadZone(tc.zone)
			require.NoError(t, err)
			date, err := civiltime.ParseDate(tc.date)
			require.NoError(t, err)
			bounds, ok := timelineDayBounds(date, loc)
			require.True(t, ok)
			require.Equal(t, tc.start, bounds.Start.Format("2006-01-02T15:04:05Z"))
			require.Equal(t, tc.minutes, bounds.Minutes)
			require.Len(t, bounds.Ticks, tc.ticks)
			if tc.zone == "America/Toronto" && tc.date == "2026-11-01" {
				require.Equal(t, TimelineTick{Minute: 60, Label: "01:00 −04:00"}, bounds.Ticks[1])
				require.Equal(t, TimelineTick{Minute: 120, Label: "01:00 −05:00"}, bounds.Ticks[2])
			}
		})
	}
	loc, err := civiltime.LoadZone("Pacific/Apia")
	require.NoError(t, err)
	date, err := civiltime.ParseDate("2011-12-30")
	require.NoError(t, err)
	_, ok := timelineDayBounds(date, loc)
	require.False(t, ok)
}

func TestTimelineDayFrozenAfterTrash(t *testing.T) {
	checkTimelineDayFrozenAfterTrash(t, newTestStore)
}

func checkTimelineDayFrozenAfterTrash(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	for _, seed := range []string{"a1", "a2"} {
		node, err := s.CreateFile(ctx, s.RootID(), seed+".txt", fakeHash(seed), 10, "text/plain")
		require.NoError(t, err)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		event := &record.Events[0]
		event.DateValue, event.RawValue = "2019-03-04T09:30:00", "2019-03-04T09:30:00"
		event.Precision = "second"
		event.AxisKey = "2019-03-04T09:30:00.000000000"
		event.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, event.SourceKey, event.DateKind, event.DateValue, event.Precision, event.TimezoneKind, event.EvidenceSHA256)
		record.Primaries[0].EventID = event.EventID
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
	}
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 1}
	first, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	_, _, err = s.Trash(ctx, first.Items[0].NodeID, UnconditionalRev)
	require.NoError(t, err)
	req.Cursor = first.NextCursor
	for name, change := range map[string]func(*TimelineDayRequest){
		"limit":    func(r *TimelineDayRequest) { r.Limit = 2 },
		"date":     func(r *TimelineDayRequest) { r.Date = r.Date.AddDays(1) },
		"kinds":    func(r *TimelineDayRequest) { r.Scope.DateKinds = []string{"created"} },
		"hidden":   func(r *TimelineDayRequest) { r.HiddenLocked = true },
		"tampered": func(r *TimelineDayRequest) { r.Cursor += "x" },
	} {
		t.Run(name, func(t *testing.T) {
			altered := req
			change(&altered)
			_, err := svc.TimelineDay(t.Context(), "owner", altered)
			require.ErrorIs(t, err, ErrTimelineCursorMismatch)
		})
	}
	// A newly configured default cannot move a continuation to another zone.
	defaulted := req
	defaulted.Scope.Zone = "America/Toronto"
	defaulted.Scope.Defaulted = true
	frozen, err := svc.TimelineDay(ctx, "owner", defaulted)
	require.NoError(t, err)
	require.Equal(t, "UTC", frozen.Zone)
	require.Equal(t, int64(2), svc.stats().CachedRows)
	require.Positive(t, svc.stats().CachedBytes)
	second, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].NodeID, second.Items[0].NodeID)
	require.Empty(t, second.NextCursor)
	_, err = svc.TimelineDay(ctx, "other", req)
	require.ErrorIs(t, err, ErrTimelineCursorMismatch)
	req.Scope.Zone = "America/Toronto"
	_, err = svc.TimelineDay(ctx, "owner", req)
	require.ErrorIs(t, err, ErrTimelineCursorMismatch)
	req.Scope.Zone = "UTC"
	svc.Revoke("owner")
	require.Equal(t, snapshotCacheStats{}, svc.stats())
	_, err = svc.TimelineDay(ctx, "owner", req)
	require.ErrorIs(t, err, ErrSnapshotGone)
}

func TestTimelineDayFrozenOwnerAndScope(t *testing.T) {
	s := newTestStore(t)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2026-03-10")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 1}
	page, err := svc.TimelineDay(t.Context(), "owner", req)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Empty(t, page.NextCursor)
	req.Cursor = "not-a-cursor"
	_, err = svc.TimelineDay(t.Context(), "owner", req)
	require.ErrorIs(t, err, ErrTimelineCursorMismatch)
}

// The 201st strip row signals truncation; it is not retained or repeated on
// continuation pages. Cache accounting includes the retained strip rows.
func checkTimelineDayStripAccounting(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	for i := range 203 {
		seed := fmt.Sprintf("%064x", i+1)
		node, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("item-%03d.txt", i), seed, 1, "text/plain")
		require.NoError(t, err)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		if i < 2 {
			event := &record.Events[0]
			event.DateValue, event.RawValue = "2019-03-04T09:30:00", "2019-03-04T09:30:00"
			event.Precision = "second"
			event.AxisKey = "2019-03-04T09:30:00.000000000"
			event.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, event.SourceKey, event.DateKind, event.DateValue, event.Precision, event.TimezoneKind, event.EvidenceSHA256)
			record.Primaries[0].EventID = event.EventID
		}
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
	}
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civiltime.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 1}
	first, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Len(t, first.DateOnly, 200)
	require.True(t, first.DateOnlyTruncated)
	require.EqualValues(t, 202, svc.stats().CachedRows)
	require.Positive(t, svc.stats().CachedBytes)
	req.Cursor = first.NextCursor
	require.NotEmpty(t, req.Cursor)
	second, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Empty(t, second.DateOnly)
	require.False(t, second.DateOnlyTruncated)
	svc.Revoke("owner")
	require.Equal(t, snapshotCacheStats{}, svc.stats())
}

func checkTimelineDayBoundsOperations(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	for _, tc := range []struct {
		zone, date     string
		minutes, ticks int
	}{
		{"America/Santiago", "2026-09-06", 1380, 23},
		{"America/Havana", "2026-03-08", 1380, 23},
		{"America/Toronto", "2026-03-08", 1380, 23},
		{"America/Toronto", "2026-11-01", 1500, 25},
		{"America/St_Johns", "2010-11-07", 1500, 26},
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			date, err := civiltime.ParseDate(tc.date)
			require.NoError(t, err)
			scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: tc.zone}
			page, err := svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, tc.minutes, page.Bounds.Minutes)
			require.Len(t, page.Bounds.Ticks, tc.ticks)
		})
	}
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "Pacific/Apia"}
	skipped, err := civiltime.ParseDate("2011-12-30")
	require.NoError(t, err)
	_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: skipped, Limit: 10})
	require.ErrorIs(t, err, civiltime.ErrDateNotInZone)
	_, err = svc.TimelineDays(t.Context(), "owner", scope, skipped, skipped)
	require.ErrorIs(t, err, civiltime.ErrDateNotInZone)
	_, err = svc.TimelineDays(t.Context(), "owner", scope, skipped.AddDays(-1), skipped.AddDays(1))
	require.NoError(t, err)
}
