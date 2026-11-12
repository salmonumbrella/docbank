package daemonconn

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/taxrecord"
)

var documentFields = []string{"link_id", "record_id", "event_id", "node_id", "content_version_id", "content_sha256", "role", "form_code", "note", "recorded_at", "retired_at", "live", "pinned", "intact", "path"}
var documentNullable = map[string]bool{"event_id": true, "node_id": true, "content_version_id": true, "retired_at": true, "path": true}

func validTaxDocument(d api.TaxDocumentLink, recordID string) bool {
	if !validUUIDv4(d.LinkID) || d.RecordID != recordID || d.NodeID != nil && *d.NodeID < 1 || d.Pinned != (d.ContentVersionID != nil) || d.Intact && (!d.Pinned || d.NodeID == nil) || d.Live && (d.NodeID == nil || d.RetiredAt != nil || d.Path == nil) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, d.RecordedAt); err != nil {
		return false
	}
	if d.RetiredAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *d.RetiredAt); err != nil || *d.RetiredAt < d.RecordedAt {
			return false
		}
	}
	return taxrecord.ValidateDocument(taxrecord.DocumentValues{EventID: d.EventID, ContentVersionID: d.ContentVersionID, ContentSHA256: d.ContentSHA256, Role: d.Role, FormCode: d.FormCode, Note: d.Note}) == nil
}
func invalidDocumentReceipt() error {
	return &responseDecodeError{err: errors.New("invalid tax document response")}
}
func (c *Connection) taxDocumentReceipt(ctx context.Context, response *http.Response, id, linkID string, revision int64, retired bool) (api.TaxDocumentMutation, error) {
	var out api.TaxDocumentMutation
	data, err := readTaxReceipt(ctx, response, false, []string{"record", "document"}, nil)
	if err != nil {
		return out, taxMutationFailure(err, "record")
	}
	var raw map[string]jsontext.Value
	if json.Unmarshal(data, &raw) != nil || !taxMembers(raw["record"], recordFields, recordNullable) || !taxMembers(raw["document"], documentFields, documentNullable) || json.Unmarshal(data, &out) != nil {
		return api.TaxDocumentMutation{}, taxMutationFailure(invalidDocumentReceipt(), "record")
	}
	if _, err = validateTaxRecordResponse(&out.Record, response, id); err != nil || out.Record.Revision-1 != revision || out.Record.RetiredAt != nil || !validTaxDocument(out.Document, id) || (linkID != "" && out.Document.LinkID != linkID) || (out.Document.RetiredAt != nil) != retired {
		return api.TaxDocumentMutation{}, taxMutationFailure(invalidDocumentReceipt(), "record")
	}
	return out, nil
}
func (c *Connection) LinkTaxDocument(ctx context.Context, id string, revision int64, in api.LinkTaxDocumentRequest) (api.TaxDocumentMutation, error) {
	if !validUUIDv4(id) || revision < 1 || in.NodeID < 1 || in.EventID != nil && !validUUIDv4(*in.EventID) || in.ContentVersionID != nil && !validUUIDv4(*in.ContentVersionID) {
		return api.TaxDocumentMutation{}, errors.New("invalid tax document input")
	}
	var response *http.Response
	_, err := c.apiWithResponse(&response).LinkTaxDocument(runtime.WithStreamingResponse(ctx), &apiclient.LinkTaxDocumentRequestOptions{PathParams: &apiclient.LinkTaxDocumentPath{RecordID: id}, Header: &apiclient.LinkTaxDocumentHeaders{IfMatch: new(revisionIfMatch(revision))}, Body: &in})
	if err != nil {
		return api.TaxDocumentMutation{}, taxMutationFailure(mutationRequestError(response, err), "record")
	}
	out, err := c.taxDocumentReceipt(ctx, response, id, "", revision, false)
	if err == nil && (out.Document.NodeID == nil || *out.Document.NodeID != in.NodeID || out.Document.Role != in.Role || !reflect.DeepEqual(out.Document.EventID, in.EventID) || !reflect.DeepEqual(out.Document.ContentVersionID, in.ContentVersionID)) {
		return api.TaxDocumentMutation{}, taxMutationFailure(invalidDocumentReceipt(), "record")
	}
	return out, err
}
func (c *Connection) RetireTaxDocument(ctx context.Context, recordID, linkID string, revision int64) (api.TaxDocumentMutation, error) {
	if !validUUIDv4(recordID) || !validUUIDv4(linkID) || revision < 1 {
		return api.TaxDocumentMutation{}, errors.New("invalid tax document ID or revision")
	}
	var response *http.Response
	_, err := c.apiWithResponse(&response).RetireTaxDocument(runtime.WithStreamingResponse(ctx), &apiclient.RetireTaxDocumentRequestOptions{PathParams: &apiclient.RetireTaxDocumentPath{RecordID: recordID, LinkID: linkID}, Header: &apiclient.RetireTaxDocumentHeaders{IfMatch: new(revisionIfMatch(revision))}})
	if err != nil {
		return api.TaxDocumentMutation{}, taxMutationFailure(mutationRequestError(response, err), "record")
	}
	return c.taxDocumentReceipt(ctx, response, recordID, linkID, revision, true)
}
