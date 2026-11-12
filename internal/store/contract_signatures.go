package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/docbank/document"
)

var ErrInvalidSignatureRequest = errors.New("invalid signature request")
var ErrSignatureRequestOpen = errors.New("signature request is open")

type SignatureSignerInput struct {
	Role  string `json:"role"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type SignatureRequestInput struct {
	EndpointOrigin, APIPath, TemplateID, TemplateName string
	Signers                                           []SignatureSignerInput
}
type SignatureRequestSummary struct {
	RequestID            string  `json:"request_id"`
	ContractID           string  `json:"contract_id"`
	Provider             string  `json:"provider"`
	EndpointOrigin       string  `json:"endpoint_origin"`
	APIPath              string  `json:"api_path"`
	TemplateID           string  `json:"template_id"`
	TemplateName         string  `json:"template_name"`
	ProviderSubmissionID *string `json:"provider_submission_id" nullable:"true"`
	State                string  `json:"state"`
	LastErrorCode        string  `json:"last_error_code"`
	Revision             int64   `json:"revision"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}
type SignatureSigner struct {
	Position            int     `json:"position"`
	Role                string  `json:"role"`
	Name                string  `json:"name"`
	Email               string  `json:"email"`
	ProviderSubmitterID *string `json:"provider_submitter_id" nullable:"true"`
	State               string  `json:"state"`
}
type SignatureRequest struct {
	SignatureRequestSummary
	Signers []SignatureSigner `json:"signers"`
}
type SignatureCreationResult struct {
	SubmissionID string
	SubmitterIDs []string
	ErrorCode    string
	Uncertain    bool
}

