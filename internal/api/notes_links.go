package api

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/document/notemarkdown"
	"go.kenn.io/docbank/internal/ingest"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
	"golang.org/x/text/unicode/norm"
)

type noteLinksCursor struct {
	Kind      string                 `json:"kind"`
	NodeID    int64                  `json:"node_id"`
	Direction string                 `json:"direction"`
	VersionID string                 `json:"version_id"`
	Recipe    string                 `json:"recipe"`
	Position  store.NoteLinkPosition `json:"position"`
	IssuedAt  int64                  `json:"iat"`
}

type noteLinkResponse struct {
	SourcePath string   `json:"source_path"`
	Line       int      `json:"line"`
	Form       string   `json:"form"`
	Raw        string   `json:"raw"`
	Label      string   `json:"label"`
	Fragment   string   `json:"fragment"`
	TargetPath *string  `json:"target_path" nullable:"true"`
	State      string   `json:"state"`
	Candidates []string `json:"candidates,omitempty"`
}

func registerNoteLinksRoute(api huma.API, d Deps, cursors *documentQueryService) {
	type response struct {
		Body struct {
			Path             string             `json:"path"`
			NodeID           int64              `json:"node_id"`
			ContentVersionID string             `json:"content_version_id"`
			Direction        string             `json:"direction"`
			Links            []noteLinkResponse `json:"links"`
			NextCursor       string             `json:"next_cursor,omitempty"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "getNoteLinks", Method: http.MethodGet, Path: "/api/v1/notes/{node_id}/links", Summary: "Resolve current note links"}, func(ctx context.Context, input *struct {
		NodeID    int64  `path:"node_id"`
		Direction string `query:"direction" default:"outgoing" enum:"outgoing,incoming,unresolved"`
		Limit     int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
		Cursor    string `query:"cursor"`
	}) (*response, error) {
		var cursor noteLinksCursor
		var after *store.NoteLinkPosition
		if input.Cursor != "" {
			payload, err := cursors.verifyCursorEnvelope(input.Cursor)
			if err != nil {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note links cursor")
			}
			if err := json.Unmarshal(payload, &cursor, json.RejectUnknownMembers(true)); err != nil || cursor.Kind != "note_links" || cursor.NodeID != input.NodeID || cursor.Direction != input.Direction || cursor.Recipe != notemarkdown.Recipe || cursor.VersionID == "" {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note links cursor")
			}
			issued := time.Unix(cursor.IssuedAt, 0)
			if cursors.now().Before(issued) || !cursors.now().Before(issued.Add(documentCursorTTL)) {
				return nil, NewError(http.StatusUnprocessableEntity, "cursor_expired", "Note links cursor expired")
			}
			after = &cursor.Position
		}
		view, err := d.Store.NoteVersion(ctx, input.NodeID, "")
		if err != nil {
			return nil, FromStoreError(err)
		}
		if cursor.VersionID != "" && cursor.VersionID != view.Version.ID {
			return nil, NewError(http.StatusConflict, "stale_cursor", "Note version changed")
		}
		if view.ParseState != "" && view.ParseState != "ok" {
			return nil, NewError(http.StatusUnprocessableEntity, "note_"+view.ParseState, "Note structure is unavailable for these original bytes")
		}
		_, note, state, err := processing.ReadNoteOriginal(ctx, d.Blobs, view)
		if err != nil {
			return nil, FromStoreError(err)
		}
		if state != "ok" {
			return nil, NewError(http.StatusUnprocessableEntity, "note_"+state, "Note structure is unavailable for these original bytes")
		}
		watches := make([]store.NoteWatch, 0, len(d.Cfg.Watches))
		filters := make([]func(string) bool, 0, len(d.Cfg.Watches))
		for _, watch := range d.Cfg.Watches {
			watches = append(watches, store.NoteWatch{Name: watch.Name, Destination: watch.Destination})
			f, err := ingest.WatchExcluded(watch)
			if err != nil {
				return nil, err
			}
			filters = append(filters, f)
		}
		excluded := func(i int, p string) bool {
			if i >= len(watches) {
				return false
			}
			root := norm.NFC.String(path.Clean(watches[i].Destination))
			return filters[i](strings.TrimPrefix(strings.TrimPrefix(p, root), "/"))
		}
		var aliases []string
		if note.Frontmatter != nil {
			aliases = note.Frontmatter.Aliases
		}
		authored := append([]notemarkdown.Link{}, note.Links...)
		page, err := d.Store.NoteLinks(ctx, input.NodeID, input.Direction, view.Version.ID, authored, aliases, watches, excluded, after, input.Limit)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &response{}
		out.Body.Path, out.Body.NodeID, out.Body.ContentVersionID, out.Body.Direction = page.Path, input.NodeID, page.Version, input.Direction
		out.Body.Links = make([]noteLinkResponse, 0, len(page.Items))
		for _, l := range page.Items {
			out.Body.Links = append(out.Body.Links, noteLinkResponse{SourcePath: l.SourcePath, Line: l.Line, Form: l.Form, Raw: l.Raw, Label: l.Label, Fragment: l.Fragment, TargetPath: l.TargetPath, State: l.State, Candidates: l.Candidates})
		}
		if page.Next != nil {
			payload, err := json.Marshal(noteLinksCursor{Kind: "note_links", NodeID: input.NodeID, Direction: input.Direction, VersionID: page.Version, Recipe: notemarkdown.Recipe, Position: *page.Next, IssuedAt: cursors.now().Unix()})
			if err != nil {
				return nil, err
			}
			out.Body.NextCursor = cursors.signCursorEnvelope(payload)
			if len(out.Body.NextCursor) > MaxDocumentCursorBytes {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Note cursor exceeds size limit")
			}
		}
		return out, nil
	})
}
