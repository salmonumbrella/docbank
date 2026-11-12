package daemonconn

import (
	"context"
	"errors"
	"net/http"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/store"
)

func validateSignatureResponse(v store.SignatureRequest, r *http.Response, id string) error {
	if r == nil || !validUUIDv4(v.RequestID) || !validUUIDv4(v.ContractID) || (id != "" && v.RequestID != id) || v.Revision < 1 || r.Header.Get("ETag") != revisionIfMatch(v.Revision) || v.Signers == nil || len(v.Signers) < 1 || len(v.Signers) > 20 {
		return errors.New("invalid signature request response")
	}
	for i, signer := range v.Signers {
		if signer.Position != i || !store.ValidSignatureSigner(store.SignatureSignerInput{Role: signer.Role, Name: signer.Name, Email: signer.Email}) {
			return errors.New("invalid signature signer")
		}
	}
	return nil
}
func (c *Connection) CreateSignatureRequest(ctx context.Context, id string, rev int64, body api.SignatureRequestBody) (store.SignatureRequest, error) {
	if !validUUIDv4(id) || rev < 1 {
		return store.SignatureRequest{}, errors.New("invalid contract ID or revision")
	}
	var r *http.Response
	_, err := c.apiWithResponseLimit(&r, 3<<20).CreateContractSignatureRequest(runtime.WithStreamingResponse(ctx), &apiclient.CreateContractSignatureRequestRequestOptions{PathParams: &apiclient.CreateContractSignatureRequestPath{ContractID: id}, Header: &apiclient.CreateContractSignatureRequestHeaders{IfMatch: revisionIfMatch(rev)}, Body: &body})
	if err != nil {
		return store.SignatureRequest{}, mutationRequestError(r, err)
	}
	defer func() { _ = r.Body.Close() }()
	var out struct {
		store.SignatureRequest

		Schema string `json:"$schema,omitzero"`
	}
	err = decodeBoundedResponse(r, &out, 3<<20)
	if err == nil {
		err = validateSignatureResponse(out.SignatureRequest, r, "")
		if err == nil && out.ContractID != id {
			err = errors.New("invalid contract identity")
		}
		if err == nil && r.StatusCode != http.StatusCreated {
			err = errors.New("unexpected signature status")
		}
		if err == nil && out.State != "sent" {
			err = errors.New("unconfirmed signature request")
		}
	}
	if err != nil {
		return store.SignatureRequest{}, &responseDecodeError{err: err}
	}
	return out.SignatureRequest, nil
}
func (c *Connection) SignatureRequest(ctx context.Context, id string) (store.SignatureRequest, error) {
	if !validUUIDv4(id) {
		return store.SignatureRequest{}, errors.New("invalid signature request ID")
	}
	var r *http.Response
	_, err := c.apiWithResponseLimit(&r, 3<<20).GetSignatureRequest(runtime.WithStreamingResponse(ctx), &apiclient.GetSignatureRequestRequestOptions{PathParams: &apiclient.GetSignatureRequestPath{RequestID: id}})
	if err != nil {
		return store.SignatureRequest{}, err
	}
	defer func() { _ = r.Body.Close() }()
	var out struct {
		store.SignatureRequest

		Schema string `json:"$schema,omitzero"`
	}
	if err = decodeBoundedResponse(r, &out, 3<<20); err == nil {
		err = validateSignatureResponse(out.SignatureRequest, r, id)
	}
	if err != nil {
		return store.SignatureRequest{}, &responseDecodeError{err: err}
	}
	return out.SignatureRequest, nil
}
func (c *Connection) SignatureRequests(ctx context.Context, id string, limit, offset int) (api.SignatureRequestPage, error) {
	if !validUUIDv4(id) || limit < 1 || limit > 100 || offset < 0 {
		return api.SignatureRequestPage{}, errors.New("invalid signature request page")
	}
	lim, off := int64(limit), int64(offset)
	var r *http.Response
	_, err := c.apiWithResponseLimit(&r, 3<<20).ListContractSignatureRequests(runtime.WithStreamingResponse(ctx), &apiclient.ListContractSignatureRequestsRequestOptions{PathParams: &apiclient.ListContractSignatureRequestsPath{ContractID: id}, Query: &apiclient.ListContractSignatureRequestsQuery{Limit: &lim, Offset: &off}})
	if err != nil {
		return api.SignatureRequestPage{}, err
	}
	defer func() { _ = r.Body.Close() }()
	var out struct {
		api.SignatureRequestPage

		Schema string `json:"$schema,omitzero"`
	}
	err = decodeBoundedResponse(r, &out, 3<<20)
	if err == nil && (out.Items == nil || len(out.Items) > limit || out.Limit != limit || out.Offset != offset) {
		err = errors.New("invalid signature page")
	}
	if err == nil {
		for _, v := range out.Items {
			if !validUUIDv4(v.RequestID) || v.ContractID != id || v.Revision < 1 {
				err = errors.New("invalid signature page item")
				break
			}
		}
	}
	if err != nil {
		return api.SignatureRequestPage{}, &responseDecodeError{err: err}
	}
	return out.SignatureRequestPage, nil
}
