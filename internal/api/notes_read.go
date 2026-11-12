package api

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/document/notemarkdown"
	"go.kenn.io/docbank/internal/processing"
)

type noteReadRange struct {
	Start     int `json:"start"`
	End       int `json:"end"`
	LineStart int `json:"line_start"`
	LineEnd   int `json:"line_end"`
}

// noteOffsetParam preserves whether the optional absolute offset was supplied.
// Huma query parameters cannot themselves be pointers.
type noteOffsetParam struct {
	Value int
	IsSet bool
}

func (noteOffsetParam) Schema(r huma.Registry) *huma.Schema {
	return huma.SchemaFromType(r, reflect.TypeFor[int]())
}

func (p *noteOffsetParam) Receiver() reflect.Value      { return reflect.ValueOf(&p.Value).Elem() }
func (p *noteOffsetParam) OnParamSet(isSet bool, _ any) { p.IsSet = isSet }

type noteReadSource struct {
	State  string `json:"state"`
	Watch  string `json:"watch,omitempty"`
	InSync bool   `json:"in_sync"`
}

type noteReadHeading struct {
	Text  string `json:"text"`
	Level int    `json:"level"`
	Path  string `json:"path"`
}

type noteTextResponse struct {
	Path             string           `json:"path"`
	NodeID           int64            `json:"node_id"`
	ContentVersionID string           `json:"content_version_id"`
	SHA256           string           `json:"sha256"`
	Size             int64            `json:"size"`
	Title            string           `json:"title"`
	Frontmatter      noteFrontmatter  `json:"frontmatter" nullable:"true"`
	Source           noteReadSource   `json:"source"`
	Range            noteReadRange    `json:"range"`
	Text             string           `json:"text"`
	Truncated        bool             `json:"truncated"`
	NextOffset       *int             `json:"next_offset,omitempty"`
	Heading          *noteReadHeading `json:"heading,omitempty"`
}

func (noteTextResponse) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Properties["frontmatter"].AdditionalProperties = &huma.Schema{}
	return schema
}