func validSignatureText(s string, n int, empty bool) bool {
	return len(s) <= n && (empty || s != "") && document.ValidPersonIdentityText(s)
}
func ValidSignatureSigner(s SignatureSignerInput) bool {
	if !validSignatureText(s.Role, 4096, false) || !validSignatureText(s.Name, 512, true) || len(s.Email) > 1016 || utf8.RuneCountInString(s.Email) > 254 || !document.ValidPersonIdentityText(s.Email) || strings.TrimSpace(s.Email) != s.Email {
		return false
	}
	return document.ValidBarePersonEmail(s.Email)
}
func signaturePositiveID(s string) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func validSignatureDestination(origin, apiPath string) bool {
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != origin {
		return false
	}
	return apiPath == "" || (strings.HasPrefix(apiPath, "/") && apiPath != "/" && path.Clean(apiPath) == apiPath && !strings.ContainsAny(apiPath, "?#%\\") && strings.IndexFunc(apiPath, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0)
}
func validSignatureInput(in SignatureRequestInput) bool {
	if !validSignatureDestination(in.EndpointOrigin, in.APIPath) || !signaturePositiveID(in.TemplateID) || !validSignatureText(in.TemplateName, 512, false) || len(in.Signers) < 1 || len(in.Signers) > 20 {
		return false
	}
	roles := map[string]bool{}
	for _, s := range in.Signers {
		if !ValidSignatureSigner(s) || roles[s.Role] {
			return false
		}
		roles[s.Role] = true
	}
	return true
}

const signatureColumns = `request_id,contract_id,provider,endpoint_origin,api_path,template_id,template_name,provider_submission_id,state,last_error_code,revision,created_at,updated_at`

func scanSignature(row interface{ Scan(...any) error }) (SignatureRequestSummary, error) {
	var v SignatureRequestSummary
	err := row.Scan(&v.RequestID, &v.ContractID, &v.Provider, &v.EndpointOrigin, &v.APIPath, &v.TemplateID, &v.TemplateName, &v.ProviderSubmissionID, &v.State, &v.LastErrorCode, &v.Revision, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func getSignatureTx(ctx context.Context, tx *sql.Tx, id string) (SignatureRequest, error) {
	v, err := scanSignature(tx.QueryRowContext(ctx, `SELECT `+signatureColumns+` FROM contract_signature_requests WHERE request_id=$1`, id))
	if err != nil {
		return SignatureRequest{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT position,role,name,email,provider_submitter_id,state FROM contract_signature_signers WHERE request_id=$1 ORDER BY position`, id)
	if err != nil {
		return SignatureRequest{}, err
	}
	defer func() { _ = rows.Close() }()
	out := SignatureRequest{SignatureRequestSummary: v, Signers: []SignatureSigner{}}
	for rows.Next() {
		var s SignatureSigner
		if err = rows.Scan(&s.Position, &s.Role, &s.Name, &s.Email, &s.ProviderSubmitterID, &s.State); err != nil {
			return SignatureRequest{}, err
		}
		out.Signers = append(out.Signers, s)
	}
	return out, rows.Err()
}
func (s *Store) BeginSignatureRequest(ctx context.Context, contractID string, rev int64, in SignatureRequestInput) (SignatureRequest, error) {
	if !validSignatureInput(in) {
		return SignatureRequest{}, ErrInvalidSignatureRequest
	}
	id, err := newUUIDv4()
	if err != nil {
		return SignatureRequest{}, err
	}
	now := nowRFC3339()
	var out SignatureRequest
	err = s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		c, e := getContractTx(ctx, tx, contractID, time.Now().Format(time.DateOnly))
		if e != nil {
			return e
		}
		if c.Revision != rev {
			return ErrStaleRevision
		}
		open, e := hasOpenSignatureTx(ctx, tx, contractID)
		if e != nil {
			return e
		}
		if open {
			return ErrSignatureRequestOpen
		}

		_, e = tx.ExecContext(ctx, `INSERT INTO contract_signature_requests (`+signatureColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, contractID, "docuseal", in.EndpointOrigin, in.APIPath, in.TemplateID, in.TemplateName, nil, "creating", "", 1, now, now)
		if e != nil {
			return e
		}
		for i, p := range in.Signers {
			if _, e = tx.ExecContext(ctx, `INSERT INTO contract_signature_signers(request_id,position,role,name,email,provider_submitter_id,state) VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, i, p.Role, p.Name, p.Email, nil, "awaiting"); e != nil {
				return e
			}
		}
		out, e = getSignatureTx(ctx, tx, id)
		return e
	})
	return out, err
}
func (s *Store) FinishSignatureCreation(ctx context.Context, id string, rev int64, result SignatureCreationResult) (SignatureRequest, error) {
	var out SignatureRequest
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		v, e := getSignatureTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if v.Revision != rev || v.State != "creating" {
			return ErrStaleRevision
		}
		state, code := "creating", result.ErrorCode
		var submission any
		if result.SubmissionID != "" {
			if !signaturePositiveID(result.SubmissionID) || len(result.SubmitterIDs) != len(v.Signers) {
				return ErrInvalidSignatureRequest
			}
			seen := map[string]bool{}
			for i, p := range result.SubmitterIDs {
				if !signaturePositiveID(p) || seen[p] {
					return ErrInvalidSignatureRequest
				}
				seen[p] = true
				if _, e = tx.ExecContext(ctx, `UPDATE contract_signature_signers SET provider_submitter_id=$1 WHERE request_id=$2 AND position=$3`, p, id, i); e != nil {
					return e
				}
			}
			submission = result.SubmissionID
			state = "sent"
			code = ""
		} else if !result.Uncertain {
			state = "failed"
		}
		if code != "" && code != "docuseal_creation_unknown" && code != "docuseal_creation_failed" {
			return ErrInvalidSignatureRequest
		}
		if _, e = tx.ExecContext(ctx, `UPDATE contract_signature_requests SET state=$1,last_error_code=$2,provider_submission_id=$3,revision=revision+1,updated_at=$4 WHERE request_id=$5`, state, code, submission, nowRFC3339(), id); e != nil {
			return e
		}
		if state == "sent" {
			if _, e = tx.ExecContext(ctx, `UPDATE contracts SET status='pending_signature',revision=revision+1,updated_at=$1 WHERE contract_id=$2 AND status='draft'`, nowRFC3339(), v.ContractID); e != nil {
				return e
			}
		}
		out, e = getSignatureTx(ctx, tx, id)
		return e
	})
	return out, err
}
func (s *Store) GetSignatureRequest(ctx context.Context, id string) (SignatureRequest, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.postgresSchema != "" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return SignatureRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	v, err := getSignatureTx(ctx, tx, id)
	if err != nil {
		return SignatureRequest{}, err
	}
	return v, tx.Commit()
}
func (s *Store) ListSignatureRequests(ctx context.Context, id string, limit, offset int) ([]SignatureRequestSummary, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, ErrInvalidSignatureRequest
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.postgresSchema != "" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = getContractTx(ctx, tx, id, time.Now().Format(time.DateOnly)); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+signatureColumns+` FROM contract_signature_requests WHERE contract_id=$1 ORDER BY created_at DESC,request_id DESC LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []SignatureRequestSummary{}
	for rows.Next() {
		v, e := scanSignature(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("list signatures: %w", err)
	}
	return items, nil
}

func hasOpenSignatureTx(ctx context.Context, tx *sql.Tx, contractID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT state FROM contract_signature_requests WHERE contract_id=$1`, contractID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var state string
		if err = rows.Scan(&state); err != nil {
			return false, err
		}
		if signatureOpen(state) {
			return true, nil
		}
	}
	return false, rows.Err()
}
