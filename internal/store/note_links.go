package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"go.kenn.io/docbank/document/notemarkdown"
	"golang.org/x/text/unicode/norm"
)

type NoteLink struct {
	SourcePath, Form, Raw, Label, Fragment, State string
	Line                                          int
	TargetPath                                    *string
	Candidates                                    []string
}

type NoteLinkPosition struct {
	VersionID string
	Ordinal   int
}

type NoteLinksPage struct {
	Items   []NoteLink
	Next    *NoteLinkPosition
	Path    string
	Version string
}

type noteRawLink struct {
	version, sourcePath, form, raw, target, fragment, label string
	ordinal, line                                           int
}

// NoteLinks resolves indexed raw links against current placement. The raw-row
// position advances even when candidates do not match the requested direction.
func (s *Store) NoteLinks(ctx context.Context, nodeID int64, direction, selectedVersion string, authored []notemarkdown.Link, targetAliases []string, watches []NoteWatch, excluded func(int, string) bool, after *NoteLinkPosition, limit int) (NoteLinksPage, error) {
	if (direction != "outgoing" && direction != "incoming" && direction != "unresolved") || limit < 1 || limit > 100 {
		return NoteLinksPage{}, ErrInvalidDocumentQuery
	}
	destinations, err := noteDestinations(watches)
	if err != nil {
		return NoteLinksPage{}, err
	}
	var page NoteLinksPage
	page.Items = []NoteLink{}
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		n, err := nodeByIDTx(ctx, tx, nodeID)
		if err != nil || !IsNoteCandidate(n) || n.Size > MaxNoteBytes {
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, noteTreeCTE+`SELECT path FROM note_tree WHERE id=$1`, nodeID).Scan(&page.Path); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		page.Version = n.CurrentVersionID
		if selectedVersion != "" && selectedVersion != page.Version {
			return ErrStaleRevision
		}
		var present bool
		if err := tx.QueryRowContext(ctx, `SELECT `+sourcePresentSQL+` FROM nodes n WHERE n.id=$1`, nodeID).Scan(&present); err != nil {
			return err
		}
		if !present {
			return ErrNotFound
		}
		collection := noteCollection(page.Path, watches, destinations)
		root := "/"
		if collection < len(destinations) {
			root = destinations[collection]
		}
		keys := []string{noteStemKey(n.Name)}
		for _, alias := range targetAliases {
			keys = append(keys, strings.ToLower(norm.NFC.String(alias)))
		}
		if direction == "incoming" {
			if err := queryRows(ctx, tx, `SELECT a.alias_key FROM note_aliases a JOIN note_parses p ON p.version_id=a.version_id AND p.recipe=$2 AND p.state='ok' WHERE a.version_id=$1`, []any{n.CurrentVersionID, notemarkdown.Recipe}, func(rows *sql.Rows) error {
				var key string
				if err := rows.Scan(&key); err != nil {
					return err
				}
				keys = append(keys, key)
				return nil
			}); err != nil {
				return err
			}
		}
		args := []any{notemarkdown.Recipe, MaxNoteBytes}
		add := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
		filter := ""
		if direction == "outgoing" || direction == "unresolved" {
			filter = ` AND n.id=` + add(nodeID)
		} else if direction == "incoming" {
			placeholders := make([]string, len(keys))
			for i, key := range keys {
				placeholders[i] = add(key)
			}
			filter = ` AND (l.target_path IS NOT NULL OR l.target_key IN (` + strings.Join(placeholders, ",") + `))`
		}
		if after != nil {
			if after.Ordinal < 0 || validateUUIDv4(after.VersionID) != nil {
				return ErrInvalidDocumentCursor
			}
			v, ordinal := add(after.VersionID), add(after.Ordinal)
			filter += ` AND (l.version_id>` + v + ` OR (l.version_id=` + v + ` AND l.ordinal>` + ordinal + `))`
		}
		var raw []noteRawLink
		if authored != nil && direction != "incoming" {
			for _, link := range authored {
				if after != nil && (after.VersionID != page.Version || link.Ordinal <= after.Ordinal) {
					continue
				}
				raw = append(raw, noteRawLink{version: page.Version, ordinal: link.Ordinal, sourcePath: page.Path, form: link.Form, raw: link.Raw, target: link.Target, fragment: link.Fragment, label: link.Label, line: link.Line})
				if len(raw) == 501 {
					break
				}
			}
		} else {
			rows, err := tx.QueryContext(ctx, noteTreeCTE+` SELECT l.version_id,l.ordinal,t.path,l.form,l.raw,l.target,l.fragment,l.label,l.line
 FROM note_links l JOIN note_parses p ON p.version_id=l.version_id AND p.recipe=$1 AND p.state='ok'
 JOIN content_versions cv ON cv.version_id=l.version_id JOIN nodes n ON n.id=cv.node_id AND n.current_version_id=cv.version_id
 JOIN note_tree t ON t.id=n.id WHERE `+noteCandidateSQL+` AND cv.size<=$2 AND `+sourcePresentSQL+filter+`
 ORDER BY l.version_id,l.ordinal LIMIT 501`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var item noteRawLink
				if err := rows.Scan(&item.version, &item.ordinal, &item.sourcePath, &item.form, &item.raw, &item.target, &item.fragment, &item.label, &item.line); err != nil {
					_ = rows.Close()
					return err
				}
				raw = append(raw, item)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		for i, link := range raw {
			if i == 500 {
				break
			}
			page.Next = &NoteLinkPosition{VersionID: link.version, Ordinal: link.ordinal}
			if noteCollection(link.sourcePath, watches, destinations) != collection || (excluded != nil && excluded(collection, link.sourcePath)) {
				continue
			}
			resolved, err := s.resolveNoteLink(ctx, tx, link, root, collection, watches, destinations, excluded, page.Path, targetAliases)
			if err != nil {
				return err
			}
			if direction == "incoming" && (resolved.TargetPath == nil || *resolved.TargetPath != page.Path || resolved.State != "resolved") {
				continue
			}
			if direction == "unresolved" && resolved.State != "missing" && resolved.State != "ambiguous" {
				continue
			}
			page.Items = append(page.Items, resolved)
			if len(page.Items) == limit {
				if i == len(raw)-1 {
					page.Next = nil
				}
				return nil
			}
		}
		if len(raw) <= 500 {
			page.Next = nil
		}
		return nil
	})
	return page, err
}

