package store

import (
	"context"
	"errors"
)

type metadataSignatureRequest struct {
	Type                 string  `json:"type"`
	RequestID            string  `json:"request_id" db:"request_id"`
	ContractID           string  `json:"contract_id" db:"contract_id"`
	Provider             string  `json:"provider" db:"provider"`
	EndpointOrigin       string  `json:"endpoint_origin" db:"endpoint_origin"`
	APIPath              string  `json:"api_path" db:"api_path"`
	TemplateID           string  `json:"template_id" db:"template_id"`
	TemplateName         string  `json:"template_name" db:"template_name"`
	ProviderSubmissionID *string `json:"provider_submission_id" db:"provider_submission_id"`
	State                string  `json:"state" db:"state"`
	LastErrorCode        string  `json:"last_error_code" db:"last_error_code"`
	Revision             int64   `json:"revision" db:"revision"`
	CreatedAt            string  `json:"created_at" db:"created_at"`
	UpdatedAt            string  `json:"updated_at" db:"updated_at"`
}
type metadataSignatureSigner struct {
	Type                string  `json:"type"`
	RequestID           string  `json:"request_id" db:"request_id"`
	Position            int     `json:"position" db:"position"`
	Role                string  `json:"role" db:"role"`
	Name                string  `json:"name" db:"name"`
	Email               string  `json:"email" db:"email"`
	ProviderSubmitterID *string `json:"provider_submitter_id" db:"provider_submitter_id"`
	State               string  `json:"state" db:"state"`
}

var signatureRequestMetadata = newMetadataTable(metadataTable[metadataSignatureRequest]{record: metadataSignatureRequest{Type: "contract_signature_request"}, table: "contract_signature_requests", suffix: "ORDER BY request_id", validate: validateSignatureRequestMetadata, checkExport: true})
var signatureSignerMetadata = newMetadataTable(metadataTable[metadataSignatureSigner]{record: metadataSignatureSigner{Type: "contract_signature_signer"}, table: "contract_signature_signers", suffix: "ORDER BY request_id,position", validate: validateSignatureSignerMetadata, checkExport: true})

func signatureOpen(s string) bool {
	switch s {
	case "creating", "sent", "partially_signed", stateCompleted:
		return true
	}
	return false
}
func validSignatureState(s string) bool {
	switch s {
	case "creating", "sent", "partially_signed", stateCompleted, "stored", "declined", "expired", "cancelled", stateFailed:
		return true
	}
	return false
}
func validateSignatureRequestMetadata(v metadataSignatureRequest) error {
	if v.Type != "contract_signature_request" || validateUUIDv4(v.RequestID) != nil || validateUUIDv4(v.ContractID) != nil || v.Provider != "docuseal" || !validSignatureDestination(v.EndpointOrigin, v.APIPath) || !signaturePositiveID(v.TemplateID) || !validSignatureText(v.TemplateName, 512, false) || !validSignatureState(v.State) || v.Revision < 1 {
		return errors.New("invalid signature request metadata")
	}
	if v.LastErrorCode != "" && v.LastErrorCode != "docuseal_creation_unknown" && v.LastErrorCode != "docuseal_creation_failed" && v.LastErrorCode != "docuseal_cancellation_unknown" && v.LastErrorCode != "docuseal_cancellation_failed" {
		return errors.New("invalid signature error code")
	}
	if v.ProviderSubmissionID != nil && !signaturePositiveID(*v.ProviderSubmissionID) {
		return errors.New("invalid signature submission ID")
	}
	if (v.State == "creating" || v.State == stateFailed) != (v.ProviderSubmissionID == nil) {
		return errors.New("incoherent signature submission")
	}
	if err := validateMetadataTime("signature created_at", v.CreatedAt); err != nil {
		return err
	}
	return validateMetadataTime("signature updated_at", v.UpdatedAt)
}
func validateSignatureSignerMetadata(v metadataSignatureSigner) error {
	if v.Type != "contract_signature_signer" || validateUUIDv4(v.RequestID) != nil || v.Position < 0 || v.Position >= 20 || !ValidSignatureSigner(SignatureSignerInput{Role: v.Role, Name: v.Name, Email: v.Email}) {
		return errors.New("invalid signature signer metadata")
	}
	if v.ProviderSubmitterID != nil && !signaturePositiveID(*v.ProviderSubmitterID) {
		return errors.New("invalid signature submitter ID")
	}
	switch v.State {
	case "awaiting", "sent", "opened", stateCompleted, "declined":
		return nil
	}
	return errors.New("invalid signature signer state")
}
func validateSignatureMetadataState(ctx context.Context, q metadataQuerier) error {
	if err := signatureRequestMetadata.validateRows(ctx, q); err != nil {
		return err
	}
	if err := signatureSignerMetadata.validateRows(ctx, q); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT request_id,contract_id,state,provider_submission_id FROM contract_signature_requests ORDER BY request_id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type request struct {
		id, contract, state string
		submission          *string
	}
	var requests []request
	open := map[string]bool{}
	for rows.Next() {
		var v request
		if err = rows.Scan(&v.id, &v.contract, &v.state, &v.submission); err != nil {
			return err
		}
		if signatureOpen(v.state) {
			if open[v.contract] {
				return errors.New("multiple open signature requests")
			}
			open[v.contract] = true
		}
		requests = append(requests, v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, v := range requests {
		if err := func() error {
			r, err := q.QueryContext(ctx, `SELECT position,role,provider_submitter_id FROM contract_signature_signers WHERE request_id=$1 ORDER BY position`, v.id)
			if err != nil {
				return err
			}
			defer func() { _ = r.Close() }()
			count := 0
			roles := map[string]bool{}
			ids := map[string]bool{}
			for r.Next() {
				var pos int
				var role string
				var id *string
				if err := r.Scan(&pos, &role, &id); err != nil {
					return err
				}
				if pos != count || roles[role] || (id == nil) != (v.submission == nil) || (id != nil && ids[*id]) {
					return errors.New("invalid signature signer graph")
				}
				roles[role] = true
				if id != nil {
					ids[*id] = true
				}
				count++
			}
			if err := r.Err(); err != nil {
				return err
			}
			if err := r.Close(); err != nil {
				return err
			}
			if count < 1 || count > 20 {
				return errors.New("invalid signature signer count")
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}
