package store

import (
	"bytes"
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

// ErrTimelineScopeUnsupported identifies narrowing Query scopes that the
// PostgreSQL timeline operation cannot execute until shared Query is ported.
var ErrTimelineScopeUnsupported = errors.New("timeline query scope is unsupported on PostgreSQL")

func validatePostgresTimelineScope(q query.Query) error {
	actual, err := query.Canonical(q)
	if err != nil {
		return err
	}
	unscoped := q
	unscoped.Text = ""
	unscoped.Filters = query.Filters{CollapseDuplicates: q.Filters.CollapseDuplicates}
	baseline, err := query.Canonical(unscoped)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, baseline) {
		return ErrTimelineScopeUnsupported
	}
	if q.Sort.Field == "relevance" {
		return compileExpressionError(0, len(q.Text), "relevance sort is not supported by bound queries")
	}
	return nil
}

type timelineBinds struct {
	postgres bool
	args     []any
}

func (b *timelineBinds) add(value any) string {
	b.args = append(b.args, value)
	if b.postgres {
		return fmt.Sprintf("$%d", len(b.args))
	}
	return "?"
}

// timelinePlacedSQL creates one row per current version and day after ranking
// eligible date claims. The same relation drives counts and the day view.
func timelinePlacedSQL(compiled CompiledQuery, generation string, scope TimelineScope, from, to civiltime.Date, loc *time.Location, hidden, postgres bool, tail func(*timelineBinds) string) (string, []any, error) {
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
	binds := &timelineBinds{postgres: postgres}
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
		values = append(values, "(CAST("+binds.add(day.String())+" AS TEXT),CAST("+binds.add(ak)+" AS TEXT),CAST("+binds.add(bk)+" AS TEXT))")
	}
	if len(values) == 0 {
		return "", nil, civiltime.ErrDateNotInZone
	}
	var population compiledQueryFragment
	if postgres {
		population = selectCompiledPopulation(compiledQueryFragment{}, compiled.Query.Filters.CollapseDuplicates)
		if len(population.args) != 0 || population.relations != 0 {
			return "", nil, errors.New("unscoped timeline population requires query dependencies")
		}
	} else {
		population, err = matchedPopulation(compiled, generation, nil)
		if err != nil {
			return "", nil, err
		}
	}
	keyMarks := []string{
		binds.add(startKey), binds.add(endKey), binds.add(to.String()), binds.add(from.String()),
		binds.add(from.String()[:7]), binds.add(to.String()[:7]), binds.add(from.String()[:4]), binds.add(to.String()[:4]),
	}
	binds.args = append(binds.args, population.args...)
	eligible := "k.sensitive=0 AND k.primary_safe=1"
	if len(scope.DateKinds) > 0 {
		marks := make([]string, len(scope.DateKinds))
		for i, kind := range scope.DateKinds {
			marks[i] = binds.add(kind)
		}
		eligible = "k.sensitive=0 AND k.date_kind IN (" + strings.Join(marks, ",") + ")"
	}
	hiddenPredicate := ""
	if hidden {
		hiddenPredicate = " AND " + personVersionVisibleSQL("cv.version_id")
	}
	duplicate := `, dedup AS (SELECT placed.*,1 AS duplicate_rank FROM placed WHERE placement_rank=1)`
	sqlText := `WITH days(day,start_key,end_key) AS (VALUES ` + strings.Join(values, ",") + `),
 keys AS (
 SELECT k.*,0 AS tier FROM document_date_keys k WHERE k.utc_key>=` + keyMarks[0] + ` AND k.utc_key<` + keyMarks[1] + `
 UNION ALL SELECT k.*,CASE WHEN k.precision='date' THEN 2 ELSE 1 END FROM document_date_keys k WHERE k.utc_key IS NULL AND k.precision NOT IN ('year','month') AND k.civil_date<=` + keyMarks[2] + ` AND k.civil_end_date>=` + keyMarks[3] + `
 UNION ALL SELECT k.*,3 FROM document_date_keys k WHERE (k.precision='month' AND k.civil_month>=` + keyMarks[4] + ` AND k.civil_month<=` + keyMarks[5] + `) OR (k.precision='year' AND k.civil_year>=` + keyMarks[6] + ` AND k.civil_year<=` + keyMarks[7] + `)
 ), population AS (` + population.sql + `), eligible AS (
 SELECT d.day,n.id AS node_id,cv.version_id,k.generation_id,k.event_id,k.tier,k.axis_key,k.utc_key,k.civil_date,k.civil_end_date,cv.blob_hash
 FROM keys k JOIN document_event_heads h ON h.generation_id=k.generation_id AND h.content_version_id=k.content_version_id
 JOIN content_versions cv ON cv.version_id=h.content_version_id JOIN nodes n ON n.id=cv.node_id JOIN population m ON m.content_version_id=cv.version_id
 JOIN days d ON ((k.tier=0 AND k.utc_key>=d.start_key AND k.utc_key<d.end_key) OR (k.tier IN (1,2) AND k.civil_date<=d.day AND k.civil_end_date>=d.day) OR (k.tier=3 AND ((k.precision='month' AND k.civil_month=substr(d.day,1,7)) OR (k.precision='year' AND k.civil_year=substr(d.day,1,4)))))
 WHERE ` + currentDocumentDateHead + ` AND ` + eligible + hiddenPredicate + `
 ), placed AS (SELECT eligible.*,ROW_NUMBER() OVER (PARTITION BY day,version_id ORDER BY tier,COALESCE(utc_key,axis_key),event_id) placement_rank FROM eligible)` + duplicate
	fragment := compiledQueryFragment{sql: `SELECT * FROM (` + sqlText + ` ` + tail(binds) + `) timeline_result`, args: binds.args, relations: population.relations}
	// bindQueryPopulation adds required shared query relations before this nested WITH.
	return bindQueryPopulation(fragment, CoverageSelection{}, generation)
}

