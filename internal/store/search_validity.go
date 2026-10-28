package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"

	"go.kenn.io/docbank/document"
)

// SearchValidityNode contains only current live authority inside a search fence.
type SearchValidityNode struct {
	NodeID                 int64
	ContentVersionID, Path string
	Window                 document.EffectiveWindow
	Declared               bool
	Successors             []int64
}

// LoadSearchValidity reads the bounded fence once. Out-of-fence relationships
// neither declare a result nor affect its standing.
func (s *Store) LoadSearchValidity(ctx context.Context, contentVersionIDs []string) (map[int64]SearchValidityNode, error) {
	if len(contentVersionIDs) > 4096 {
		return nil, fmt.Errorf("search validity node budget: %w", ErrInvalidSupersession)
	}
	encoded, err := json.Marshal(contentVersionIDs)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `WITH fence_input AS (`+s.jsonArrayRowsSQL()+`), fence AS (
 SELECT n.id,n.current_version_id FROM nodes n JOIN fence_input f ON f.value=n.current_version_id
 WHERE n.kind='file' AND n.trashed_at IS NULL GROUP BY n.id
 ) SELECT f.id,f.current_version_id,w.node_id,w.effective_from,w.effective_to,
 s.successor_node_id,s.note,s.recorded_at FROM fence f
 LEFT JOIN node_effective_windows w ON w.node_id=f.id
 LEFT JOIN node_supersessions s ON s.predecessor_node_id=f.id AND s.successor_node_id IN (SELECT id FROM fence)
 ORDER BY f.id,s.successor_node_id LIMIT 69633`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	graph := map[int64]SearchValidityNode{}
	for rows.Next() {
		var id int64
		var version string
		var windowID, next sql.NullInt64
		var from, to, note, recorded sql.NullString
		if err = rows.Scan(&id, &version, &windowID, &from, &to, &next, &note, &recorded); err != nil {
			return nil, err
		}
		n := graph[id]
		n.NodeID = id
		n.ContentVersionID = version
		n.Window = document.EffectiveWindow{From: from.String, To: to.String}
		n.Declared = n.Declared || windowID.Valid || next.Valid
		if windowID.Valid {
			if (from.Valid && from.String == "") || (to.Valid && to.String == "") {
				return nil, ErrInvalidSupersession
			}
			if err = document.ValidateEffectiveWindow(n.Window); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalidSupersession, err)
			}
			if n.Window.From == "" && n.Window.To == "" {
				return nil, ErrInvalidSupersession
			}
		}
		if next.Valid {
			if err = validSupersessionNote(note.String); err != nil {
				return nil, err
			}
			if err = validateMetadataTime("supersession recorded_at", recorded.String); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalidSupersession, err)
			}
			n.Successors = append(n.Successors, next.Int64)
		}
		graph[id] = n
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if len(graph) > 4096 {
		return nil, ErrInvalidSupersession
	}
	pending := map[int64]int{}
	queue := []int64{}
	ids := []int64{}
	for id, n := range graph {
		if len(n.Successors) > 16 {
			return nil, ErrInvalidSupersession
		}
		for _, next := range n.Successors {
			if id == next {
				return nil, ErrInvalidSupersession
			}
			pending[next]++
			if pending[next] > 16 {
				return nil, ErrInvalidSupersession
			}
			successor := graph[next]
			successor.Declared = true
			graph[next] = successor
		}
		ids = append(ids, id)
	}
	for id := range graph {
		if pending[id] == 0 {
			queue = append(queue, id)
		}
	}
	count := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		count++
		for _, next := range graph[id].Successors {
			pending[next]--
			if pending[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if count != len(graph) {
		return nil, ErrInvalidSupersession
	}
	paths, err := s.pathsOf(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		n := graph[id]
		n.Path = paths[i]
		graph[id] = n
	}
	return graph, tx.Commit()
}
