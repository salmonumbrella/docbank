package api

import (
	"encoding/json/v2"
	"errors"
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/store"
)

type TaxDocumentLink struct {
	LinkID           string  `json:"link_id"`
	RecordID         string  `json:"record_id"`
	EventID          *string `json:"event_id" nullable:"true"`
	NodeID           *int64  `json:"node_id" nullable:"true"`
	ContentVersionID *string `json:"content_version_id" nullable:"true"`
	ContentSHA256    string  `json:"content_sha256"`
	Role             string  `json:"role"`
	FormCode         string  `json:"form_code"`
	Note             string  `json:"note"`
	RecordedAt       string  `json:"recorded_at"`
	RetiredAt        *string `json:"retired_at" nullable:"true"`
	Live             bool    `json:"live"`
	Pinned           bool    `json:"pinned"`
	Intact           bool    `json:"intact"`
	Path             *string `json:"path" nullable:"true"`
}
type TaxEvidenceRequirement struct {
	Requires string  `json:"requires"`
	EventID  *string `json:"event_id" nullable:"true"`
}
type TaxEvidence struct {
	State string                   `json:"state"`
	Unmet []TaxEvidenceRequirement `json:"unmet"`
}
type LinkTaxDocumentRequest struct {
	NodeID           int64   `json:"node_id"`
	EventID          *string `json:"event_id,omitzero" nullable:"true"`
	ContentVersionID *string `json:"content_version_id,omitzero" nullable:"true"`
	Role             string  `json:"role"`
	FormCode         string  `json:"form_code,omitzero"`
	Note             string  `json:"note,omitzero"`
}
type TaxDocumentMutation struct {
	Record   TaxRecord       `json:"record"`
	Document TaxDocumentLink `json:"document"`
}

func (p *LinkTaxDocumentRequest) UnmarshalJSON(raw []byte) error {
	if _, err := taxMembers(raw, []string{"node_id", "event_id", "content_version_id", "role", "form_code", "note"}, "event_id", "content_version_id"); err != nil {
		return err
	}
	type fields LinkTaxDocumentRequest
	if err := json.Unmarshal(raw, (*fields)(p)); err != nil {
		return err
	}
	if p.EventID != nil && *p.EventID == "" || p.ContentVersionID != nil && *p.ContentVersionID == "" {
		return errors.New("empty document coordinate")
	}
	return nil
}
func (LinkTaxDocumentRequest) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Properties["form_code"].MaxLength = new(256)
	schema.Properties["note"].MaxLength = new(8192)
	return schema
}
func fromStoreTaxDocument(d store.TaxDocumentLinkView) TaxDocumentLink {
	return TaxDocumentLink{LinkID: d.LinkID, RecordID: d.RecordID, EventID: d.EventID, NodeID: d.NodeID, ContentVersionID: d.ContentVersionID, ContentSHA256: d.ContentSHA256, Role: d.Role, FormCode: d.FormCode, Note: d.Note, RecordedAt: d.RecordedAt, RetiredAt: d.RetiredAt, Live: d.Live, Pinned: d.Pinned, Intact: d.Intact, Path: d.Path}
}
