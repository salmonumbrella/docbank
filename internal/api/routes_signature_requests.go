package api

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/contractsign"
	"go.kenn.io/docbank/internal/docusealclient"
	"go.kenn.io/docbank/internal/store"
)

type SignatureRequestBody struct {
	TemplateID int64                        `json:"template_id"`
	Signers    []store.SignatureSignerInput `json:"signers"`
}

func (b *SignatureRequestBody) UnmarshalJSON(raw []byte) error {
	invalid := errors.New("invalid signature request body")
	var m map[string]jsontext.Value
	if json.Unmarshal(raw, &m) != nil {
		return invalid
	}
	for k, v := range m {
		if k != "template_id" && k != "signers" && k != "$schema" {
			return invalid
		}
		if string(v) == "null" {
			return invalid
		}
	}
	var signerRaw []map[string]jsontext.Value
	if json.Unmarshal(m["signers"], &signerRaw) != nil {
		return invalid
	}
	for _, signer := range signerRaw {
		if signer == nil {
			return invalid
		}
		for k, v := range signer {
			if (k != "role" && k != "name" && k != "email") || string(v) == "null" {
				return invalid
			}
		}
	}
	type fields SignatureRequestBody
	var v fields
	if json.Unmarshal(raw, &v) != nil {
		return invalid
	}
	*b = SignatureRequestBody(v)
	return nil
}

type signatureOutput struct {
	CacheControl string `header:"Cache-Control"`
	ETag         string `header:"ETag"`
	Body         store.SignatureRequest
}
type SignatureRequestPage struct {
	Items  []store.SignatureRequestSummary `json:"items"`
	Limit  int                             `json:"limit"`
	Offset int                             `json:"offset"`
}
type signaturePageOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         SignatureRequestPage
}

func signatureResult(v store.SignatureRequest) *signatureOutput {
	return &signatureOutput{CacheControl: cacheControlNoStore, ETag: revisionETag(v.Revision), Body: v}
}
func signatureError(err error) error {
	var created *contractsign.CreationError
	if errors.As(err, &created) {
		code := "docuseal_creation_unknown"
		if errors.Is(err, docusealclient.ErrCreationFailed) {
			code = "docuseal_creation_failed"
		}
		e := NewError(502, code, "Read the request before retrying")
		e.RequestID = created.RequestID
		return e
	}
	var problem *Error
	if errors.As(err, &problem) {
		return problem
	}
	if errors.Is(err, store.ErrInvalidSignatureRequest) || errors.Is(err, store.ErrSignatureRequestOpen) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrStaleRevision) || errors.Is(err, store.ErrAuditMutationUnsupported) {
		return FromStoreError(err)
	}
	return docuSealProblem(err)
}

// signatureBodyContext replays the bounded, validated bytes to Huma.
type signatureBodyContext struct {
	huma.Context
	body *bytes.Reader
}

func (c signatureBodyContext) BodyReader() io.Reader { return c.body }

// Huma includes the original body in parse errors; validate this private body
// first and return a fixed problem without submitting it to the formatter.
func privateSignatureBody(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		_ = ctx.SetReadDeadline(time.Now().Add(5 * time.Second))
		reader := ctx.BodyReader()
		if reader == nil {
			reader = bytes.NewReader(nil)
		}
		if closer, ok := reader.(io.Closer); ok {
			defer func() { _ = closer.Close() }()
		}
		body, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		_ = ctx.SetReadDeadline(time.Time{})
		status := http.StatusUnprocessableEntity
		detail := "invalid signature request body"
		if err != nil {
			status = http.StatusBadRequest
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Timeout() {
				status = http.StatusRequestTimeout
			}
		} else if len(body) > 1<<20 {
			status = http.StatusRequestEntityTooLarge
		} else if len(body) == 0 {
			status = http.StatusBadRequest
		} else {
			contentType := ctx.Header("Content-Type")
			if contentType == "" {
				contentType = "application/json"
			}
			var request SignatureRequestBody
			if err = api.Unmarshal(contentType, body, &request); err == nil {
				next(signatureBodyContext{Context: ctx, body: bytes.NewReader(body)})
				return
			} else if errors.Is(err, huma.ErrUnknownContentType) {
				status = http.StatusUnsupportedMediaType
			}
		}
		ctx.SetHeader("Cache-Control", cacheControlNoStore)
		_ = huma.WriteErr(api, ctx, status, detail)
	}
}

func registerSignatureRequestRoutes(api huma.API, d Deps, g *gate) {
	service := contractsign.Service{Store: d.Store, Connect: func(ctx context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		return resolveDocuSeal(ctx, d)
	}, Admit: g.mutate, Settle: g.MutateContext}
	huma.Register(api, huma.Operation{OperationID: "createContractSignatureRequest", Method: http.MethodPost, Path: "/api/v1/contracts/{contract_id}/signature-requests", Summary: "Send a contract for signature", DefaultStatus: http.StatusCreated, MaxBodyBytes: 1 << 20, SkipValidateBody: true, Middlewares: huma.Middlewares{privateSignatureBody(api)}}, func(ctx context.Context, in *struct {
		ContractID string `path:"contract_id"`
		IfMatch    string `header:"If-Match"`
		Body       SignatureRequestBody
	}) (*signatureOutput, error) {
		rev, err := parseIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		v, err := service.Request(ctx, in.ContractID, rev, in.Body.TemplateID, in.Body.Signers)
		if err != nil {
			return nil, signatureError(err)
		}
		return signatureResult(v), nil
	})
	huma.Register(api, huma.Operation{OperationID: "listContractSignatureRequests", Method: http.MethodGet, Path: "/api/v1/contracts/{contract_id}/signature-requests", Summary: "List local signature requests", RejectUnknownQueryParameters: true}, func(ctx context.Context, in *struct {
		ContractID string `path:"contract_id"`
		Limit      int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
		Offset     int    `query:"offset" minimum:"0"`
	}) (*signaturePageOutput, error) {
		items, err := d.Store.ListSignatureRequests(ctx, in.ContractID, in.Limit, in.Offset)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &signaturePageOutput{CacheControl: cacheControlNoStore}
		out.Body.Items = items
		out.Body.Limit = in.Limit
		out.Body.Offset = in.Offset
		return out, nil
	})
	huma.Register(api, huma.Operation{OperationID: "getSignatureRequest", Method: http.MethodGet, Path: "/api/v1/signature-requests/{request_id}", Summary: "Inspect a local signature request"}, func(ctx context.Context, in *struct {
		RequestID string `path:"request_id"`
	}) (*signatureOutput, error) {
		v, err := d.Store.GetSignatureRequest(ctx, in.RequestID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return signatureResult(v), nil
	})
}
