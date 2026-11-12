package api

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/recordtype"
	"go.kenn.io/docbank/internal/store"
)

// The server tracks presence separately so null cannot collapse into omission.
type optionalRecordString struct {
	Set   bool
	Value string
}

const cacheControlNoStore = "no-store"

func (v *optionalRecordString) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return errors.New("record field cannot be null")
	}
	v.Set = true
	return jsonv2.Unmarshal(raw, &v.Value)
}
func (optionalRecordString) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString}
}

type optionalRecordFields struct {
	Set   bool
	Value map[string]any
}

func (v *optionalRecordFields) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return errors.New("record fields cannot be null")
	}
	v.Set = true
	return jsonv2.Unmarshal(raw, &v.Value)
}
func (optionalRecordFields) Schema(r huma.Registry) *huma.Schema {
	return r.Schema(reflect.TypeFor[map[string]any](), true, "")
}

// RecordCreateInput is server-only; generated clients use RecordCreateRequest.
type RecordCreateInput struct {
	TypeID string               `json:"type_id" minLength:"1"`
	Title  string               `json:"title" minLength:"1" doc:"After trimming and NFC normalization, 1 to 300 characters and at most 1200 UTF-8 bytes."`
	Note   optionalRecordString `json:"note,omitzero" doc:"At most 8192 UTF-8 bytes after normalization."`
	Status optionalRecordString `json:"status,omitzero"`
	Fields optionalRecordFields `json:"fields,omitzero"`
}

// RecordUpdateInput distinguishes omitted properties from forbidden nulls.
type RecordUpdateInput struct {
	Title  optionalRecordString `json:"title,omitzero" doc:"After trimming and NFC normalization, 1 to 300 characters and at most 1200 UTF-8 bytes."`
	Note   optionalRecordString `json:"note,omitzero" doc:"At most 8192 UTF-8 bytes after normalization."`
	Status optionalRecordString `json:"status,omitzero"`
	Fields optionalRecordFields `json:"fields,omitzero"`
}