func (s *QuerySnapshotService) TimelineDays(ctx context.Context, owner string, scope TimelineScope, from, to civiltime.Date) (TimelineDays, error) {
	result := TimelineDays{Days: []TimelineDayCount{}, Months: []TimelineMonthCount{}}
	if s.store != nil && s.store.postgresSchema != "" {
		if err := validatePostgresTimelineScope(scope.Query); err != nil {
			return result, err
		}
	}
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
		postgres := s.store.postgresSchema != ""
		generation := ""
		compiled := CompiledQuery{Query: scope.Query}
		if !postgres {
			generation, err = collectionGenerationTx(scoped, tx)
			if err != nil {
				return err
			}
			compiled, err = (queryCompiler{}).compile(scoped, scope.Query, queryResolver{q: tx})
			if err != nil {
				return err
			}
		}
		coarseIdentity := "version_id"
		if compiled.Query.Filters.CollapseDuplicates {
			coarseIdentity = "blob_hash"
		}
		visible := ""
		if hidden {
			visible = " AND " + personVersionVisibleSQL("m.content_version_id")
		}
		tail := func(b *timelineBinds) string {
			condition := "k.sensitive=0 AND k.primary_safe=1"
			if len(scope.DateKinds) > 0 {
				marks := make([]string, len(scope.DateKinds))
				for i, kind := range scope.DateKinds {
					marks[i] = b.add(kind)
				}
				condition = "k.sensitive=0 AND k.date_kind IN (" + strings.Join(marks, ",") + ")"
			}
			return `SELECT 'd',day,COUNT(*),CAST(SUM(CASE WHEN tier=1 THEN 1 ELSE 0 END) AS BIGINT),CAST(SUM(CASE WHEN tier=2 THEN 1 ELSE 0 END) AS BIGINT) FROM dedup WHERE duplicate_rank=1 AND tier<3 GROUP BY day
 UNION ALL SELECT 'm',substr(day,1,7),COUNT(DISTINCT ` + coarseIdentity + `),0,0 FROM eligible WHERE tier=3 GROUP BY substr(day,1,7)
 UNION ALL SELECT 'u','',COUNT(*),0,0 FROM population m JOIN document_event_heads h ON h.content_version_id=m.content_version_id WHERE ` + currentDocumentDateHead + visible + ` AND NOT EXISTS (SELECT 1 FROM document_date_keys k WHERE k.generation_id=h.generation_id AND ` + condition + `) ORDER BY 1,2`
		}
		statement, args, err := timelinePlacedSQL(compiled, generation, scope, from, to, loc, hidden, postgres, tail)
		if err != nil {
			return err
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
