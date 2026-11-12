package store

import (
	"context"
	"database/sql"
	"errors"

	"go.kenn.io/docbank/document/notemarkdown"
)

// NoteVersionView binds a retained original to its live note and path.
type NoteVersionView struct {
	Node          Node
	Version       ContentVersion
	Path          string
	FilenameTitle string
	SourceState   string
	Watch         string
	InSync        bool
	ParseState    string
}

// NoteVersion selects a node-owned original and its live placement atomically.
// An empty versionID selects the current version.
func (s *Store) NoteVersion(ctx context.Context, nodeID int64, versionID string) (NoteVersionView, error) {
	var view NoteVersionView
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		view.Node, err = nodeByIDTx(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		if !IsNoteCandidate(view.Node) || view.Node.Size > MaxNoteBytes {
			return ErrNotFound
		}
		view.FilenameTitle, _ = splitSuffix(view.Node.Name)
		err = tx.QueryRowContext(ctx, noteTreeCTE+`SELECT path FROM note_tree WHERE id=$1`, nodeID).Scan(&view.Path)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if versionID == "" {
			versionID = view.Node.CurrentVersionID
		}
		if validateUUIDv4(versionID) != nil {
			return ErrNotFound
		}
		view.Version, err = scanContentVersion(tx.QueryRowContext(ctx,
			`SELECT `+contentVersionCols+` FROM content_versions WHERE version_id=$1 AND node_id=$2`, versionID, nodeID))
		if err != nil {
			return err
		}
		if view.Version.Size > MaxNoteBytes {
			return ErrNotFound
		}
		if view.Version.ID == view.Node.CurrentVersionID {
			err = tx.QueryRowContext(ctx, `SELECT state FROM note_parses WHERE version_id=$1 AND recipe=$2`, versionID, notemarkdown.Recipe).Scan(&view.ParseState)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if view.ParseState != "" && !validNoteState(view.ParseState) {
				return errors.New("invalid stored note parse state")
			}
		}
		var hash string
		err = tx.QueryRowContext(ctx, `SELECT watch_name, blob_hash FROM watch_sources WHERE node_id=$1 ORDER BY watch_name,source_ref LIMIT 1`, nodeID).Scan(&view.Watch, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			view.SourceState = "not_watched"
			return nil
		}
		if err != nil {
			return err
		}
		view.InSync = hash == view.Node.BlobHash
		return tx.QueryRowContext(ctx, `SELECT CASE WHEN `+sourcePresentSQL+` THEN 'present' ELSE 'gone' END FROM nodes n WHERE n.id=$1`, nodeID).Scan(&view.SourceState)
	})
	return view, err
}