func recordToday(asOf string) string {
	if asOf != "" {
		return asOf
	}
	return time.Now().Format("2006-01-02")
}
func recordRevision(v string, allowZero bool) (int64, error) {
	if allowZero && (v == "0" || v == `"0"`) {
		return 0, nil
	}
	return parseIfMatch(v)
}
func invalidRecordRequest() error {
	return NewError(http.StatusUnprocessableEntity, "invalid_record", "invalid record request")
}
func registerRecordRoutes(api huma.API, d Deps, g *gate) {
	huma.Register(api, huma.Operation{OperationID: "listRecordTypes", Method: http.MethodGet, Path: "/api/v1/record-types", Summary: "List record types"}, func(ctx context.Context, in *struct {
		IncludeInactive bool `query:"include_inactive"`
	}) (*recordTypeListOutput, error) {
		states, err := d.Store.ListRecordTypes(ctx, in.IncludeInactive)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := make([]RecordType, 0, len(states))
		for _, s := range states {
			out = append(out, fromRecordType(s))
		}
		return &recordTypeListOutput{cacheControlNoStore, out}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "getRecordType", Method: http.MethodGet, Path: "/api/v1/record-types/{type}", Summary: "Inspect a record type"}, func(ctx context.Context, in *struct {
		Type string `path:"type"`
	}) (*recordTypeOutput, error) {
		s, err := d.Store.GetRecordType(ctx, in.Type)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &recordTypeOutput{ETag: revisionETag(s.Revision), CacheControl: cacheControlNoStore, Body: fromRecordType(s)}, nil
	})
	for _, active := range []bool{true, false} {
		path, op := "deactivate", "deactivateRecordType"
		if active {
			path, op = "activate", "activateRecordType"
		}
		huma.Register(api, huma.Operation{OperationID: op, Method: http.MethodPost, Path: "/api/v1/record-types/{type}/" + path, Summary: "Change record type activation"}, func(ctx context.Context, in *struct {
			Type    string `path:"type"`
			IfMatch string `header:"If-Match"`
		}) (*recordTypeOutput, error) {
			revision, err := recordRevision(in.IfMatch, true)
			if err != nil {
				return nil, err
			}
			var out *recordTypeOutput
			err = g.mutate(ctx, func() error {
				var s store.RecordTypeState
				var e error
				if active {
					s, e = d.Store.ActivateRecordType(ctx, in.Type, revision)
				} else {
					s, e = d.Store.DeactivateRecordType(ctx, in.Type, revision)
				}
				if e != nil {
					return FromStoreError(e)
				}
				out = &recordTypeOutput{ETag: revisionETag(s.Revision), CacheControl: cacheControlNoStore, Body: fromRecordType(s)}
				return nil
			})
			return out, err
		})
	}
	huma.Register(api, huma.Operation{OperationID: "getRecord", Method: http.MethodGet, Path: "/api/v1/records/{id}", Summary: "Inspect a record"}, func(ctx context.Context, in *struct {
		ID   string `path:"id"`
		AsOf string `query:"as_of"`
	}) (*recordOutput, error) {
		asOf := recordToday(in.AsOf)
		if !recordtype.ValidDate(asOf) {
			return nil, invalidRecordRequest()
		}
		r, err := d.Store.GetRecord(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &recordOutput{ETag: revisionETag(r.Revision), CacheControl: cacheControlNoStore, Body: fromRecord(r, asOf)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "listRecords", Method: http.MethodGet, Path: "/api/v1/records", Summary: "List records"}, func(ctx context.Context, in *struct {
		Type            string   `query:"type" required:"true"`
		Status          string   `query:"status"`
		IncludeInactive bool     `query:"include_inactive"`
		Query           string   `query:"q"`
		AsOf            string   `query:"as_of"`
		Field           []string `query:"field"`
		Before          []string `query:"before"`
		After           []string `query:"after"`
		Limit           int      `query:"limit" default:"100" minimum:"1" maximum:"1000"`
		Offset          int      `query:"offset" minimum:"0"`
	}) (*recordPageOutput, error) {
		opts := store.RecordListOptions{TypeID: in.Type, Status: in.Status, IncludeInactive: in.IncludeInactive, Query: in.Query, AsOf: recordToday(in.AsOf), Fields: map[string]any{}, Before: map[string]string{}, After: map[string]string{}, Limit: in.Limit, Offset: in.Offset}
		for _, v := range in.Field {
			id, raw, ok := strings.Cut(v, "=")
			if !ok {
				return nil, invalidRecordRequest()
			}
			if _, exists := opts.Fields[id]; exists {
				return nil, invalidRecordRequest()
			}
			var value any
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				return nil, invalidRecordRequest()
			}
			opts.Fields[id] = value
		}
		for _, f := range []struct {
			items []string
			dst   map[string]string
		}{{in.Before, opts.Before}, {in.After, opts.After}} {
			for _, v := range f.items {
				id, date, ok := strings.Cut(v, "=")
				if !ok {
					return nil, invalidRecordRequest()
				}
				if _, exists := f.dst[id]; exists {
					return nil, invalidRecordRequest()
				}
				f.dst[id] = date
			}
		}
		records, total, err := d.Store.ListRecords(ctx, opts)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := make([]Record, 0, len(records))
		for _, r := range records {
			out = append(out, fromRecord(r, opts.AsOf))
		}
		return &recordPageOutput{cacheControlNoStore, RecordPage{Records: out, Total: total, Limit: opts.Limit, Offset: opts.Offset, AsOf: opts.AsOf}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "createRecord", Method: http.MethodPost, Path: "/api/v1/records", MaxBodyBytes: 512 << 10, Summary: "Create a record", DefaultStatus: http.StatusCreated}, func(ctx context.Context, in *struct{ Body RecordCreateInput }) (*recordOutput, error) {
		input := recordtype.Record{Title: in.Body.Title, Status: "draft", Fields: in.Body.Fields.Value}
		if in.Body.Note.Set {
			input.Note = in.Body.Note.Value
		}
		if in.Body.Status.Set {
			input.Status = in.Body.Status.Value
		}
		var out *recordOutput
		err = g.mutate(ctx, func() error {
			r, e := d.Store.CreateRecord(ctx, in.Body.TypeID, input)
			if e != nil {
				return FromStoreError(e)
			}
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: cacheControlNoStore, Body: fromRecord(r, recordToday(""))}
			return nil
		})
		return out, err
	})
	huma.Register(api, huma.Operation{OperationID: "updateRecord", Method: http.MethodPatch, Path: "/api/v1/records/{id}", MaxBodyBytes: 512 << 10, Summary: "Edit a record"}, func(ctx context.Context, in *struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    RecordUpdateInput
	}) (*recordOutput, error) {
		revision, err := recordRevision(in.IfMatch, false)
		if err != nil {
			return nil, err
		}
		patch := store.RecordPatch{Fields: in.Body.Fields.Value}
		if in.Body.Title.Set {
			patch.Title = &in.Body.Title.Value
		}
		if in.Body.Note.Set {
			patch.Note = &in.Body.Note.Value
		}
		if in.Body.Status.Set {
			patch.Status = &in.Body.Status.Value
		}
		if patch.Title == nil && patch.Note == nil && patch.Status == nil && len(patch.Fields) == 0 {
			return nil, invalidRecordRequest()
		}
		var out *recordOutput
		err = g.mutate(ctx, func() error {
			r, e := d.Store.UpdateRecord(ctx, in.ID, revision, patch)
			if e != nil {
				return FromStoreError(e)
			}
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: cacheControlNoStore, Body: fromRecord(r, recordToday(""))}
			return nil
		})
		return out, err
	})
	huma.Register(api, huma.Operation{OperationID: "retireRecord", Method: http.MethodPost, Path: "/api/v1/records/{id}/retire", Summary: "Retire a record"}, func(ctx context.Context, in *struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
	}) (*recordOutput, error) {
		revision, err := recordRevision(in.IfMatch, false)
		if err != nil {
			return nil, err
		}
		var out *recordOutput
		err = g.mutate(ctx, func() error {
			r, e := d.Store.RetireRecord(ctx, in.ID, revision)
			if e != nil {
				return FromStoreError(e)
			}
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: cacheControlNoStore, Body: fromRecord(r, recordToday(""))}
			return nil
		})
		return out, err
	})
}
