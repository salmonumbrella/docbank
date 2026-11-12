package api

import (
	"go.kenn.io/docbank/internal/recordtype"
	"go.kenn.io/docbank/internal/store"
)

type RecordType struct {
	Definition recordtype.Definition `json:"definition"`
	Active     bool                  `json:"active"`
	Revision   int64                 `json:"revision"`
	CreatedAt  string                `json:"created_at"`
	UpdatedAt  string                `json:"updated_at"`
}
type Record struct {
	RecordID        string         `json:"record_id"`
	TypeID          string         `json:"type_id"`
	Title           string         `json:"title"`
	Note            string         `json:"note"`
	Status          string         `json:"status"`
	EffectiveStatus string         `json:"effective_status"`
	Fields          map[string]any `json:"fields"`
	Revision        int64          `json:"revision"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	RetiredAt       *string        `json:"retired_at"`
}
type RecordPage struct {
	Records []Record `json:"records"`
	Total   int      `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
	AsOf    string   `json:"as_of"`
}
type recordOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         Record
}
type recordTypeOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         RecordType
}

func fromRecordType(s store.RecordTypeState) RecordType {
	return RecordType{Definition: s.Definition, Active: s.Active, Revision: s.Revision, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}
func fromRecord(r store.Record, asOf string) Record {
	def, _ := recordtype.Builtin(r.TypeID)
	return Record{RecordID: r.ID, TypeID: r.TypeID, Title: r.Title, Note: r.Note, Status: r.Status, EffectiveStatus: recordtype.EffectiveStatus(def, r.Status, r.Fields, asOf), Fields: r.Fields, Revision: r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RetiredAt: r.RetiredAt}
}
