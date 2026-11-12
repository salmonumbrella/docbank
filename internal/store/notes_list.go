package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"go.kenn.io/docbank/document/notemarkdown"
)

type NoteFieldFilter struct{ Key, Value string }

type NoteListQuery struct {
	UnderPath, Sort, Direction, ModifiedSince, ModifiedBefore string
	Recursive, SourcePresent                                  bool
	PageSize                                                  int
	Fields                                                    []NoteFieldFilter
}

type NoteListPosition struct {
	Path, Value, VersionID string
	NodeID                 int64
	ParsedTitle            bool
}

type NoteFolder struct {
	Path  string
	Notes int
}

type NoteListPage struct {
	Items   []NoteSummary
	Folders []NoteFolder
	Last    NoteListPosition
	HasNext bool
}

// ListNotes applies note filters before paging and captures each boundary in
// the same snapshot as the page. The caller authenticates the boundary.
func NormalizeNoteListQuery(q NoteListQuery) (NoteListQuery, error) {
	if q.UnderPath == "" {
		q.UnderPath = "/"
	}
	canonical, err := canonicalBatchMovePath(q.UnderPath)
	if err != nil || !strings.HasPrefix(q.UnderPath, "/") || len(canonical) > MaxWalkPathBytes || q.PageSize < 1 || q.PageSize > 250 || len(q.Fields) > 8 || (q.Sort != "path" && q.Sort != "title" && q.Sort != "modified_at") || (q.Direction != "asc" && q.Direction != "desc") {
		return NoteListQuery{}, ErrInvalidDocumentQuery
	}
	q.UnderPath = canonical
	q.ModifiedSince, q.ModifiedBefore, err = NormalizeSearchTimeBounds(q.ModifiedSince, q.ModifiedBefore)
	if err != nil {
		return NoteListQuery{}, fmt.Errorf("%w: %v", ErrInvalidDocumentQuery, err)
	}
	for _, f := range q.Fields {
		if len(f.Key) == 0 || len(f.Key) > 128 || len(f.Value) > 1024 || !utf8.ValidString(f.Key) || !utf8.ValidString(f.Value) || strings.ContainsRune(f.Key, 0) || strings.ContainsRune(f.Value, 0) {
			return NoteListQuery{}, ErrInvalidDocumentQuery
		}
	}
	q.Fields = slices.Clone(q.Fields)
	slices.SortFunc(q.Fields, func(a, b NoteFieldFilter) int {
		if order := strings.Compare(a.Key, b.Key); order != 0 {
			return order
		}
		return strings.Compare(a.Value, b.Value)
	})
	return q, nil
}

