package api

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/document/notemarkdown"
	"go.kenn.io/docbank/internal/store"
)

type noteListCursor struct {
	Kind        string   `json:"kind"`
	QueryHash   [32]byte `json:"q"`
	Path        []byte   `json:"path"`
	Value       []byte   `json:"value,omitempty"`
	VersionID   string   `json:"version_id"`
	NodeID      int64    `json:"node_id"`
	ParsedTitle bool     `json:"parsed_title"`
	IssuedAt    int64    `json:"iat"`
}

func registerNoteListRoute(api huma.API, d Deps, cursors *documentQueryService) {
	type folder struct {
		Path  string `json:"path"`
		Notes int    `json:"notes"`
	}
	type response struct {
		Body struct {
			Items      []NoteSummary `json:"items"`
			Folders    []folder      `json:"folders,omitempty"`
			NextCursor string        `json:"next_cursor,omitempty"`
		}
	}
	huma.Register(api, huma.Operation{
		OperationID: "listNotes", Method: http.MethodGet, Path: "/api/v1/notes",
		Summary: "List current Markdown notes",
	}, func(ctx context.Context, input *struct {
		UnderPath      string   `query:"under_path" default:"/"`
		Recursive      bool     `query:"recursive" default:"true"`
		Field          []string `query:"field,explode" maxItems:"8"`
		SourcePresent  bool     `query:"source_present"`
		ModifiedSince  string   `query:"modified_since"`
		ModifiedBefore string   `query:"modified_before"`
		Sort           string   `query:"sort" default:"path" enum:"path,modified_at,title"`
		Direction      string   `query:"direction" default:"asc" enum:"asc,desc"`
		PageSize       int      `query:"page_size" default:"100" minimum:"1" maximum:"250"`
		Cursor         string   `query:"cursor"`
	}) (*response, error) {
		q := store.NoteListQuery{UnderPath: input.UnderPath, Recursive: input.Recursive, SourcePresent: input.SourcePresent, ModifiedSince: input.ModifiedSince, ModifiedBefore: input.ModifiedBefore, Sort: input.Sort, Direction: input.Direction, PageSize: input.PageSize}
		if len(input.Field) > 8 {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", "At most eight field filters are allowed")
		}
		for _, f := range input.Field {
			key, value, ok := strings.Cut(f, ":")
			if !ok {
				return nil, NewError(http.StatusUnprocessableEntity, "validation", "A field filter must contain key:value")
			}
			q.Fields = append(q.Fields, store.NoteFieldFilter{Key: key, Value: value})
		}
		q, err := store.NormalizeNoteListQuery(q)
		if err != nil {
			return nil, FromStoreError(err)
		}
		queryBytes, err := json.Marshal(struct {
			Recipe string
			Query  store.NoteListQuery
		}{notemarkdown.Recipe, q})
		if err != nil {
			return nil, err
		}
		queryHash := sha256.Sum256(queryBytes)
		var boundary *store.NoteListPosition
		if input.Cursor != "" {
			payload, err := cursors.verifyCursorEnvelope(input.Cursor)
			if err != nil {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note list cursor")
			}
			var cursor noteListCursor
			if err := json.Unmarshal(payload, &cursor, json.RejectUnknownMembers(true)); err != nil || cursor.Kind != "note_list" || cursor.QueryHash != queryHash || cursor.NodeID <= 0 || len(cursor.Path) == 0 || len(cursor.Path) > store.MaxWalkPathBytes {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note list cursor")
			}
			issued := time.Unix(cursor.IssuedAt, 0)
			if cursors.now().Before(issued) || !cursors.now().Before(issued.Add(documentCursorTTL)) {
				return nil, NewError(http.StatusUnprocessableEntity, "cursor_expired", "Note list cursor expired")
			}
			boundary = &store.NoteListPosition{Path: string(cursor.Path), Value: string(cursor.Value), VersionID: cursor.VersionID, NodeID: cursor.NodeID, ParsedTitle: cursor.ParsedTitle}
		}
		page, err := d.Store.ListNotes(ctx, q, boundary)
		if errors.Is(err, store.ErrNoteCursorStale) {
			return nil, NewError(http.StatusConflict, "stale_cursor", err.Error())
		}
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &response{}
		out.Body.Items = make([]NoteSummary, 0, len(page.Items))
		for _, n := range page.Items {
			out.Body.Items = append(out.Body.Items, NoteSummary{Path: n.Path, NodeID: n.NodeID, ContentVersionID: n.ContentVersionID, SHA256: n.SHA256, Size: n.Size, ModifiedAt: n.ModifiedAt, Title: n.Title, Tags: n.Tags, SourceState: n.SourceState})
		}
		if !q.Recursive {
			out.Body.Folders = make([]folder, 0, len(page.Folders))
			for _, f := range page.Folders {
				out.Body.Folders = append(out.Body.Folders, folder{Path: f.Path, Notes: f.Notes})
			}
		}
		if page.HasNext {
			value := page.Last.Value
			if page.Last.ParsedTitle || q.Sort == "path" {
				value = ""
			}
			payload, err := json.Marshal(noteListCursor{Kind: "note_list", QueryHash: queryHash, Path: []byte(page.Last.Path), Value: []byte(value), VersionID: page.Last.VersionID, NodeID: page.Last.NodeID, ParsedTitle: page.Last.ParsedTitle, IssuedAt: cursors.now().Unix()})
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
