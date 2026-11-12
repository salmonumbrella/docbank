package store

import (
	"fmt"
	"testing"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
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
	id := "90000000-0000-4000-8000-000000000001"
	for name, filters := range map[string]query.Filters{
		"document date":       {DocumentDate: &query.DocumentDateFilter{From: "2026-03-10", To: "2026-03-10"}},
		"path":                {Paths: []string{"/synthetic"}},
		"excluded path":       {ExcludePaths: []string{"/synthetic"}},
		"collection":          {CollectionIDs: []string{id}},
		"excluded collection": {ExcludeCollectionIDs: []string{id}},
		"tag":                 {TagIDs: []string{id}},
		"excluded tag":        {ExcludeTagIDs: []string{id}},
		"no tags":             {NoTags: true},
		"person":              {PersonIDs: []string{id}},
		"person role":         {People: []query.PersonFilter{{PersonID: id, Roles: []string{"author"}}}},
		"size minimum":        {SizeMin: 1},
		"size maximum":        {SizeMax: new(int64(1))},
		"mime":                {MIMETypes: []string{"text/plain"}},
		"extension":           {Extensions: []string{"txt"}},
		"duplicates":          {HasDuplicates: true},
	} {
		t.Run(name, func(t *testing.T) {
			q := base
			q.Filters = filters
			scope := TimelineScope{Query: q, Zone: "invalid-zone"}
			_, err := svc.TimelineDays(t.Context(), "owner", scope, date, date)
			require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
			_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 1})
			require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
			_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Cursor: "invalid", Limit: 1})
			require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
		})
	}
	q := base
	q = base
	q.Filters.CollapseDuplicates = true
	require.NoError(t, validatePostgresTimelineScope(q))
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		for _, field := range []string{"name", "path", "modified_at", "size", "media_type", "capture_time", "import_time", "added_time", "document_date"} {
			q.Mode, q.Sort.Field = mode, field
			require.NoError(t, validatePostgresTimelineScope(q), "%s/%s", mode, field)
		}
	}
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

func checkTimelinePlacementTiers(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	offset := -4 * 60 * 60
	cases := []struct {
		name, value string
		precision   document.EventPrecision
		zone        document.EventTimezoneKind
		offset      *int
	}{
		{"exact", "2019-03-03T23:30:00", "second", "offset", &offset},
		{"floating", "2019-03-04T09:30:00", "second", "omitted", nil},
		{"date", "2019-03-04", "date", "omitted", nil},
		{"month", "2019-03", "month", "omitted", nil},
		{"year", "2019", "year", "omitted", nil},
	}
	ids := make(map[string]int64)
	for i, tc := range cases {
		seed := fmt.Sprintf("%02x", i+1)
		node, err := s.CreateFile(ctx, s.RootID(), tc.name+".txt", fakeHash(seed), 1, "text/plain")
		require.NoError(t, err)
		ids[tc.name] = node.ID
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		e := &record.Events[0]
		e.DateValue, e.RawValue, e.Precision, e.TimezoneKind, e.OffsetSeconds = tc.value, tc.value, tc.precision, tc.zone, tc.offset
		e.AxisKey, err = document.EventAxisKey(e.DateValue, e.Precision, e.TimezoneKind, e.OffsetSeconds)
		require.NoError(t, err)
		var hasUTC bool
		e.UTCKey, hasUTC, err = document.EventUTCKey(e.DateValue, e.Precision, e.TimezoneKind, e.OffsetSeconds)
		require.NoError(t, err)
		require.Equal(t, tc.name == "exact", hasUTC)
		e.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, e.SourceKey, e.DateKind, e.DateValue, e.Precision, e.TimezoneKind, e.EvidenceSHA256)
		record.Primaries[0].EventID = e.EventID
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
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
	require.NoError(t, err)
	require.Equal(t, []TimelineDayCount{{Date: "2019-03-04", Count: 3, Floating: 1, DateOnly: 1}}, days.Days)
	require.Equal(t, []TimelineMonthCount{{Month: "2019-03", Coarse: 2}}, days.Months)
	page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, ids["exact"], page.Items[0].NodeID)
	require.Equal(t, ids["floating"], page.Items[1].NodeID)
	require.Equal(t, 210, *page.Items[0].AxisMinute)
	require.Equal(t, 570, *page.Items[1].AxisMinute)
	require.Len(t, page.DateOnly, 1)
	require.Equal(t, ids["date"], page.DateOnly[0].NodeID)
	require.Len(t, page.MonthOnly, 2)
	require.EqualValues(t, days.Days[0].Count, len(page.Items)+len(page.DateOnly))

	// The instant crosses the calendar boundary in Toronto. Floating and
	// date-only claims keep their civil date in either view zone.
	scope.Zone = "America/Toronto"
	page, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, ids["floating"], page.Items[0].NodeID)
	require.Len(t, page.DateOnly, 1)
	require.Equal(t, ids["date"], page.DateOnly[0].NodeID)
	page, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date.AddDays(-1), Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, ids["exact"], page.Items[0].NodeID)
}
