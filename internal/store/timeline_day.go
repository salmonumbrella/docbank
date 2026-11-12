package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/civil"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
	"go.kenn.io/kit/civiltime"
)

type TimelineTick struct {
	Minute int
	Label  string
}

type TimelineDayBounds struct {
	Start, End time.Time
	Minutes    int
	Ticks      []TimelineTick
}

func timelineDayBounds(date civil.Date, loc *time.Location) (TimelineDayBounds, bool) {
	start, end, ok := civiltime.DayBounds(date, loc)
	if !ok {
		return TimelineDayBounds{}, false
	}
	bounds := TimelineDayBounds{Start: start, End: end, Minutes: int(end.Sub(start) / time.Minute)}
	counts := make(map[string]int)
	for minute := 0; minute < bounds.Minutes; minute++ {
		wall := start.Add(time.Duration(minute) * time.Minute).In(loc)
		if minute != 0 && (wall.Minute() != 0 || wall.Second() != 0) {
			continue
		}
		label := wall.Format("15:04")
		bounds.Ticks = append(bounds.Ticks, TimelineTick{Minute: minute, Label: label})
		counts[label]++
	}
	for i := range bounds.Ticks {
		tick := &bounds.Ticks[i]
		if counts[tick.Label] < 2 {
			continue
		}
		_, seconds := start.Add(time.Duration(tick.Minute) * time.Minute).In(loc).Zone()
		sign := "+"
		if seconds < 0 {
			sign = "−"
			seconds = -seconds
		}
		tick.Label += fmt.Sprintf(" %s%02d:%02d", sign, seconds/3600, seconds%3600/60)
	}
	return bounds, true
}

func timelineAxisMinute(start, instant time.Time) int {
	return int(instant.Sub(start) / time.Minute)
}

const maxTimelineStrip = 200

var ErrTimelineCursorMismatch = errors.New("timeline cursor mismatch")

// ErrTimelineDateNotInZone identifies a skipped civil endpoint.
var ErrTimelineDateNotInZone = errors.New("date does not exist in timezone")

type TimelineDayRequest struct {
	Scope        TimelineScope
	Date         civil.Date
	Cursor       string
	Limit        int
	HiddenLocked bool
}
type TimelineDayPage struct {
	Bounds                                TimelineDayBounds
	Items, DateOnly, MonthOnly            []TimelineItem
	DateOnlyTruncated, MonthOnlyTruncated bool
	Zone                                  string
	Defaulted                             bool
	NextCursor                            string
}
type timelineCachedMetadata struct {
	Page  TimelineDayPage
	Scope string
	Limit int
}