func (s *Store) ListNotes(ctx context.Context, q NoteListQuery, boundary *NoteListPosition) (NoteListPage, error) {
	q, err := NormalizeNoteListQuery(q)
	if err != nil {
		return NoteListPage{}, err
	}
	var page NoteListPage
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		root, err := s.nodeByPath(ctx, tx, q.UnderPath)
		if err != nil {
			return err
		}
		if !root.IsDir() {
			return ErrNotDir
		}
		args := []any{notemarkdown.Recipe, MaxNoteBytes}
		add := func(v any) string {
			args = append(args, v)
			return fmt.Sprintf("$%d", len(args))
		}
		prefix := q.UnderPath
		if prefix != "/" {
			prefix += "/"
		}
		prefixArg := add(prefix)
		where := ` WHERE substr(t.path,1,length(` + prefixArg + `))=` + prefixArg
		if q.SourcePresent {
			where += ` AND ` + sourcePresentSQL
		}
		if q.ModifiedSince != "" {
			where += ` AND n.modified_at>=` + add(q.ModifiedSince)
		}
		if q.ModifiedBefore != "" {
			where += ` AND n.modified_at<` + add(q.ModifiedBefore)
		}
		for _, f := range q.Fields {
			where += ` AND EXISTS(SELECT 1 FROM note_fields f WHERE f.version_id=cv.version_id AND p.version_id IS NOT NULL AND f.key=` + add(f.Key) + ` AND f.value=` + add(f.Value) + `)`
		}
		cte := noteTreeCTE + `, name_scan(id,name,pos,last_dot) AS (
 SELECT id,name,1,0 FROM nodes WHERE kind='file'
 UNION ALL SELECT id,name,pos+1,CASE WHEN pos>1 AND substr(name,pos,1)='.' THEN pos ELSE last_dot END FROM name_scan WHERE pos<=length(name)
), suffix AS (SELECT id,max(last_dot) AS dot FROM name_scan GROUP BY id), eligible AS (
 SELECT t.path,n.id,cv.version_id,cv.blob_hash,cv.size,n.modified_at,n.name,
 CASE WHEN COALESCE(p.title,'')<>'' THEN p.title WHEN suffix.dot>0 THEN substr(n.name,1,suffix.dot-1) ELSE n.name END AS title,
 CASE WHEN COALESCE(p.title,'')<>'' THEN 1 ELSE 0 END AS parsed_title,
 CASE WHEN NOT EXISTS(SELECT 1 FROM watch_sources ws WHERE ws.node_id=n.id) THEN 'not_watched' WHEN ` + sourcePresentSQL + ` THEN 'present' ELSE 'gone' END AS source_state
 FROM note_tree t JOIN nodes n ON n.id=t.id JOIN content_versions cv ON cv.version_id=n.current_version_id
 JOIN suffix ON suffix.id=n.id LEFT JOIN note_parses p ON p.version_id=cv.version_id AND p.recipe=$1` + where + ` AND ` + noteCandidateSQL + ` AND cv.size<=$2)`
		if !q.Recursive {
			rows, err := tx.QueryContext(ctx, cte+` SELECT path FROM eligible ORDER BY path`, args...)
			if err != nil {
				return err
			}
			counts := make(map[string]int)
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					_ = rows.Close()
					return err
				}
				rel := strings.TrimPrefix(p, prefix)
				if name, _, ok := strings.Cut(rel, "/"); ok {
					counts[prefix+name]++
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for p, n := range counts {
				page.Folders = append(page.Folders, NoteFolder{Path: p, Notes: n})
			}
			slices.SortFunc(page.Folders, func(a, b NoteFolder) int { return strings.Compare(a.Path, b.Path) })
		}
		selectWhere := " WHERE 1=1"
		if !q.Recursive {
			if s.postgresSchema != "" {
				selectWhere += ` AND strpos(substr(path,length(` + prefixArg + `)+1),'/')=0`
			} else {
				selectWhere += ` AND instr(substr(path,length(` + prefixArg + `)+1),'/')=0`
			}
		}
		collation := ""
		if s.postgresSchema != "" {
			collation = ` COLLATE "C"`
		}
		key := "path"
		if q.Sort == "title" {
			key = "title"
		} else if q.Sort == "modified_at" {
			key = "modified_at"
		}
		if boundary != nil {
			value := boundary.Value
			if q.Sort == "path" {
				value = boundary.Path
			} else if q.Sort == "title" && boundary.ParsedTitle {
				err := tx.QueryRowContext(ctx, `SELECT title FROM note_parses WHERE version_id=$1 AND recipe=$2 AND title<>''`, boundary.VersionID, notemarkdown.Recipe).Scan(&value)
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNoteCursorStale
				}
				if err != nil {
					return err
				}
			}
			op := ">"
			if q.Direction == "desc" {
				op = "<"
			}
			selectWhere += ` AND (` + key + collation + `,path` + collation + `,id) ` + op + ` (` + add(value) + `,` + add(boundary.Path) + `,` + add(boundary.NodeID) + `)`
		}
		direction := "ASC"
		if q.Direction == "desc" {
			direction = "DESC"
		}
		rows, err := tx.QueryContext(ctx, cte+` SELECT path,id,version_id,blob_hash,size,modified_at,title,parsed_title,source_state FROM eligible`+selectWhere+` ORDER BY `+key+collation+` `+direction+`,path`+collation+` `+direction+`,id `+direction+` LIMIT `+add(q.PageSize+1), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item NoteSummary
			var parsed int
			if err := rows.Scan(&item.Path, &item.NodeID, &item.ContentVersionID, &item.SHA256, &item.Size, &item.ModifiedAt, &item.Title, &parsed, &item.SourceState); err != nil {
				_ = rows.Close()
				return err
			}
			if len(page.Items) == q.PageSize {
				page.HasNext = true
				break
			}
			item.Tags = []string{}
			page.Items = append(page.Items, item)
			value := item.Path
			if q.Sort == "title" {
				value = item.Title
			} else if q.Sort == "modified_at" {
				value = item.ModifiedAt
			}
			page.Last = NoteListPosition{Path: item.Path, Value: value, NodeID: item.NodeID, VersionID: item.ContentVersionID, ParsedTitle: q.Sort == "title" && parsed != 0}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for i := range page.Items {
			item := &page.Items[i]
			if err := queryRows(ctx, tx, `SELECT value FROM note_fields WHERE version_id=$1 AND key='tags' AND EXISTS(SELECT 1 FROM note_parses WHERE version_id=$1 AND recipe=$2) ORDER BY ordinal`, []any{item.ContentVersionID, notemarkdown.Recipe}, func(rows *sql.Rows) error {
				var value string
				if err := rows.Scan(&value); err != nil {
					return err
				}
				item.Tags = append(item.Tags, value)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return page, err
}

var ErrNoteCursorStale = errors.New("note cursor title anchor is no longer available")
