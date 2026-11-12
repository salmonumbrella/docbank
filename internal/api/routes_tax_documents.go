package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/store"
)

type taxDocumentOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         TaxDocumentMutation
}

func taxDocumentResult(m store.TaxDocumentMutation) *taxDocumentOutput {
	return &taxDocumentOutput{ETag: revisionETag(m.Record.Revision), CacheControl: cacheControlNoStore, Body: TaxDocumentMutation{Record: fromStoreTaxRecord(m.Record), Document: fromStoreTaxDocument(m.Document)}}
}
func registerTaxDocumentRoutes(api huma.API, d Deps, g *gate) {
	huma.Register(api, huma.Operation{OperationID: "linkTaxDocument", Method: http.MethodPost, Path: "/api/v1/tax/records/{record_id}/documents", Summary: "Link a tax document", DefaultStatus: http.StatusCreated}, func(ctx context.Context, in *struct {
		RecordID string `path:"record_id"`
		IfMatch  string `header:"If-Match"`
		Body     LinkTaxDocumentRequest
	}) (*taxDocumentOutput, error) {
		revision, err := parseIfMatch(in.IfMatch)
		if err != nil {
			return nil, taxRouteError(d.Logger, err)
		}
		var out *taxDocumentOutput
		err = g.mutate(ctx, func() error {
			m, err := d.Store.LinkTaxDocument(ctx, in.RecordID, revision, store.TaxDocumentInput{NodeID: in.Body.NodeID, EventID: in.Body.EventID, ContentVersionID: in.Body.ContentVersionID, Role: in.Body.Role, FormCode: in.Body.FormCode, Note: in.Body.Note})
			if err != nil {
				return err
			}
			out = taxDocumentResult(m)
			return nil
		})
		return out, taxRouteError(d.Logger, err)
	})
	huma.Register(api, huma.Operation{OperationID: "retireTaxDocument", Method: http.MethodPost, Path: "/api/v1/tax/records/{record_id}/documents/{link_id}/retire", Summary: "Retire a tax document link"}, func(ctx context.Context, in *struct {
		RecordID string `path:"record_id"`
		LinkID   string `path:"link_id"`
		IfMatch  string `header:"If-Match"`
	}) (*taxDocumentOutput, error) {
		revision, err := parseIfMatch(in.IfMatch)
		if err != nil {
			return nil, taxRouteError(d.Logger, err)
		}
		var out *taxDocumentOutput
		err = g.mutate(ctx, func() error {
			m, err := d.Store.RetireTaxDocument(ctx, in.RecordID, in.LinkID, revision)
			if err != nil {
				return err
			}
			out = taxDocumentResult(m)
			return nil
		})
		return out, taxRouteError(d.Logger, err)
	})
}