func timelineScopeHash(req TimelineDayRequest) (string, error) {
	raw, err := json.Marshal(struct {
		Query  query.Query
		Zone   string
		Date   string
		Kinds  []string
		Limit  int
		Hidden bool
	}{req.Scope.Query, req.Scope.Zone, req.Date.String(), req.Scope.DateKinds, req.Limit, req.HiddenLocked})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *QuerySnapshotService) TimelineDay(ctx context.Context, owner string, req TimelineDayRequest) (TimelineDayPage, error) {
	if owner == "" || s.store == nil {
		return TimelineDayPage{}, ErrInvalidSnapshotRequest
	}
	if s.store.postgresSchema != "" {
		if err := validatePostgresTimelineScope(req.Scope.Query); err != nil {
			return TimelineDayPage{}, err
		}
	}
	if req.Limit == 0 {
		req.Limit = 200
	}
	if req.Limit < 1 || req.Limit > 500 {
		return TimelineDayPage{}, ErrInvalidSnapshotRequest
	}
	req.Scope.DateKinds = slices.Clone(req.Scope.DateKinds)
	slices.Sort(req.Scope.DateKinds)
	req.Scope.DateKinds = slices.Compact(req.Scope.DateKinds)
	if req.Cursor != "" {
		cursor, err := s.decodeCursor(owner, req.Cursor)
		if err != nil || cursor.Projection != "timeline_day" {
			return TimelineDayPage{}, ErrTimelineCursorMismatch
		}
		cached, err := s.lookup(owner, cursor.SnapshotID)
		if err != nil {
			return TimelineDayPage{}, err
		}
		if cached.kind != "timeline_day" {
			return TimelineDayPage{}, ErrTimelineCursorMismatch
		}
		var meta timelineCachedMetadata
		if err := json.Unmarshal(cached.metadata, &meta); err != nil {
			return TimelineDayPage{}, err
		}
		if req.Scope.Defaulted {
			req.Scope.Zone = meta.Page.Zone
		}
		scope, err := timelineScopeHash(req)
		if err != nil {
			return TimelineDayPage{}, err
		}
		if scope != meta.Scope || cursor.Scope != scope || cursor.PageSize != meta.Limit || cursor.Offset < 0 || cursor.Offset >= int64(len(cached.rows)) || cursor.Offset%int64(meta.Limit) != 0 {
			return TimelineDayPage{}, ErrTimelineCursorMismatch
		}
		return s.renderTimelinePage(&cached, meta, cursor.Offset)
	}
	loc, err := query.LoadZone(req.Scope.Zone)
	if err != nil {
		return TimelineDayPage{}, err
	}
	bounds, ok := timelineDayBounds(req.Date, loc)
	if !ok {
		return TimelineDayPage{}, ErrTimelineDateNotInZone
	}
	buildCtx, cancel := context.WithTimeout(ctx, querySnapshotBuildTimeout)
	defer cancel()
	build, err := s.admitNewBuild(owner, cancel, false, "")
	if err != nil {
		return TimelineDayPage{}, err
	}
	published := false
	defer func() { s.finishBuild(build, published) }()
	var chargedRows, chargedBytes int64
	reserve := func(rows, bytes int64) error {
		chargedRows += rows
		chargedBytes += bytes
		if chargedRows > build.reservedRows || chargedBytes > build.reservedBytes {
			return s.chargeBuild(build, max(0, chargedRows-build.reservedRows), max(0, chargedBytes-build.reservedBytes))
		}
		return nil
	}
	page := TimelineDayPage{Bounds: bounds, Zone: req.Scope.Zone, Defaulted: req.Scope.Defaulted, Items: []TimelineItem{}, DateOnly: []TimelineItem{}, MonthOnly: []TimelineItem{}}
	var timed []TimelineItem
	err = s.store.withReadTx(buildCtx, func(tx *sql.Tx) error {
		browser, _ := buildCtx.Value(visiblePersonContextKey{}).(bool)
		scoped := buildCtx
		if browser {
			var err error
			scoped, err = s.store.scopeQueryPeople(scoped, tx)
			if err != nil {
				return err
			}
			if queryPeopleHideLocked(scoped) != req.HiddenLocked {
				return ErrTimelineCursorMismatch
			}
		}
		scoped = WithVisiblePeopleOnly(WithQueryDefaultTimezone(scoped, req.Scope.Zone))
		scoped = context.WithValue(scoped, queryPeopleHiddenKey{}, req.HiddenLocked)
		if err := requireDocumentDateIndex(scoped, tx); err != nil {
			return err
		}
		postgres := s.store.postgresSchema != ""
		generation := ""
		compiled := CompiledQuery{Query: req.Scope.Query}
		if !postgres {
			generation, err = collectionGenerationTx(scoped, tx)
			if err != nil {
				return err
			}
			compiled, err = (queryCompiler{}).compile(scoped, req.Scope.Query, queryResolver{q: tx})
			if err != nil {
				return err
			}
		}
		tail := `SELECT node_id,version_id,generation_id,event_id,tier,axis_key,COALESCE(utc_key,''),civil_end_date FROM (SELECT dedup.*,ROW_NUMBER() OVER (PARTITION BY tier ORDER BY civil_date,node_id) strip_rank FROM dedup WHERE duplicate_rank=1) ranked WHERE tier<2 OR strip_rank<=201 ORDER BY tier,axis_key,node_id`
		statement, args, err := timelinePlacedSQL(compiled, generation, req.Scope, req.Date, req.Date, loc, req.HiddenLocked, postgres, func(*timelineBinds) string { return tail })
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(scoped, statement, args...)
		if err != nil {
			return err
		}
		var placements []TimelinePlacement
		for rows.Next() {
			var p TimelinePlacement
			var axis, utc string
			if err := rows.Scan(&p.NodeID, &p.VersionID, &p.generationID, &p.EventID, &p.tier, &axis, &utc, &p.endDate); err != nil {
				_ = rows.Close()
				return err
			}
			if len(req.Scope.DateKinds) > 0 {
				p.Rule = "date-kind-switch"
			} else {
				p.Rule = document.PrimaryRuleV1
			}
			if p.tier < 2 {
				var instant time.Time
				if p.tier == 0 {
					instant, err = time.Parse(time.RFC3339Nano, utc)
				} else {
					instant, err = document.EventLocalInstant(axis, "fraction", loc)
				}
				if err != nil {
					_ = rows.Close()
					return err
				}
				minute := timelineAxisMinute(bounds.Start, instant)
				p.AxisMinute = &minute
			}
			if err := reserve(1, 512); err != nil {
				_ = rows.Close()
				return err
			}
			placements = append(placements, p)
			if int64(len(placements)) > querySnapshotMaxRows {
				_ = rows.Close()
				return ErrQuerySnapshotTooLarge
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		items, err := s.store.hydrateTimelineItems(scoped, tx, placements, req.HiddenLocked)
		if err != nil {
			return err
		}
		for _, item := range items {
			switch item.tier {
			case 0, 1:
				timed = append(timed, item)
			case 2:
				if len(page.DateOnly) < maxTimelineStrip {
					page.DateOnly = append(page.DateOnly, item)
				} else {
					page.DateOnlyTruncated = true
				}
			case 3:
				if len(page.MonthOnly) < maxTimelineStrip {
					page.MonthOnly = append(page.MonthOnly, item)
				} else {
					page.MonthOnlyTruncated = true
				}
			}
		}
		slices.SortFunc(timed, func(a, b TimelineItem) int {
			if c := cmp.Compare(*a.AxisMinute, *b.AxisMinute); c != 0 {
				return c
			}
			return cmp.Compare(a.NodeID, b.NodeID)
		})
		return nil
	})
	if err != nil {
		return TimelineDayPage{}, err
	}
	scope, err := timelineScopeHash(req)
	if err != nil {
		return TimelineDayPage{}, err
	}
	metadata, err := json.Marshal(timelineCachedMetadata{Page: page, Scope: scope, Limit: req.Limit})
	if err != nil {
		return TimelineDayPage{}, err
	}
	rows := make([][]byte, len(timed))
	bytes := int64(len(metadata)) + querySnapshotHandleOverhead + int64(len(owner)+len(build.snapshotID))
	if bytes > querySnapshotMaxBytes {
		return TimelineDayPage{}, ErrQuerySnapshotTooLarge
	}
	for i, item := range timed {
		rows[i], err = json.Marshal(item)
		if err != nil {
			return TimelineDayPage{}, err
		}
		if int64(len(rows[i])) > querySnapshotMaxRowBytes {
			return TimelineDayPage{}, ErrQuerySnapshotTooLarge
		}
		bytes += int64(len(rows[i]))
		if bytes > querySnapshotMaxBytes {
			return TimelineDayPage{}, ErrQuerySnapshotTooLarge
		}
	}
	if bytes > chargedBytes {
		if err := reserve(0, bytes-chargedBytes); err != nil {
			return TimelineDayPage{}, err
		}
	}
	if err := buildCtx.Err(); err != nil {
		return TimelineDayPage{}, err
	}
	now := s.now().UTC()
	cached := &cachedQuerySnapshot{kind: "timeline_day", id: build.snapshotID, owner: owner, metadata: metadata, rows: rows, extraRows: int64(len(page.DateOnly) + len(page.MonthOnly)), created: now, lastUsed: now, bytes: bytes}
	if err := s.publishBuild(build, cached); err != nil {
		return TimelineDayPage{}, err
	}
	published = true
	return s.renderTimelinePage(cached, timelineCachedMetadata{Page: page, Scope: scope, Limit: req.Limit}, 0)
}

func (s *QuerySnapshotService) renderTimelinePage(cached *cachedQuerySnapshot, meta timelineCachedMetadata, start int64) (TimelineDayPage, error) {
	page := meta.Page
	end := min(start+int64(meta.Limit), int64(len(cached.rows)))
	page.Items = make([]TimelineItem, end-start)
	for i := start; i < end; i++ {
		if err := json.Unmarshal(cached.rows[i], &page.Items[i-start]); err != nil {
			return TimelineDayPage{}, err
		}
	}
	if start > 0 {
		page.DateOnly = []TimelineItem{}
		page.MonthOnly = []TimelineItem{}
		page.DateOnlyTruncated = false
		page.MonthOnlyTruncated = false
	}
	if end < int64(len(cached.rows)) {
		var err error
		page.NextCursor, err = s.encodeCursor(cached.owner, snapshotCursorPayload{Projection: "timeline_day", Scope: meta.Scope, SnapshotID: cached.id, PageDirection: "next", PageSize: meta.Limit, Offset: end})
		if err != nil {
			return TimelineDayPage{}, err
		}
	}
	return page, nil
}
