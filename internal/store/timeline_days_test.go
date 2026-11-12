package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	_ "time/tzdata"

	"cloud.google.com/go/civil"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
)

func publishTimelineSafe(t *testing.T, s *Store, target DocumentEventTarget, record document.DocumentEventsV1) {
	t.Helper()
	primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
	require.True(t, ok)
	record.Primaries = append(record.Primaries, primary)
	_, err := s.PublishDocumentEvents(t.Context(), target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
}

func timelineTestService(t *testing.T, s *Store) *QuerySnapshotService {
	t.Helper()
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func checkTimelineDaysUndatedAndPending(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "synthetic.txt", fakeHash("a1"), 10, "text/plain")
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
	require.ErrorIs(t, err, ErrDocumentDatesPending)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "d1")
	publishTimelineSafe(t, s, target, record)
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
	date, err := civil.ParseDate("2026-03-10")
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
	filterCases := timelineNarrowingFilters()
	for name, filters := range filterCases {
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
	q.Filters.CollapseDuplicates = true
	require.NoError(t, validatePostgresTimelineScope(q))
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		for _, field := range []string{"name", "path", "modified_at", "size", "media_type", "capture_time", "import_time", "added_time", "document_date"} {
			q.Mode, q.Sort.Field = mode, field
			require.NoError(t, validatePostgresTimelineScope(q), "%s/%s", mode, field)
		}
	}
	q.Sort.Field = "relevance"
	require.ErrorContains(t, validatePostgresTimelineScope(q), "relevance sort is not supported by bound queries")
}

func timelineNarrowingFilters() map[string]query.Filters {
	id := "90000000-0000-4000-8000-000000000001"
	filterCases := map[string]query.Filters{
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
		"unevaluated":         {Unevaluated: true},
		"rating min":          {RatingMin: new(int64(1))},
		"rating max":          {RatingMax: new(int64(1))},
		"flags":               {Flags: []string{"pick"}},
		"labels":              {Labels: []string{"red"}},
		"kinds":               {Kinds: []string{"photo"}},
		"cameras":             {Cameras: []string{"camera"}},
		"lenses":              {Lenses: []string{"lens"}},
		"locations":           {Locations: []string{"place"}},
		"iso min":             {ISOMin: new(int64(1))},
		"iso max":             {ISOMax: new(int64(1))},
		"capture after":       {CaptureAfter: "2026-03-10"},
		"capture before":      {CaptureBefore: "2026-03-10"},
		"gps":                 {GPSBounds: &query.GPSBounds{South: "0", West: "0", North: "1", East: "1"}},
		"asset":               {AssetIDs: []string{id}},
		"set":                 {SetIDs: []string{id}},
		"media family":        {MediaFamilies: []string{"document"}},
		"modified after":      {ModifiedAfter: "2026-03-10T00:00:00Z"},
		"modified before":     {ModifiedBefore: "2026-03-10T00:00:00Z"},
		"text coverage":       {TextCoverage: []string{"complete"}},
	}
	// Exercise each quality bound independently so canonicalization cannot hide
	// a facet behind a second nonzero field.
	for _, name := range []string{"FocusMin", "FocusMax", "BlurMin", "BlurMax", "BrightnessMin", "BrightnessMax", "FramingMin", "FramingMax", "AestheticsMin", "AestheticsMax", "ColorRedMin", "ColorRedMax", "ColorGreenMin", "ColorGreenMax", "ColorBlueMin", "ColorBlueMax"} {
		var filters query.Filters
		reflect.ValueOf(&filters).Elem().FieldByName(name).Set(reflect.ValueOf(new("0.5")))
		filterCases[name] = filters
	}
	return filterCases
}