func noteStemKey(name string) string {
	name, _ = splitSuffix(name)
	return strings.ToLower(norm.NFC.String(name))
}

func (s *Store) resolveNoteLink(ctx context.Context, tx *sql.Tx, link noteRawLink, root string, collection int, watches []NoteWatch, destinations []string, excluded func(int, string) bool, selectedPath string, selectedAliases []string) (NoteLink, error) {
	result := NoteLink{SourcePath: link.sourcePath, Form: link.form, Raw: link.raw, Label: link.label, Fragment: link.fragment, Line: link.line, State: "missing"}
	resolved, key := noteLinkIdentity(link.sourcePath, link.form, link.target)
	if resolved == nil && key == nil {
		result.State = "unsupported"
		if u, err := url.Parse(link.target); err == nil && (u.Scheme != "" || strings.HasPrefix(link.target, "//")) {
			result.State = "external"
		}
		return result, nil
	}
	target := link.target
	if target == "" {
		target = link.sourcePath
	} else if strings.HasPrefix(target, "/") {
		target = path.Join(root, strings.TrimPrefix(target, "/"))
	} else {
		target = path.Join(path.Dir(link.sourcePath), target)
	}
	within := func(p string) bool {
		return (root == "/" || strings.HasPrefix(p, root+"/")) && noteCollection(p, watches, destinations) == collection && (excluded == nil || !excluded(collection, p))
	}
	lookup := func(p string) (bool, error) {
		if !within(p) {
			return false, nil
		}
		n, err := s.nodeByPath(ctx, tx, p)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotDir) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !IsNoteCandidate(n) || n.Size > MaxNoteBytes {
			return false, nil
		}
		var present bool
		err = tx.QueryRowContext(ctx, `SELECT `+sourcePresentSQL+` FROM nodes n WHERE n.id=$1`, n.ID).Scan(&present)
		return present, err
	}
	paths := []string{target}
	if path.Ext(target) == "" {
		paths = append(paths, target+".md")
	}
	for _, candidate := range paths {
		candidate, err := canonicalBatchMovePath(candidate)
		if err != nil {
			continue
		}
		ok, err := lookup(candidate)
		if err != nil {
			return NoteLink{}, err
		}
		if ok {
			result.State, result.TargetPath = "resolved", &candidate
			return result, nil
		}
	}
	if key == nil {
		return result, nil
	}
	var stems, aliases []string
	err := queryRows(ctx, tx, noteTreeCTE+` SELECT t.path,n.name,EXISTS(SELECT 1 FROM note_aliases a JOIN note_parses p ON p.version_id=a.version_id AND p.recipe=$3 AND p.state='ok' WHERE a.version_id=cv.version_id AND a.alias_key=$1)
 FROM note_tree t JOIN nodes n ON n.id=t.id JOIN content_versions cv ON cv.version_id=n.current_version_id
 WHERE `+noteCandidateSQL+` AND cv.size<=$2 AND `+sourcePresentSQL, []any{*key, MaxNoteBytes, notemarkdown.Recipe}, func(rows *sql.Rows) error {
		var p, name string
		var alias bool
		if err := rows.Scan(&p, &name, &alias); err != nil {
			return err
		}
		if within(p) {
			if noteStemKey(name) == *key {
				stems = append(stems, p)
			} else if alias {
				aliases = append(aliases, p)
			}
		}
		return nil
	})
	if err != nil {
		return NoteLink{}, err
	}
	if within(selectedPath) && !slices.Contains(stems, selectedPath) && !slices.Contains(aliases, selectedPath) {
		for _, alias := range selectedAliases {
			if strings.ToLower(norm.NFC.String(alias)) == *key {
				aliases = append(aliases, selectedPath)
				break
			}
		}
	}
	matches := stems
	if len(matches) == 0 {
		matches = aliases
	}
	slices.Sort(matches)
	if len(matches) == 1 {
		result.State, result.TargetPath = "resolved", &matches[0]
	} else if len(matches) > 1 {
		result.State, result.Candidates = "ambiguous", matches[:min(5, len(matches))]
	}
	return result, nil
}
