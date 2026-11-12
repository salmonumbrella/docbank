package daemonconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"

	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
)

type RecordListOptions struct {
	TypeID, Status, Query, AsOf string
	Fields, Before, After       []string
	Limit, Offset               int
}

func (c *Connection) ListRecordTypes(ctx context.Context, includeInactive bool) ([]api.RecordType, error) {
	result, err := c.API().ListRecordTypes(ctx, &apiclient.ListRecordTypesRequestOptions{Query: &apiclient.ListRecordTypesQuery{IncludeInactive: &includeInactive}})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, &responseDecodeError{err: errors.New("record type list response is incomplete")}
	}
	return *result, nil
}
func (c *Connection) GetRecordType(ctx context.Context, id string) (api.RecordType, error) {
	var response *http.Response
	result, err := c.apiWithResponse(&response).GetRecordType(ctx, &apiclient.GetRecordTypeRequestOptions{PathParams: &apiclient.GetRecordTypePath{Type: id}})
	return recordTypeResponse(response, result, err, id)
}
func recordTypeResponse(response *http.Response, result *api.RecordType, err error, id string) (api.RecordType, error) {
	if err != nil {
		return api.RecordType{}, err
	}
	if result == nil || response == nil || result.Definition.ID != id || result.Revision < 0 || response.Header.Get("ETag") != revisionIfMatch(result.Revision) {
		return api.RecordType{}, &responseDecodeError{err: errors.New("invalid record type response")}
	}
	return *result, nil
}
func (c *Connection) SetRecordTypeActive(ctx context.Context, id string, revision int64, active bool) (api.RecordType, error) {
	var response *http.Response
	var result *api.RecordType
	var err error
	if active {
		result, err = c.apiWithResponse(&response).ActivateRecordType(ctx, &apiclient.ActivateRecordTypeRequestOptions{PathParams: &apiclient.ActivateRecordTypePath{Type: id}, Header: &apiclient.ActivateRecordTypeHeaders{IfMatch: revisionIfMatch(revision)}})
	} else {
		result, err = c.apiWithResponse(&response).DeactivateRecordType(ctx, &apiclient.DeactivateRecordTypeRequestOptions{PathParams: &apiclient.DeactivateRecordTypePath{Type: id}, Header: &apiclient.DeactivateRecordTypeHeaders{IfMatch: revisionIfMatch(revision)}})
	}
	return recordTypeResponse(response, result, err, id)
}
func (c *Connection) GetRecord(ctx context.Context, id string) (api.Record, error) {
	var response *http.Response
	result, err := c.apiWithResponse(&response).GetRecord(ctx, &apiclient.GetRecordRequestOptions{PathParams: &apiclient.GetRecordPath{ID: id}})
	return recordResponse(response, result, err, id)
}
func recordResponse(response *http.Response, result *api.Record, err error, id string) (api.Record, error) {
	if err != nil {
		return api.Record{}, err
	}
	if result == nil || response == nil || !validUUIDv4(result.RecordID) || (id != "" && result.RecordID != id) || result.Revision < 1 || response.Header.Get("ETag") != revisionIfMatch(result.Revision) {
		return api.Record{}, &responseDecodeError{err: errors.New("invalid record response")}
	}
	return *result, nil
}
func (c *Connection) ListRecords(ctx context.Context, opts RecordListOptions) (api.RecordPage, error) {
	fields := make([]string, 0, len(opts.Fields))
	for _, v := range opts.Fields {
		k, value, ok := strings.Cut(v, "=")
		if !ok {
			return api.RecordPage{}, fmt.Errorf("expected field=value")
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return api.RecordPage{}, err
		}
		fields = append(fields, k+"="+string(raw))
	}
	limit, offset := int64(opts.Limit), int64(opts.Offset)
	query := &apiclient.ListRecordsQuery{Type: opts.TypeID, Status: &opts.Status, Q: &opts.Query, AsOf: &opts.AsOf, Field: fields, Before: opts.Before, After: opts.After, Limit: &limit, Offset: &offset}
	page, err := c.API().ListRecords(ctx, &apiclient.ListRecordsRequestOptions{Query: query})
	if err != nil {
		return api.RecordPage{}, err
	}
	if page == nil || len(page.Records) > opts.Limit {
		return api.RecordPage{}, &responseDecodeError{err: errors.New("invalid record page")}
	}
	return *page, nil
}

// The generated schema represents arbitrary JSON properties as struct{}.
// Keep the generated operation and route, while encoding the validated field map
// in the request editor until the generator supports arbitrary JSON values.
func recordBodyEditor(body map[string]any) runtime.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding record request: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(encoded))
		req.ContentLength = int64(len(encoded))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
		return nil
	}
}
func (c *Connection) CreateRecord(ctx context.Context, body map[string]any) (api.Record, error) {
	var response *http.Response
	result, err := c.apiWithResponse(&response).CreateRecord(ctx, &apiclient.CreateRecordRequestOptions{}, recordBodyEditor(body))
	return recordResponse(response, result, err, "")
}
func (c *Connection) UpdateRecord(ctx context.Context, id string, revision int64, body map[string]any) (api.Record, error) {
	var response *http.Response
	result, err := c.apiWithResponse(&response).UpdateRecord(ctx, &apiclient.UpdateRecordRequestOptions{PathParams: &apiclient.UpdateRecordPath{ID: id}, Header: &apiclient.UpdateRecordHeaders{IfMatch: revisionIfMatch(revision)}}, recordBodyEditor(body))
	return recordResponse(response, result, err, id)
}
func (c *Connection) RetireRecord(ctx context.Context, id string, revision int64) (api.Record, error) {
	var response *http.Response
	result, err := c.apiWithResponse(&response).RetireRecord(ctx, &apiclient.RetireRecordRequestOptions{PathParams: &apiclient.RetireRecordPath{ID: id}, Header: &apiclient.RetireRecordHeaders{IfMatch: revisionIfMatch(revision)}})
	return recordResponse(response, result, err, id)
}
