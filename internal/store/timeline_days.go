package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
	"go.kenn.io/kit/civiltime"
)

const maxTimelineRangeDates = 92

type TimelineScope struct {
	Query     query.Query
	Zone      string
	Defaulted bool
	DateKinds []string
}
type TimelineDayCount struct {
	Date                      string
	Count, Floating, DateOnly int64
}
type TimelineMonthCount struct {
	Month  string
	Coarse int64
}
type TimelineDays struct {
	Days                     []TimelineDayCount
	Months                   []TimelineMonthCount
	Undated, PendingVersions int64
}

// timelinePlacedSQL creates one row per current version and day after ranking
// eligible date claims. The same relation drives counts and the day view.
func timelinePlacedSQL(compiled CompiledQuery, generation string, scope TimelineScope, from, to civiltime.Date, loc *time.Location, hidden bool, tail string) (string, []any, error) {
	start, end, err := civiltime.RangeBounds(from, to, loc)
	if err != nil {
		return "", nil, err
	}
	startKey, err := document.EventInstantUTCKey(start)
	if err != nil {
		return "", nil, err
	}
	endKey, err := document.EventInstantUTCKey(end)
	if err != nil {
		return "", nil, err
	}
	values := make([]string, 0, maxTimelineRangeDates)
	args := make([]any, 0, maxTimelineRangeDates*3)
	for day := from; day.Compare(to) <= 0; day = day.AddDays(1) {
		a, b, ok := civiltime.DayBounds(day, loc)
		if !ok {
			continue
		}
		ak, err := document.EventInstantUTCKey(a)
		if err != nil {
			return "", nil, err
		}
		bk, err := document.EventInstantUTCKey(b)
		if err != nil {
			return "", nil, err
		}
		values = append(values, "(?,?,?)")
		args = append(args, day.String(), ak, bk)
	}
	if len(values) == 0 {
		return "", nil, civiltime.ErrDateNotInZone
	}
	population, err := matchedPopulation(compiled, generation, nil)
	if err != nil {
		return "", nil, err
	}
	eligible := "k.sensitive=0 AND k.primary_safe=1"
	if len(scope.DateKinds) > 0 {
		marks := make([]string, len(scope.DateKinds))
		for i, kind := range scope.DateKinds {
			marks[i] = "?"
			args = append(args, kind)
		}
		eligible = "k.sensitive=0 AND k.date_kind IN (" + strings.Join(marks, ",") + ")"
	}
	// The arguments of the key arms precede the safe-kind and compiled filters.
	keyArgs := []any{startKey, endKey, to.String(), from.String(), from.String()[:7], to.String()[:7], from.String()[:4], to.String()[:4]}
	dayArgs := append([]any(nil), args[:len(values)*3]...)
	filterArgs := append([]any(nil), args[len(values)*3:]...)
	args = append(dayArgs, keyArgs...)
	args = append(args, population.args...)
	args = append(args, filterArgs...)
	hiddenPredicate := ""
	if hidden {
		hiddenPredicate = " AND " + personVersionVisibleSQL("cv.version_id")
	}
	duplicate := `, dedup AS (SELECT placed.*,1 AS duplicate_rank FROM placed WHERE placement_rank=1)`
	sqlText := `WITH days(day,start_key,end_key) AS (VALUES ` + strings.Join(values, ",") + `),
 keys AS (
 SELECT k.*,0 AS tier FROM document_date_keys k WHERE k.utc_key>=? AND k.utc_key<?
 UNION ALL SELECT k.*,CASE WHEN k.precision='date' THEN 2 ELSE 1 END FROM document_date_keys k WHERE k.utc_key IS NULL AND k.precision NOT IN ('year','month') AND k.civil_date<=? AND k.civil_end_date>=?
 UNION ALL SELECT k.*,3 FROM document_date_keys k WHERE (k.precision='month' AND k.civil_month>=? AND k.civil_month<=?) OR (k.precision='year' AND k.civil_year>=? AND k.civil_year<=?)
 ), population AS (` + population.sql + `), eligible AS (
 SELECT d.day,n.id AS node_id,cv.version_id,k.generation_id,k.event_id,k.tier,k.axis_key,k.utc_key,k.civil_date,k.civil_end_date,cv.blob_hash
 FROM keys k JOIN document_event_heads h ON h.generation_id=k.generation_id AND h.content_version_id=k.content_version_id
 JOIN content_versions cv ON cv.version_id=h.content_version_id JOIN nodes n ON n.id=cv.node_id JOIN population m ON m.content_version_id=cv.version_id
 JOIN days d ON ((k.tier=0 AND k.utc_key>=d.start_key AND k.utc_key<d.end_key) OR (k.tier IN (1,2) AND k.civil_date<=d.day AND k.civil_end_date>=d.day) OR (k.tier=3 AND ((k.precision='month' AND k.civil_month=substr(d.day,1,7)) OR (k.precision='year' AND k.civil_year=substr(d.day,1,4)))))
 WHERE ` + currentDocumentDateHead + ` AND ` + eligible + hiddenPredicate + `
 ), placed AS (SELECT eligible.*,ROW_NUMBER() OVER (PARTITION BY day,version_id ORDER BY tier,COALESCE(utc_key,axis_key),event_id) placement_rank FROM eligible)` + duplicate
	fragment := compiledQueryFragment{sql: `SELECT * FROM (` + sqlText + ` ` + tail + `) timeline_result`, args: args, relations: population.relations}
	// bindQueryPopulation adds required shared query relations before this nested WITH.
	return bindQueryPopulation(fragment, CoverageSelection{}, generation)
}