func checkTimelineScopeRejected(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2026-03-10")
	require.NoError(t, err)
	q := snapshotTestQuery(t, `{}`)
	q.Filters = timelineNarrowingFilters()["person"]
	scope := TimelineScope{Query: q, Zone: "UTC"}
	_, err = svc.TimelineDays(t.Context(), "owner", scope, date, date)
	require.ErrorIs(t, err, ErrTimelineScopeUnsupported)
	_, err = svc.TimelineDay(t.Context(), "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 1})
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
	publishTimelineSafe(t, s, target, record)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	for i, state := range []string{"failed", "unavailable"} {
		_, err = s.StartDocumentEventRebuild(ctx, fmt.Sprintf("90000000-0000-4000-8000-%012d", i+1), fakeHash(fmt.Sprintf("f%02x", i+1)))
		require.NoError(t, err)
		_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
		require.ErrorIs(t, err, ErrDocumentDatesPending, "a rebuild invalidates even an unfiltered query")
		_, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
		require.ErrorIs(t, err, ErrDocumentDatesPending)
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
		if tc.name == "exact" {
			// A second eligible day-level claim must not put the same version
			// in both the timed list and the date strip.
			other := *e
			other.DateKind, other.DateValue, other.RawValue = "modified", "2019-03-04", "2019-03-04"
			other.Precision, other.TimezoneKind, other.OffsetSeconds, other.UTCKey = "date", "omitted", nil, ""
			other.AxisKey = "2019-03-04T00:00:00.000000000"
			other.SourceKey += "/modified"
			other.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, other.SourceKey, other.DateKind, other.DateValue, other.Precision, other.TimezoneKind, other.EvidenceSHA256)
			record.Events = append(record.Events, other)
		}
		publishTimelineSafe(t, s, target, record)
	}
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
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
	scope.DateKinds = []string{"created", "modified"}
	selected, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Len(t, selected.Items, 2)
	require.Len(t, selected.DateOnly, 1)
	require.Equal(t, ids["exact"], selected.Items[0].NodeID)
	require.Equal(t, "date-kind-switch", selected.Items[0].Rule)
	scope.DateKinds = nil

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

// Republishing source metadata invalidates the revision without fabricating a head.
func checkTimelineFreshnessSourceRevision(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "revision.txt", fakeHash("f11"), 1, "text/plain")
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "f12")
	publishTimelineSafe(t, s, target, record)
	metadata := document.SourceMetadataV1{ContractVersion: document.SourceMetadataContractV1,
		Fields: []document.SourceMetadataFieldV1{{Key: "title", Namespace: "pdf.info", SourceField: "Title", Value: document.SourceMetadataValueV1{Kind: document.SourceMetadataString, String: new("Synthetic")}}}}
	raw, _, err := document.MarshalSourceMetadataV1(metadata)
	require.NoError(t, err)
	_, err = s.PublishSourceMetadata(ctx, node.BlobHash, fakeHash("f13"), raw)
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
	require.ErrorIs(t, err, ErrDocumentDatesPending)
	_, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.ErrorIs(t, err, ErrDocumentDatesPending)
}

