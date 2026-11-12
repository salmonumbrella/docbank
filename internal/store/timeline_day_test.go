package store

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
	_ "time/tzdata"

	"cloud.google.com/go/civil"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
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
			loc, err := query.LoadZone(tc.zone)
			require.NoError(t, err)
			date, err := civil.ParseDate(tc.date)
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
	loc, err := query.LoadZone("Pacific/Apia")
	require.NoError(t, err)
	date, err := civil.ParseDate("2011-12-30")
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
	var nodes []Node
	for _, seed := range []string{"a1", "a2"} {
		node, err := s.CreateFile(ctx, s.RootID(), seed+".txt", fakeHash(seed), 10, "text/plain")
		require.NoError(t, err)
		nodes = append(nodes, node)
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
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 1}
	var saved SavedQuery
	if s.postgresSchema == "" {
		saved, err = s.CreateSavedQuery(ctx, "Frozen scope", "", SavedQueryKindQuery, []byte(`{}`))
		require.NoError(t, err)
		req.Scope.Query.Syntax = "advanced"
		req.Scope.Query.Text = `saved:"Frozen scope"`
	}
	first, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	if s.postgresSchema == "" {
		payload := []byte(`{"filters":{"size_min":100}}`)
		_, err = s.UpdateSavedQuery(ctx, saved.ID, saved.Revision, SavedQueryPatch{Payload: &payload})
		require.NoError(t, err)
	}
	person, err := s.CreatePerson(ctx, "Newly assigned", "operator")
	require.NoError(t, err)
	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: nodes[1].CurrentVersionID, PersonID: person.PersonID, Role: "author", Action: "assert", Revision: 1})
	require.NoError(t, err)
	_, _, err = s.Trash(ctx, first.Items[0].NodeID, UnconditionalRev)
	require.NoError(t, err)
	_, _, err = s.Move(ctx, nodes[1].ID, s.RootID(), "renamed.txt", nodes[1].Revision)
	require.NoError(t, err)
	changedFingerprint := fakeHash("a3")
	_, err = s.BumpDocumentEventInputEpoch(ctx, changedFingerprint)
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, nodes[1].CurrentVersionID)
	changed := documentEventRecord(t, s.VaultID(), nodes[1].CurrentVersionID, "a2")
	changed.Events[0].DateValue, changed.Events[0].RawValue = "2019-03-05", "2019-03-05"
	changed.Events[0].AxisKey = "2019-03-05T00:00:00.000000000"
	changed.Events[0].EventID = document.DocumentEventID(changed.VaultUID, changed.ContentVersionID, changed.Events[0].SourceKey, changed.Events[0].DateKind, changed.Events[0].DateValue, changed.Events[0].Precision, changed.Events[0].TimezoneKind, changed.Events[0].EvidenceSHA256)
	changed.Primaries[0].EventID = changed.Events[0].EventID
	primary, ok := document.SelectPrimaryEvent(changed.Events, changed.DocumentKind, "vault", "safe")
	require.True(t, ok)
	changed.Primaries = append(changed.Primaries, primary)
	_, err = s.PublishDocumentEvents(ctx, target, changedFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, changed))
	require.NoError(t, err)
	req.Cursor = first.NextCursor
	for name, change := range map[string]func(*TimelineDayRequest){
		"limit":    func(r *TimelineDayRequest) { r.Limit = 2 },
		"query":    func(r *TimelineDayRequest) { r.Scope.Query.Mode = "semantic" },
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
	require.Equal(t, "a2.txt", second.Items[0].Name)
	require.Equal(t, "/a2.txt", second.Items[0].Path)
	require.Equal(t, "2019-03-04T09:30:00", second.Items[0].Event.DateValue)
	require.Empty(t, second.Items[0].People)
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
	date, err := civil.ParseDate("2026-03-10")
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
	for i := range 404 {
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
		} else if i >= 203 {
			event := &record.Events[0]
			event.DateValue, event.RawValue, event.Precision = "2019-03", "2019-03", "month"
			event.AxisKey = "2019-03-01T00:00:00.000000000"
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
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 1}
	first, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Len(t, first.DateOnly, 200)
	require.True(t, first.DateOnlyTruncated)
	require.Len(t, first.MonthOnly, 200)
	require.True(t, first.MonthOnlyTruncated)
	require.EqualValues(t, 402, svc.stats().CachedRows)
	require.Positive(t, svc.stats().CachedBytes)
	req.Cursor = first.NextCursor
	require.NotEmpty(t, req.Cursor)
	second, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Empty(t, second.DateOnly)
	require.False(t, second.DateOnlyTruncated)
	require.Empty(t, second.MonthOnly)
	require.False(t, second.MonthOnlyTruncated)
	svc.Revoke("owner")
	require.Equal(t, snapshotCacheStats{}, svc.stats())

	for name, limits := range map[string]snapshotCacheLimits{
		"rows":  {MaxRows: 401},
		"bytes": {MaxBytes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			limited := newQuerySnapshotService(s, querySnapshotServiceOptions{Limits: limits})
			t.Cleanup(func() { require.NoError(t, limited.Close()) })
			firstRequest := req
			firstRequest.Cursor = ""
			_, err := limited.TimelineDay(t.Context(), "owner", firstRequest)
			require.ErrorIs(t, err, ErrSnapshotAdmission)
			require.Equal(t, snapshotCacheStats{}, limited.stats())
		})
	}
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
			date, err := civil.ParseDate(tc.date)
			require.NoError(t, err)
			scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: tc.zone}
			page, err := svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, tc.minutes, page.Bounds.Minutes)
			require.Len(t, page.Bounds.Ticks, tc.ticks)
		})
	}
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "Pacific/Apia"}
	skipped, err := civil.ParseDate("2011-12-30")
	require.NoError(t, err)
	_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: skipped, Limit: 10})
	require.ErrorIs(t, err, ErrTimelineDateNotInZone)
	_, err = svc.TimelineDays(t.Context(), "owner", scope, skipped, skipped)
	require.ErrorIs(t, err, ErrTimelineDateNotInZone)
	_, err = svc.TimelineDays(t.Context(), "owner", scope, skipped.AddDays(-1), skipped.AddDays(1))
	require.NoError(t, err)
}

