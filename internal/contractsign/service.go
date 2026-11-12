// Package contractsign coordinates a durable signing intent with one provider call.
package contractsign

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/docusealclient"
	"go.kenn.io/docbank/internal/store"
)

type Connect func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error)
type Gate func(context.Context, func() error) error
type Service struct {
	Store         *store.Store
	Connect       Connect
	Admit, Settle Gate
}
type CreationError struct {
	RequestID string
	Cause     error
}

func (e *CreationError) Error() string { return e.Cause.Error() }
func (e *CreationError) Unwrap() error { return e.Cause }
func (s Service) Request(ctx context.Context, contractID string, revision, templateID int64, signers []store.SignatureSignerInput) (store.SignatureRequest, error) {
	if templateID <= 0 || len(signers) < 1 || len(signers) > 20 {
		return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
	}
	supplied := map[string]store.SignatureSignerInput{}
	for _, v := range signers {
		if !store.ValidSignatureSigner(v) {
			return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
		}
		if _, exists := supplied[v.Role]; exists {
			return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
		}
		supplied[v.Role] = v
	}
	connection, client, err := s.Connect(ctx)
	if err != nil {
		return store.SignatureRequest{}, err
	}
	defer client.CloseIdleConnections()
	if strings.Contains(connection.Endpoint, connection.APIKey) || strings.Contains(connection.APIPath, connection.APIKey) {
		return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
	}
	template, err := client.Template(ctx, templateID)
	if err != nil {
		return store.SignatureRequest{}, err
	}
	if len(template.Roles) < 1 || len(template.Roles) > 20 || len(template.Roles) != len(signers) {
		return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
	}
	ordered := make([]store.SignatureSignerInput, 0, len(signers))
	roles := make(map[string]bool, len(template.Roles))
	for _, role := range template.Roles {
		folded := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}, role)
		if roles[folded] {
			return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
		}
		roles[folded] = true
		v, ok := supplied[role]
		if !ok {
			return store.SignatureRequest{}, store.ErrInvalidSignatureRequest
		}
		ordered = append(ordered, v)
		delete(supplied, role)
	}
	input := store.SignatureRequestInput{EndpointOrigin: connection.Endpoint, APIPath: connection.APIPath, TemplateID: strconv.FormatInt(templateID, 10), TemplateName: template.Name, Signers: ordered}
	var request store.SignatureRequest
	err = s.Admit(ctx, func() error {
		var e error
		request, e = s.Store.BeginSignatureRequest(ctx, contractID, revision, input)
		return e
	})
	if err != nil {
		return store.SignatureRequest{}, err
	}
	outgoing := make([]docusealclient.SubmitterInput, len(ordered))
	for i, v := range ordered {
		outgoing[i] = docusealclient.SubmitterInput{Role: v.Role, Name: v.Name, Email: v.Email, ExternalID: fmt.Sprintf("docbank:%s:%d", request.RequestID, i)}
	}
	receipt, providerErr := client.CreateSubmission(ctx, templateID, outgoing)
	if providerErr == nil {
		if strings.Contains(receipt.SubmissionID, connection.APIKey) {
			providerErr = docusealclient.ErrCreationUnknown
		}
		for _, id := range receipt.SubmitterIDs {
			if strings.Contains(id, connection.APIKey) {
				providerErr = docusealclient.ErrCreationUnknown
			}
		}
		if providerErr != nil {
			receipt = docusealclient.SubmissionReceipt{}
		}
	}
	result := store.SignatureCreationResult{SubmissionID: receipt.SubmissionID, SubmitterIDs: receipt.SubmitterIDs}
	if providerErr != nil {
		result.Uncertain = !errors.Is(providerErr, docusealclient.ErrCreationFailed)
		result.ErrorCode = "docuseal_creation_failed"
		if result.Uncertain {
			result.ErrorCode = "docuseal_creation_unknown"
		}
	}
	// Settling must finish after caller cancellation; no network work is detached.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var settled store.SignatureRequest
	err = s.Settle(settleCtx, func() error {
		var e error
		settled, e = s.Store.FinishSignatureCreation(settleCtx, request.RequestID, request.Revision, result)
		return e
	})
	if err != nil {
		return store.SignatureRequest{}, &CreationError{RequestID: request.RequestID, Cause: docusealclient.ErrCreationUnknown}
	}
	if providerErr != nil {
		return store.SignatureRequest{}, &CreationError{RequestID: request.RequestID, Cause: providerErr}
	}
	return settled, nil
}