// Exact instants use containing day bounds even when the local date differs.
func checkTimelineAnomalousInstantPlacement(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	for _, tc := range []struct {
		zone, day, instant string
		minute, minutes    int
	}{
		{"America/Santiago", "2026-09-05", "2026-09-06T03:30:00", 1410, 1440},
		{"America/Santiago", "2026-09-06", "2026-09-06T04:10:00", 10, 1380},
		{"America/Havana", "2026-03-08", "2026-03-08T05:10:00", 10, 1380},
		{"America/Toronto", "2026-03-08", "2026-03-08T07:10:00", 130, 1380},
		{"America/Toronto", "2026-11-01", "2026-11-01T06:10:00", 130, 1500},
		{"America/St_Johns", "2010-11-07", "2010-11-07T03:00:00", 30, 1500},
	} {
		t.Run(tc.zone+"/"+tc.day, func(t *testing.T) {
			s := open(t)
			ctx := t.Context()
			node, err := s.CreateFile(ctx, s.RootID(), "instant.txt", fakeHash("da1"), 1, "text/plain")
			require.NoError(t, err)
			target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
			record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "da2")
			e := &record.Events[0]
			e.DateValue, e.RawValue, e.Precision, e.TimezoneKind = tc.instant, tc.instant, "second", "utc"
			e.AxisKey, err = document.EventAxisKey(e.DateValue, e.Precision, e.TimezoneKind, nil)
			require.NoError(t, err)
			var hasUTC bool
			e.UTCKey, hasUTC, err = document.EventUTCKey(e.DateValue, e.Precision, e.TimezoneKind, nil)
			require.NoError(t, err)
			require.True(t, hasUTC)
			e.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, e.SourceKey, e.DateKind, e.DateValue, e.Precision, e.TimezoneKind, e.EvidenceSHA256)
			record.Primaries = nil
			for _, disclosure := range []string{"full", "safe"} {
				primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", disclosure)
				require.True(t, ok)
				record.Primaries = append(record.Primaries, primary)
			}
			_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
			require.NoError(t, err)
			svc := timelineTestService(t, s)
			date, err := civil.ParseDate(tc.day)
			require.NoError(t, err)
			scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: tc.zone}
			days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
			require.NoError(t, err)
			require.Equal(t, []TimelineDayCount{{Date: tc.day, Count: 1}}, days.Days)
			page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, tc.minutes, page.Bounds.Minutes)
			require.Len(t, page.Items, 1)
			require.Equal(t, node.ID, page.Items[0].NodeID)
			require.Equal(t, tc.minute, *page.Items[0].AxisMinute)
		})
	}
}

func checkTimelinePairedCalendarDates(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "meeting.ics", fakeHash("ca1"), 1, "text/calendar")
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "ca2")
	record.DocumentKind = "calendar"
	start := record.Events[0]
	start.DateKind = "started"
	start.DateValue, start.RawValue, start.AxisKey = "2019-03-04", "2019-03-04", "2019-03-04T00:00:00.000000000"
	start.SourceKey = "metadata/ca2/calendar.event.e000001.start/0"
	start.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, start.SourceKey, start.DateKind, start.DateValue, start.Precision, start.TimezoneKind, start.EvidenceSHA256)
	end := start
	end.DateKind = "ended"
	end.DateValue, end.RawValue, end.AxisKey = "2019-03-06", "2019-03-06", "2019-03-06T00:00:00.000000000"
	end.SourceKey = "metadata/ca2/calendar.event.e000001.end/1"
	end.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, end.SourceKey, end.DateKind, end.DateValue, end.Precision, end.TimezoneKind, end.EvidenceSHA256)
	record.Events = []document.DocumentEventV1{start, end}
	record.Primaries = nil
	for _, disclosure := range []string{"full", "safe"} {
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", disclosure)
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
	}
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	for day := range 3 {
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date.AddDays(day), Limit: 10})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, 1)
		require.Equal(t, start.EventID, page.DateOnly[0].EventID)
	}
	// The end claim itself belongs to its own date when selected by kind.
	scope.DateKinds = []string{"ended"}
	page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date.AddDays(2), Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.DateOnly, 1)
	require.Equal(t, end.EventID, page.DateOnly[0].EventID)
	page, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date.AddDays(1), Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.DateOnly)
}

func checkTimelineUndatedSafeOnly(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "private-date.txt", fakeHash("bc1"), 1, "text/plain")
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "bc2")
	record.Events[0].Sensitive = true
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
	require.NoError(t, err)
	require.Empty(t, days.Days)
	require.EqualValues(t, 1, days.Undated)
	page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Empty(t, page.DateOnly)
	require.Empty(t, page.MonthOnly)
}