func (s *QuerySnapshotService) TimelineDays(ctx context.Context, owner string, scope TimelineScope, from, to civiltime.Date) (TimelineDays, error) {
	result := TimelineDays{Days: []TimelineDayCount{}, Months: []TimelineMonthCount{}}
	if from.Compare(to) > 0 || from.AddDays(maxTimelineRangeDates-1).Compare(to) < 0 {
		return result, errors.New("invalid timeline range")
	}
	loc, err := civiltime.LoadZone(scope.Zone)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, querySnapshotBuildTimeout)
	defer cancel()
	build, err := s.admitNewBuild(owner, cancel, true, "")
	if err != nil {
		return result, err
	}
	defer s.finishBuild(build, false)
	err = s.store.withReadTx(ctx, func(tx *sql.Tx) error {
		browser, _ := ctx.Value(visiblePersonContextKey{}).(bool)
		scoped := ctx
		if browser {
			var err error
			scoped, err = s.store.scopeQueryPeople(scoped, tx)
			if err != nil {
				return err
			}
		}
		hidden := browser && queryPeopleHideLocked(scoped)
		scoped = WithVisiblePeopleOnly(WithQueryDefaultTimezone(scoped, scope.Zone))
		scoped = context.WithValue(scoped, queryPeopleHiddenKey{}, hidden)
		if err := requireDocumentDateIndex(scoped, tx); err != nil {
			return err
		}
		generation, err := collectionGenerationTx(scoped, tx)
		if err != nil {
			return err
		}
		compiled, err := (queryCompiler{}).compile(scoped, scope.Query, queryResolver{q: tx})
		if err != nil {
			return err
		}
		coarseIdentity := "version_id"
		if compiled.Query.Filters.CollapseDuplicates {
			coarseIdentity = "blob_hash"
		}
		condition := "k.sensitive=0 AND k.primary_safe=1"
		if len(scope.DateKinds) > 0 {
			condition = "k.sensitive=0 AND k.date_kind IN (" + strings.TrimSuffix(strings.Repeat("?,", len(scope.DateKinds)), ",") + ")"
		}
		visible := ""
		if hidden {
			visible = " AND " + personVersionVisibleSQL("m.content_version_id")
		}
		tail := `SELECT 'd',day,COUNT(*),SUM(CASE WHEN tier=1 THEN 1 ELSE 0 END),SUM(CASE WHEN tier=2 THEN 1 ELSE 0 END) FROM dedup WHERE duplicate_rank=1 AND tier<3 GROUP BY day
 UNION ALL SELECT 'm',substr(day,1,7),COUNT(DISTINCT ` + coarseIdentity + `),0,0 FROM eligible WHERE tier=3 GROUP BY substr(day,1,7)
 UNION ALL SELECT 'u','',COUNT(*),0,0 FROM population m JOIN document_event_heads h ON h.content_version_id=m.content_version_id WHERE ` + currentDocumentDateHead + visible + ` AND NOT EXISTS (SELECT 1 FROM document_date_keys k WHERE k.generation_id=h.generation_id AND ` + condition + `) ORDER BY 1,2`
		statement, args, err := timelinePlacedSQL(compiled, generation, scope, from, to, loc, hidden, tail)
		if err != nil {
			return err
		}
		for _, kind := range scope.DateKinds {
			args = append(args, kind)
		}

		rows, err := tx.QueryContext(scoped, statement, args...)
		if err != nil {
			return fmt.Errorf("counting timeline placements: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var kind, label string
			var a, b, c int64
			if err := rows.Scan(&kind, &label, &a, &b, &c); err != nil {
				return err
			}
			if kind == "d" {
				result.Days = append(result.Days, TimelineDayCount{label, a, b, c})
			} else if kind == "m" {
				result.Months = append(result.Months, TimelineMonthCount{label, a})
			} else {
				result.Undated = a
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		return nil
	})
	return result, err
}
