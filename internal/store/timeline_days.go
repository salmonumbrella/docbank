package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/civil"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
	"go.kenn.io/kit/civiltime"
)

const maxTimelineRangeDates = 92

var ErrTimelineRangeOrder = errors.New("from must not follow to")
var ErrTimelineRangeTooLarge = errors.New("range exceeds 92 dates")

type TimelineScope struct {
	Query     query.Query
	Zone      string
	Defaulted bool
	DateKinds []string
	Coverage  CoverageSelection
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

func timelineEligibleSQL(b *timelineBinds, kinds []string) string {
	if len(kinds) == 0 {
		return "k.sensitive=0 AND k.primary_safe=1"
	}
	marks := make([]string, len(kinds))
	for i, kind := range kinds {
		marks[i] = b.add(kind)
	}
	return "k.sensitive=0 AND k.date_kind IN (" + strings.Join(marks, ",") + ")"
}

// timelinePlacedSQL creates one row per current version and day after ranking
// eligible date claims. The same relation drives counts and the day view.
func timelinePlacedSQL(population compiledQueryFragment, generation string, scope TimelineScope, from, to civil.Date, loc *time.Location, postgres bool, tail func(*timelineBinds) string) (string, []any, error) {
	start, end, err := query.RangeBounds(from, to, loc)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrTimelineDateNotInZone, err)
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
		return "", nil, ErrTimelineDateNotInZone
	}

	keyMarks := []string{
		binds.add(startKey), binds.add(endKey), binds.add(to.String()), binds.add(from.String()),
		binds.add(from.String()[:7]), binds.add(to.String()[:7]), binds.add(from.String()[:4]), binds.add(to.String()[:4]),
	}
	binds.args = append(binds.args, population.args...)
	eligible := timelineEligibleSQL(binds, scope.DateKinds)

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
 WHERE ` + currentDocumentDateHead + ` AND ` + eligible + `
 ), placed AS (SELECT eligible.*,ROW_NUMBER() OVER (PARTITION BY day,version_id ORDER BY tier,COALESCE(utc_key,axis_key),event_id) placement_rank FROM eligible)`
	tailSQL := tail(binds)
	fragment := compiledQueryFragment{sql: `SELECT * FROM (` + sqlText + ` ` + tailSQL + `) timeline_result`, args: binds.args, relations: population.relations}
	// bindQueryPopulation adds required shared query relations before this nested WITH.
	return bindQueryPopulation(fragment, scope.Coverage, generation)
}

// withTimelineRead keeps SQLite's prepared population on its pinned read and
// leaves PostgreSQL on its native transaction.
func (s *Store) withTimelineRead(ctx context.Context, fn func(metadataQuerier, string) error) error {
	if s.postgresSchema != "" {
		return s.withReadTx(ctx, func(tx *sql.Tx) error { return fn(tx, "") })
	}
	return s.withLexicalGenerationRead(ctx, func(q metadataQuerier, generation LexicalGeneration) error {
		return fn(q, generation.ID)
	})
}

func (s *Store) timelinePopulation(ctx context.Context, q metadataQuerier, compiled CompiledQuery, generation string, coverage CoverageSelection, hidden bool) (compiledQueryFragment, error) {
	collapse := compiled.Query.Filters.CollapseDuplicates
	if hidden {
		compiled.Query.Filters.CollapseDuplicates = false
	}
	var population compiledQueryFragment
	var err error
	if s.postgresSchema == "" {
		population, err = documentSnapshotPopulation(ctx, q, compiled, generation, coverage)
		if err != nil {
			return compiledQueryFragment{}, err
		}
	} else {
		population = selectCompiledPopulation(compiledQueryFragment{}, compiled.Query.Filters.CollapseDuplicates)
	}
	if s.postgresSchema != "" && (len(population.args) != 0 || population.relations != 0) {
		return compiledQueryFragment{}, errors.New("unscoped timeline population requires query dependencies")
	}
	if hidden {
		members := `SELECT m.node_id,m.content_version_id,cv.blob_hash,n.modified_at FROM (` + population.sql + `) m JOIN content_versions cv ON cv.version_id=m.content_version_id JOIN nodes n ON n.id=m.node_id WHERE ` + personVersionVisibleSQL("m.content_version_id")
		if collapse {
			population.sql = `SELECT node_id,content_version_id FROM (SELECT visible.*,ROW_NUMBER() OVER (PARTITION BY blob_hash ORDER BY ` + DuplicateRepresentativeOrder + `) duplicate_rank FROM (` + members + `) visible) ranked WHERE duplicate_rank=1`
		} else {
			population.sql = `SELECT node_id,content_version_id FROM (` + members + `) visible`
		}
	}
	return population, nil
}

// timelineReadPopulation resolves visibility and membership on the pinned read.
func (s *Store) timelineReadPopulation(ctx context.Context, q metadataQuerier, generation string, scope TimelineScope, hidden bool) (context.Context, bool, *PhotoHiddenState, CompiledQuery, compiledQueryFragment, error) {
	var state *PhotoHiddenState
	if browser, _ := ctx.Value(visiblePersonContextKey{}).(bool); browser {
		var err error
		state, err = s.photoHiddenStateTx(ctx, q)
		if err != nil {
			return ctx, false, nil, CompiledQuery{}, compiledQueryFragment{}, err
		}
		hidden = state.ExpiresAt == nil
	}
	ctx = WithVisiblePeopleOnly(WithQueryDefaultTimezone(ctx, scope.Zone))
	ctx = context.WithValue(ctx, queryPeopleHiddenKey{}, hidden)
	if err := requireDocumentDateIndex(ctx, q); err != nil {
		return ctx, hidden, state, CompiledQuery{}, compiledQueryFragment{}, err
	}
	compiled := CompiledQuery{Query: scope.Query}
	if s.postgresSchema == "" {
		if scope.Coverage.Configuration == "configured" {
			if err := validateSnapshotCoverageProfile(ctx, q, scope.Coverage); err != nil {
				return ctx, hidden, state, compiled, compiledQueryFragment{}, err
			}
		}
		var err error
		compiled, err = (queryCompiler{}).compile(ctx, scope.Query, queryResolver{q: q})
		if err != nil {
			return ctx, hidden, state, compiled, compiledQueryFragment{}, err
		}
	}
	population, err := s.timelinePopulation(ctx, q, compiled, generation, scope.Coverage, hidden)
	return ctx, hidden, state, compiled, population, err
}

func (s *QuerySnapshotService) TimelineDays(ctx context.Context, owner string, scope TimelineScope, from, to civil.Date) (TimelineDays, error) {
	result := TimelineDays{Days: []TimelineDayCount{}, Months: []TimelineMonthCount{}}
	if s.store != nil && s.store.postgresSchema != "" {
		if err := validatePostgresTimelineScope(scope.Query); err != nil {
			return result, err
		}
	}
	if from.Compare(to) > 0 {
		return result, ErrTimelineRangeOrder
	}
	if from.AddDays(maxTimelineRangeDates-1).Compare(to) < 0 {
		return result, ErrTimelineRangeTooLarge
	}
	loc, err := query.LoadZone(scope.Zone)
	if err != nil {
		return result, err
	}
	if _, _, err := query.RangeBounds(from, to, loc); err != nil {
		return result, fmt.Errorf("%w: %v", ErrTimelineDateNotInZone, err)
	}
	ctx, cancel := context.WithTimeout(ctx, querySnapshotBuildTimeout)
	defer cancel()
	build, err := s.admitNewBuild(owner, cancel, true, "")
	if err != nil {
		return result, err
	}
	defer s.finishBuild(build, false)
	err = s.store.withTimelineRead(ctx, func(q metadataQuerier, generation string) error {
		scoped, _, _, compiled, population, err := s.store.timelineReadPopulation(ctx, q, generation, scope, false)
		if err != nil {
			return err
		}
		postgres := s.store.postgresSchema != ""
		coarseIdentity := "version_id"
		if compiled.Query.Filters.CollapseDuplicates {
			coarseIdentity = "blob_hash"
		}

		tail := func(b *timelineBinds) string {
			condition := timelineEligibleSQL(b, scope.DateKinds)
			return `SELECT 'd',day,COUNT(*),CAST(SUM(CASE WHEN tier=1 THEN 1 ELSE 0 END) AS BIGINT),CAST(SUM(CASE WHEN tier=2 THEN 1 ELSE 0 END) AS BIGINT) FROM placed WHERE placement_rank=1 AND tier<3 GROUP BY day
 UNION ALL SELECT 'm',substr(day,1,7),COUNT(DISTINCT ` + coarseIdentity + `),0,0 FROM eligible WHERE tier=3 GROUP BY substr(day,1,7)
 UNION ALL SELECT 'u','',COUNT(*),0,0 FROM population m JOIN document_event_heads h ON h.content_version_id=m.content_version_id WHERE ` + currentDocumentDateHead + ` AND NOT EXISTS (SELECT 1 FROM document_date_keys k WHERE k.generation_id=h.generation_id AND ` + condition + `) ORDER BY 1,2`
		}
		statement, args, err := timelinePlacedSQL(population, generation, scope, from, to, loc, postgres, tail)
		if err != nil {
			return err
		}

		rows, err := q.QueryContext(scoped, statement, args...)
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