func checkTimelineDuplicateCollapse(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	var ids []int64
	for i, seed := range []string{"dd1", "dd2"} {
		node, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("duplicate-%d.txt", i), fakeHash("dd0"), 1, "text/plain")
		require.NoError(t, err)
		ids = append(ids, node.ID)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		publishTimelineSafe(t, s, target, record)
	}
	svc := timelineTestService(t, s)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	for _, tc := range []struct {
		collapse bool
		count    int
	}{{false, 2}, {true, 1}} {
		scope.Query.Filters.CollapseDuplicates = tc.collapse
		days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
		require.NoError(t, err)
		require.Len(t, days.Days, 1)
		require.EqualValues(t, tc.count, days.Days[0].Count)
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, tc.count)
		for _, item := range page.DateOnly {
			require.Contains(t, ids, item.NodeID)
		}
		if tc.collapse && s.postgresSchema == "" {
			group, err := s.DuplicateGroupByHash(ctx, fakeHash("dd0"), 1)
			require.NoError(t, err)
			require.Equal(t, group.RepresentativeNodeID, page.DateOnly[0].NodeID)
		}
	}
	if s.postgresSchema == "" {
		// Collapse must select a representative from the scoped population.
		scope.Query.Filters.Paths = []string{"/duplicate-1.txt"}
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, 1)
		require.Equal(t, ids[1], page.DateOnly[0].NodeID)
		days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
		require.NoError(t, err)
		require.Equal(t, []TimelineDayCount{{Date: date.String(), Count: 1, DateOnly: 1}}, days.Days)
	}
}

// Exercise D's composition with the existing pinned population owner.
func TestTimelinePreparedPopulationAndCoverage(t *testing.T) {
	s := newTestStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := t.Context()
	var nodes []Node
	for i := range 2 {
		seed := fmt.Sprintf("%x", 0xe00+i)
		node, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("population-%d.txt", i), fakeHash(seed), 1, "text/plain")
		require.NoError(t, err)
		nodes = append(nodes, node)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		e := &record.Events[0]
		e.DateValue, e.RawValue, e.Precision, e.AxisKey = "2019-03-04T09:30:00", "2019-03-04T09:30:00", "second", "2019-03-04T09:30:00.000000000"
		e.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, e.SourceKey, e.DateKind, e.DateValue, e.Precision, e.TimezoneKind, e.EvidenceSHA256)
		record.Primaries[0].EventID = e.EventID
		publishTimelineSafe(t, s, target, record)
	}
	roles := `["author","last_saved_by","custodian","sender","recipient","copied","organizer","attendee","participant","speaker","depicted"]`
	entries := make([]string, 64)
	for i := range entries {
		person, err := s.CreatePerson(ctx, fmt.Sprintf("Budget Person %02d", i), "operator")
		require.NoError(t, err)
		for _, node := range nodes {
			_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: person.PersonID, Role: "speaker", Action: "assert", Revision: 1})
			require.NoError(t, err)
		}
		entries[i] = fmt.Sprintf(`{"person_id":%q,"roles":%s}`, person.PersonID, roles)
	}
	profile := catalogProcessingProfile(t, false)
	require.NoError(t, s.withStorageTx(ctx, func(tx *sql.Tx) error { return ensureProcessingProfileTx(ctx, tx, profile) }))
	coverage := CoverageSelection{Configuration: "configured", ProfileFingerprint: profile.Fingerprint}
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	for _, personScope := range []bool{false, true} {
		filters := ""
		if personScope {
			filters = `,"filters":{"people":[` + strings.Join(entries, ",") + `]}`
		}
		value := snapshotTestQuery(t, `{"syntax":"advanced","text":"text_coverage:unprocessed"`+filters+`}`)
		scope := TimelineScope{Query: value, Zone: "UTC", Coverage: coverage}
		days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
		require.NoError(t, err)
		require.EqualValues(t, 2, days.Days[0].Count)
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 1})
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, nodes[0].ID, page.Items[0].NodeID)
		require.NotEmpty(t, page.NextCursor)
		for _, selection := range []CoverageSelection{{}, {Configuration: "profile_required"}} {
			scope.Coverage = selection
			_, err = svc.TimelineDays(ctx, "owner", scope, date, date)
			require.ErrorIs(t, err, ErrInvalidCoverageSelection)
			_, err = svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 1})
			require.ErrorIs(t, err, ErrInvalidCoverageSelection)
		}
		var scratch int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_temp_master WHERE name='query_population_members'`).Scan(&scratch))
		require.Zero(t, scratch)
		require.Empty(t, s.LeasedLexicalGenerationRoots())
		if personScope {
			_, _, err = s.Trash(ctx, nodes[1].ID, UnconditionalRev)
			require.NoError(t, err)
			// Hold the only pooled connection and cancel the request context: a
			// continuation must come entirely from the already frozen snapshot.
			conn, err := s.db.Conn(ctx)
			require.NoError(t, err)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			continuation, err := svc.TimelineDay(cancelled, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 1, Cursor: page.NextCursor})
			require.NoError(t, conn.Close())
			require.NoError(t, err)
			require.Len(t, continuation.Items, 1)
			require.Equal(t, nodes[1].ID, continuation.Items[0].NodeID)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = svc.TimelineDays(cancelled, "owner", TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, date, date)
	require.ErrorIs(t, err, context.Canceled)
	var scratch int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_temp_master WHERE name='query_population_members'`).Scan(&scratch))
	require.Zero(t, scratch)
	require.Empty(t, s.LeasedLexicalGenerationRoots())
}