type noteOutlineHeading struct {
	Ordinal   int    `json:"ordinal"`
	Level     int    `json:"level"`
	Text      string `json:"text"`
	Path      string `json:"path"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type noteOutlineCursor struct {
	Kind      string `json:"kind"`
	NodeID    int64  `json:"node_id"`
	VersionID string `json:"version_id"`
	Recipe    string `json:"recipe"`
	Next      int    `json:"next"`
	IssuedAt  int64  `json:"iat"`
}

func registerNoteOutlineRoute(api huma.API, d Deps, cursors *documentQueryService) {
	type response struct {
		Body struct {
			Path             string               `json:"path"`
			NodeID           int64                `json:"node_id"`
			ContentVersionID string               `json:"content_version_id"`
			SHA256           string               `json:"sha256"`
			Headings         []noteOutlineHeading `json:"headings"`
			NextCursor       string               `json:"next_cursor,omitempty"`
		}
	}
	huma.Register(api, huma.Operation{
		OperationID: "getNoteOutline", Method: http.MethodGet, Path: "/api/v1/notes/{node_id}/outline",
		Summary: "Read an original Markdown note outline",
	}, func(ctx context.Context, input *struct {
		NodeID    int64  `path:"node_id"`
		VersionID string `query:"version_id"`
		Cursor    string `query:"cursor"`
	}) (*response, error) {
		versionID, start := input.VersionID, 0
		if input.Cursor != "" {
			payload, err := cursors.verifyCursorEnvelope(input.Cursor)
			if err != nil {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note outline cursor")
			}
			var cursor noteOutlineCursor
			if err := json.Unmarshal(payload, &cursor, json.RejectUnknownMembers(true)); err != nil || cursor.Kind != "note_outline" || cursor.NodeID != input.NodeID || cursor.Recipe != notemarkdown.Recipe || cursor.Next < 1 || (versionID != "" && versionID != cursor.VersionID) {
				return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note outline cursor")
			}
			issued := time.Unix(cursor.IssuedAt, 0)
			if cursors.now().Before(issued) || !cursors.now().Before(issued.Add(documentCursorTTL)) {
				return nil, NewError(http.StatusUnprocessableEntity, "cursor_expired", "Note outline cursor expired")
			}
			versionID, start = cursor.VersionID, cursor.Next
		}
		view, err := d.Store.NoteVersion(ctx, input.NodeID, versionID)
		if err != nil {
			return nil, FromStoreError(err)
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
		if start > len(note.Headings) {
			return nil, NewError(http.StatusUnprocessableEntity, "invalid_cursor", "Invalid note outline cursor")
		}
		out := &response{}
		out.Body.Path, out.Body.NodeID, out.Body.ContentVersionID, out.Body.SHA256 = view.Path, view.Node.ID, view.Version.ID, view.Version.BlobHash
		end := min(start+200, len(note.Headings))
		out.Body.Headings = make([]noteOutlineHeading, 0, end-start)
		for _, h := range note.Headings[start:end] {
			lineEnd := h.SectionEndLine
			if int64(h.SectionEnd) < view.Version.Size {
				lineEnd--
			}
			out.Body.Headings = append(out.Body.Headings, noteOutlineHeading{Ordinal: h.Ordinal, Level: h.Level, Text: h.Text, Path: h.Path, Start: h.Start, End: h.SectionEnd, LineStart: h.Line, LineEnd: lineEnd})
		}
		if end < len(note.Headings) {
			payload, err := json.Marshal(noteOutlineCursor{Kind: "note_outline", NodeID: input.NodeID, VersionID: view.Version.ID, Recipe: notemarkdown.Recipe, Next: end, IssuedAt: cursors.now().Unix()})
			if err != nil {
				return nil, err
			}
			out.Body.NextCursor = cursors.signCursorEnvelope(payload)
		}
		return out, nil
	})
}

func registerNoteTextRoute(api huma.API, d Deps) {
	type output struct{ Body noteTextResponse }
	huma.Register(api, huma.Operation{
		OperationID: "getNoteText", Method: http.MethodGet, Path: "/api/v1/notes/{node_id}/text",
		Summary: "Read original Markdown note bytes",
	}, func(ctx context.Context, input *struct {
		NodeID    int64           `path:"node_id"`
		VersionID string          `query:"version_id"`
		Heading   string          `query:"heading" maxLength:"512"`
		LineStart int             `query:"line_start" minimum:"1"`
		LineEnd   int             `query:"line_end" minimum:"1"`
		Offset    noteOffsetParam `query:"offset" minimum:"0"`
		MaxBytes  int             `query:"max_bytes" default:"262144" minimum:"1" maximum:"524288"`
	}) (*output, error) {
		view, err := d.Store.NoteVersion(ctx, input.NodeID, input.VersionID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		if view.ParseState != "" && view.ParseState != "ok" {
			return nil, NewError(http.StatusUnprocessableEntity, "note_"+view.ParseState, "Note structure is unavailable for these original bytes")
		}
		src, note, state, err := processing.ReadNoteOriginal(ctx, d.Blobs, view)
		if err != nil {
			return nil, FromStoreError(err)
		}
		if state != "ok" {
			return nil, NewError(http.StatusUnprocessableEntity, "note_"+state, "Note structure is unavailable for these original bytes")
		}
		var offset *int
		if input.Offset.IsSet {
			offset = &input.Offset.Value
		}
		selected, err := processing.SelectNoteText(src, note, processing.NoteTextOptions{
			Heading: input.Heading, LineStart: input.LineStart, LineEnd: input.LineEnd,
			Offset: offset, MaxBytes: input.MaxBytes,
		})
		if err != nil {
			if errors.Is(err, processing.ErrNoteRange) {
				return nil, NewError(http.StatusUnprocessableEntity, "range_invalid", err.Error())
			}
			if errors.Is(err, processing.ErrNoteHeadingNotFound) {
				return nil, NewError(http.StatusNotFound, "heading_not_found", err.Error())
			}
			var ambiguous *processing.NoteHeadingAmbiguous
			if errors.As(err, &ambiguous) {
				problem := NewError(http.StatusConflict, "heading_ambiguous", err.Error())
				problem.Candidates = ambiguous.Candidates
				return nil, problem
			}
			return nil, err
		}
		title := note.Title
		if title == "" {
			title = view.FilenameTitle
		}
		var frontmatter noteFrontmatter
		if note.Frontmatter != nil && note.Frontmatter.Raw != nil {
			encoded, err := json.Marshal(note.Frontmatter.Raw)
			if err != nil {
				return nil, err
			}
			var raw map[string]jsontext.Value
			if err := json.Unmarshal(encoded, &raw); err != nil {
				return nil, err
			}
			frontmatter = raw
		}
		out := noteTextResponse{Path: view.Path, NodeID: view.Node.ID, ContentVersionID: view.Version.ID,
			SHA256: view.Version.BlobHash, Size: view.Version.Size, Title: title, Frontmatter: frontmatter,
			Source: noteReadSource{State: view.SourceState, Watch: view.Watch, InSync: view.InSync},
			Range:  noteReadRange{Start: selected.Start, End: selected.End, LineStart: selected.LineStart, LineEnd: selected.LineEnd},
			Text:   selected.Text, Truncated: selected.Truncated}
		if selected.Truncated {
			out.NextOffset = &selected.End
		}
		if h := selected.Heading; h != nil {
			out.Heading = &noteReadHeading{Text: h.Text, Level: h.Level, Path: h.Path}
		}
		return &output{Body: out}, nil
	})
}
