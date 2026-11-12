package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/recordtype"
	"go.kenn.io/docbank/internal/store"
)

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
func parseRecordBody(body map[string]any, create bool) (string, recordtype.Record, store.RecordPatch, error) {
	var typeID string
	r := recordtype.Record{Status: "draft"}
	var patch store.RecordPatch
	fields := map[string]any{}
	business := false
	for k, v := range body {
		switch k {
		case "$schema":
			continue
		case "type_id":
			if !create {
				return "", r, patch, invalidRecordRequest()
			}
			s, ok := v.(string)
			if !ok || s == "" {
				return "", r, patch, invalidRecordRequest()
			}
			typeID = s
			business = true
		case "title", "note", "status":
			s, ok := v.(string)
			if !ok {
				return "", r, patch, invalidRecordRequest()
			}
			business = true
			switch k {
			case "title":
				r.Title = s
				patch.Title = &s
			case "note":
				r.Note = s
				patch.Note = &s
			case "status":
				r.Status = s
				patch.Status = &s
			}
		case "fields":
			m, ok := v.(map[string]any)
			if !ok {
				return "", r, patch, invalidRecordRequest()
			}
			for id, value := range m {
				fields[id] = value
			}
			business = business || len(m) > 0
		default:
			return "", r, patch, invalidRecordRequest()
		}
	}
	if !business || (create && (typeID == "" || patch.Title == nil)) {
		return "", r, patch, invalidRecordRequest()
	}
	r.Fields = fields
	patch.Fields = fields
	return typeID, r, patch, nil
}
func registerRecordRoutes(api huma.API, d Deps, g *gate) {
	huma.Register(api, huma.Operation{OperationID: "listRecordTypes", Method: http.MethodGet, Path: "/api/v1/record-types", Summary: "List record types"}, func(ctx context.Context, in *struct {
		IncludeInactive bool `query:"include_inactive"`
	}) (*struct {
		CacheControl string `header:"Cache-Control"`
		Body         []RecordType
	}, error) {
		states, err := d.Store.ListRecordTypes(ctx, in.IncludeInactive)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := make([]RecordType, 0, len(states))
		for _, s := range states {
			out = append(out, fromRecordType(s))
		}
		return &struct {
			CacheControl string `header:"Cache-Control"`
			Body         []RecordType
		}{"no-store", out}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "getRecordType", Method: http.MethodGet, Path: "/api/v1/record-types/{type}", Summary: "Inspect a record type"}, func(ctx context.Context, in *struct {
		Type string `path:"type"`
	}) (*recordTypeOutput, error) {
		s, err := d.Store.GetRecordType(ctx, in.Type)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &recordTypeOutput{ETag: revisionETag(s.Revision), CacheControl: "no-store", Body: fromRecordType(s)}, nil
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
				out = &recordTypeOutput{ETag: revisionETag(s.Revision), CacheControl: "no-store", Body: fromRecordType(s)}
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
		if _, err := time.Parse("2006-01-02", asOf); err != nil {
			return nil, invalidRecordRequest()
		}
		r, err := d.Store.GetRecord(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &recordOutput{ETag: revisionETag(r.Revision), CacheControl: "no-store", Body: fromRecord(r, asOf)}, nil
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
	}) (*struct {
		CacheControl string `header:"Cache-Control"`
		Body         RecordPage
	}, error) {
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
		return &struct {
			CacheControl string `header:"Cache-Control"`
			Body         RecordPage
		}{"no-store", RecordPage{Records: out, Total: total, Limit: opts.Limit, Offset: opts.Offset, AsOf: opts.AsOf}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "createRecord", Method: http.MethodPost, Path: "/api/v1/records", MaxBodyBytes: 512 << 10, Summary: "Create a record", DefaultStatus: http.StatusCreated}, func(ctx context.Context, in *struct{ Body RecordCreateRequest }) (*recordOutput, error) {
		body := map[string]any{"type_id": in.Body.TypeID, "title": in.Body.Title}
		if in.Body.Fields != nil {
			body["fields"] = in.Body.Fields
		}
		if in.Body.Status != nil {
			body["status"] = *in.Body.Status
		}
		if in.Body.Note != nil {
			body["note"] = *in.Body.Note
		}
		id, input, _, err := parseRecordBody(body, true)
		if err != nil {
			return nil, err
		}
		var out *recordOutput
		err = g.mutate(ctx, func() error {
			r, e := d.Store.CreateRecord(ctx, id, input)
			if e != nil {
				return FromStoreError(e)
			}
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: "no-store", Body: fromRecord(r, recordToday(""))}
			return nil
		})
		return out, err
	})
	huma.Register(api, huma.Operation{OperationID: "updateRecord", Method: http.MethodPatch, Path: "/api/v1/records/{id}", MaxBodyBytes: 512 << 10, Summary: "Edit a record"}, func(ctx context.Context, in *struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    RecordUpdateRequest
	}) (*recordOutput, error) {
		revision, err := recordRevision(in.IfMatch, false)
		if err != nil {
			return nil, err
		}
		body := map[string]any{}
		if in.Body.Title != nil {
			body["title"] = *in.Body.Title
		}
		if in.Body.Note != nil {
			body["note"] = *in.Body.Note
		}
		if in.Body.Status != nil {
			body["status"] = *in.Body.Status
		}
		if in.Body.Fields != nil {
			body["fields"] = in.Body.Fields
		}
		_, _, patch, err := parseRecordBody(body, false)
		if err != nil {
			return nil, err
		}
		var out *recordOutput
		err = g.mutate(ctx, func() error {
			r, e := d.Store.UpdateRecord(ctx, in.ID, revision, patch)
			if e != nil {
				return FromStoreError(e)
			}
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: "no-store", Body: fromRecord(r, recordToday(""))}
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
			out = &recordOutput{ETag: revisionETag(r.Revision), CacheControl: "no-store", Body: fromRecord(r, recordToday(""))}
			return nil
		})
		return out, err
	})
}