func TestTimelineTextScopeUsesPublishedGeneration(t *testing.T) {
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	for i, version := range versions {
		target := requireDocumentEventTarget(t, s, version)
		record := documentEventRecord(t, s.VaultID(), version, fmt.Sprintf("e%x", i+1))
		publishTimelineSafe(t, s, target, record)
	}
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{"syntax":"advanced","text":"mercury"}`), Zone: "UTC"}
	assertMatches := func(want int) TimelineDayPage {
		t.Helper()
		days, err := svc.TimelineDays(ctx, "owner", scope, date, date)
		require.NoError(t, err)
		if want == 0 {
			require.Empty(t, days.Days)
		} else {
			require.EqualValues(t, want, days.Days[0].Count)
		}
		page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, want)
		return page
	}
	assertMatches(0)
	profile := catalogProcessingProfile(t, false)
	build := lexicalSearchBuild(s, profile, catalogBuildID, "eligible mercury phrase")
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z",
	}
	require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", fakeHash("91")))
	page := assertMatches(1)
	node, err := s.NodeByPath(ctx, "/synthetic-source-a.pdf")
	require.NoError(t, err)
	require.Equal(t, node.ID, page.DateOnly[0].NodeID)
	require.Empty(t, s.LeasedLexicalGenerationRoots())

	// Exercise D's pinned read and person-edge preparation while a published
	// generation is leased. The cancellation happens after TEMP is populated.
	_, err = s.CreatePerson(ctx, "Absent Reader", "operator")
	require.NoError(t, err)
	s.db.SetMaxOpenConns(1)

	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	service := newQuerySnapshotService(s, querySnapshotServiceOptions{
		ChargeHook: func(ctx context.Context, _ string, _, bytes int64) error {
			if bytes > 0 {
				close(entered)
				<-release
				return ctx.Err()
			}
			return nil
		},
	})
	t.Cleanup(func() { require.NoError(t, service.Close()) })
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	personQuery := snapshotTestQuery(t, `{"syntax":"advanced","text":"NOT author:\"Absent Reader\""}`)
	done := make(chan error, 1)
	go func() {
		_, err := service.TimelineDay(readCtx, "owner", TimelineDayRequest{Scope: TimelineScope{Query: personQuery, Zone: "UTC"}, Date: date, Limit: 1})
		done <- err
	}()
	<-entered // A placement was read after person-edge TEMP materialization.
	require.NotEmpty(t, s.LeasedLexicalGenerationRoots())
	require.Positive(t, service.stats().ReservedBytes)
	cancel()
	require.Positive(t, service.stats().ReservedBytes)
	close(release)
	released = true
	require.ErrorIs(t, <-done, context.Canceled)
	require.Empty(t, s.LeasedLexicalGenerationRoots())
	require.Equal(t, snapshotCacheStats{}, service.stats())
	var scratch int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_temp_master WHERE name='query_population_members'`).Scan(&scratch))
	require.Zero(t, scratch)
}