// This is deliberately an upstream Linux CI measurement, never a local benchmark.
func TestTimelineDayScaleCI(t *testing.T) {
	if os.Getenv("CI") != "true" || runtime.GOOS != "linux" {
		t.Skip("synthetic scale measurement runs only on Linux CI")
	}
	s := newTestStore(t)
	ctx := t.Context()
	expected := make([]int64, 0, 5000)
	for i := range 25500 {
		seed := fmt.Sprintf("%064x", i+1)
		node, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("scale-%05d.txt", i), seed, 1, "text/plain")
		require.NoError(t, err)
		if i < 5000 {
			expected = append(expected, node.ID)
		}
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		event := &record.Events[0]
		switch {
		case i < 5000:
			event.DateValue, event.RawValue, event.Precision = "2019-03-04T09:30:00", "2019-03-04T09:30:00", "second"
			event.AxisKey = "2019-03-04T09:30:00.000000000"
		case i < 25000:
			event.DateValue, event.RawValue, event.AxisKey = "2019-03-05", "2019-03-05", "2019-03-05T00:00:00.000000000"
		default:
			record.DocumentKind = "calendar"
			event.DateKind = "started"
			event.DateValue, event.RawValue, event.AxisKey = "2019-03-01", "2019-03-01", "2019-03-01T00:00:00.000000000"
			event.SourceKey = "metadata/scale/calendar.event.e000001.start/0"
		}
		event.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, event.SourceKey, event.DateKind, event.DateValue, event.Precision, event.TimezoneKind, event.EvidenceSHA256)
		if i >= 25000 {
			end := *event
			end.DateKind, end.DateValue, end.RawValue, end.AxisKey = "ended", "2019-03-06", "2019-03-06", "2019-03-06T00:00:00.000000000"
			end.SourceKey = "metadata/scale/calendar.event.e000001.end/1"
			end.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, end.SourceKey, end.DateKind, end.DateValue, end.Precision, end.TimezoneKind, end.EvidenceSHA256)
			record.Events = append(record.Events, end)
		}
		record.Primaries = nil
		for _, disclosure := range []string{"full", "safe"} {
			primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", disclosure)
			require.True(t, ok)
			record.Primaries = append(record.Primaries, primary)
		}
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
	}
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 500}
	for pageNumber := range 10 {
		started := time.Now()
		page, err := svc.TimelineDay(ctx, "owner", req)
		t.Logf("timeline page %d: %s", pageNumber+1, time.Since(started))
		require.NoError(t, err)
		require.Len(t, page.Items, 500)
		for i, item := range page.Items {
			require.Equal(t, expected[pageNumber*500+i], item.NodeID)
		}
		req.Cursor = page.NextCursor
		if pageNumber < 9 {
			require.NotEmpty(t, req.Cursor)
		} else {
			require.Empty(t, req.Cursor)
		}
	}
}