func checkTimelinePeopleScopeRespectsHiddenLock(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	person, err := s.CreatePerson(ctx, "Scoped Reader", "operator")
	require.NoError(t, err)
	var nodes []Node
	for i, name := range []string{"hidden.jpg", "open.txt"} {
		mime := "text/plain"
		if i == 0 {
			mime = "image/jpeg"
		}
		node, err := s.CreateFile(ctx, s.RootID(), name, fakeHash("d80"), 1, mime)
		require.NoError(t, err)
		nodes = append(nodes, node)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, fmt.Sprintf("%x", 0xd90+i))
		publishTimelineSafe(t, s, target, record)
		_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: person.PersonID, Role: "speaker", Action: "assert", Revision: 1})
		require.NoError(t, err)
	}
	asset, err := s.PhotoAssetForNode(ctx, nodes[0].ID)
	require.NoError(t, err)
	require.NoError(t, s.SetupPhotoHidden(ctx, "synthetic-passcode"))
	_, err = s.SetPhotoAssetHidden(ctx, asset.ID, asset.Revision, true)
	require.NoError(t, err)
	for i, name := range []string{"hidden-undated.jpg", "open-undated.txt"} {
		mime := "text/plain"
		if i == 0 {
			mime = "image/jpeg"
		}
		node, err := s.CreateFile(ctx, s.RootID(), name, fakeHash("d81"), 1, mime)
		require.NoError(t, err)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "d82")
		record.Events[0].Sensitive = true
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
		_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: person.PersonID, Role: "speaker", Action: "assert", Revision: 1})
		require.NoError(t, err)
		if i == 0 {
			asset, err := s.PhotoAssetForNode(ctx, node.ID)
			require.NoError(t, err)
			_, err = s.SetPhotoAssetHidden(ctx, asset.ID, asset.Revision, true)
			require.NoError(t, err)
		}
	}
	token, _, err := s.UnlockPhotoHidden(ctx, "synthetic-passcode")
	require.NoError(t, err)
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	svc := timelineTestService(t, s)
	scope := TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}
	if s.postgresSchema == "" {
		scope.Query = snapshotTestQuery(t, fmt.Sprintf(`{"filters":{"person_ids":[%q]}}`, person.PersonID))
	}
	for _, tc := range []struct {
		ctx    context.Context
		hidden bool
		want   int
	}{
		{WithVisiblePeopleOnly(ctx), true, 1},
		{WithVisiblePeopleOnly(WithPhotoHiddenToken(ctx, token)), false, 2},
	} {
		days, err := svc.TimelineDays(tc.ctx, "owner", scope, date, date)
		require.NoError(t, err)
		require.EqualValues(t, tc.want, days.Days[0].Count)
		require.EqualValues(t, tc.want, days.Undated)
		page, err := svc.TimelineDay(tc.ctx, "owner", TimelineDayRequest{Scope: scope, Date: date, Limit: 10, HiddenLocked: !tc.hidden})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, tc.want)
		if tc.hidden {
			require.Equal(t, nodes[1].ID, page.DateOnly[0].NodeID)
		}
		collapsed := scope
		collapsed.Query.Filters.CollapseDuplicates = true
		days, err = svc.TimelineDays(tc.ctx, "owner", collapsed, date, date)
		require.NoError(t, err)
		require.EqualValues(t, 1, days.Days[0].Count)
		require.EqualValues(t, 1, days.Undated)
		page, err = svc.TimelineDay(tc.ctx, "owner", TimelineDayRequest{Scope: collapsed, Date: date, Limit: 10, HiddenLocked: !tc.hidden})
		require.NoError(t, err)
		require.Len(t, page.DateOnly, 1)
		if tc.hidden {
			require.Equal(t, nodes[1].ID, page.DateOnly[0].NodeID)
		}
	}
}
