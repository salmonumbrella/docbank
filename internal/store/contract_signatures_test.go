package store

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func signatureFixture() SignatureRequestInput {
	return SignatureRequestInput{EndpointOrigin: "https://sign.example.com", APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []SignatureSignerInput{{Role: "Signer", Name: "Example", Email: "signer@example.com"}}}
}

// Admission, settling and deletion share one transactional revision fence on each backend.
func checkSignatureLifecycle(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	c, err := s.CreateContract(ctx, ContractInput{Title: "Before"})
	require.NoError(t, err)
	_, err = s.BeginSignatureRequest(ctx, c.ContractID, c.Revision+1, signatureFixture())
	require.ErrorIs(t, err, ErrStaleRevision)
	req, err := s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.NoError(t, err)
	require.Equal(t, "creating", req.State)
	_, err = s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.ErrorIs(t, err, ErrSignatureRequestOpen)
	require.ErrorIs(t, s.DeleteContract(ctx, c.ContractID, c.Revision), ErrSignatureRequestOpen)
	c, err = s.UpdateContract(ctx, c.ContractID, c.Revision, ContractInput{Title: "Concurrent edit", Status: "draft"})
	require.NoError(t, err)
	req, err = s.FinishSignatureCreation(ctx, req.RequestID, req.Revision, SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
	require.NoError(t, err)
	require.Equal(t, "sent", req.State)
	c, err = s.GetContract(ctx, c.ContractID, "2026-01-01")
	require.NoError(t, err)
	require.Equal(t, "Concurrent edit", c.Title)
	require.Equal(t, "pending_signature", c.Status)
	_, err = s.FinishSignatureCreation(ctx, req.RequestID, req.Revision-1, SignatureCreationResult{Uncertain: true, ErrorCode: "docuseal_creation_unknown"})
	require.ErrorIs(t, err, ErrStaleRevision)
	listed, err := s.ListSignatureRequests(ctx, c.ContractID, 50, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

// Definite failure is terminal and permits a fresh intent; it does not change the contract.
func checkSignatureFailedRetry(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	c, err := s.CreateContract(ctx, ContractInput{Title: "Active", Status: "active"})
	require.NoError(t, err)
	req, err := s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.NoError(t, err)
	req, err = s.FinishSignatureCreation(ctx, req.RequestID, req.Revision, SignatureCreationResult{ErrorCode: "docuseal_creation_failed"})
	require.NoError(t, err)
	require.Equal(t, "failed", req.State)
	next, err := s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.NoError(t, err)
	require.NotEqual(t, req.RequestID, next.RequestID)
	_, err = s.FinishSignatureCreation(ctx, next.RequestID, next.Revision, SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
	require.NoError(t, err)
	c, err = s.GetContract(ctx, c.ContractID, "2026-01-01")
	require.NoError(t, err)
	require.Equal(t, "active", c.Status)
	require.EqualValues(t, 1, c.Revision)
}
func TestSignatureEmailUnicodeIngress(t *testing.T) {
	t.Parallel()
	v := signatureFixture().Signers[0]
	v.Email = strings.Repeat("界", 120) + "@example.com"
	require.True(t, ValidSignatureSigner(v))
	v.Email = strings.Repeat("界", 250) + "@example.com"
	require.False(t, ValidSignatureSigner(v))
}

func checkSignatureTerminalHistoryDeletion(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	c, err := s.CreateContract(ctx, ContractInput{Title: "Example"})
	require.NoError(t, err)
	req, err := s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.NoError(t, err)
	_, err = s.FinishSignatureCreation(ctx, req.RequestID, req.Revision, SignatureCreationResult{ErrorCode: "docuseal_creation_failed"})
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteContract(ctx, c.ContractID, c.Revision+1), ErrStaleRevision)
	retained, err := s.GetSignatureRequest(ctx, req.RequestID)
	require.NoError(t, err)
	require.Len(t, retained.Signers, 1)
	require.NoError(t, s.DeleteContract(ctx, c.ContractID, c.Revision))
	_, err = s.GetSignatureRequest(ctx, req.RequestID)
	require.ErrorIs(t, err, ErrNotFound)
	var signers int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_signature_signers WHERE request_id=$1`, req.RequestID).Scan(&signers))
	require.Zero(t, signers)
}

// An uncertain result stays open and remains visible in paginated local history.
func checkSignatureUncertainHistory(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	c, err := s.CreateContract(ctx, ContractInput{Title: "Example"})
	require.NoError(t, err)
	pending, err := s.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
	require.NoError(t, err)
	pending, err = s.FinishSignatureCreation(ctx, pending.RequestID, pending.Revision, SignatureCreationResult{Uncertain: true, ErrorCode: "docuseal_creation_unknown"})
	require.NoError(t, err)
	require.Equal(t, "creating", pending.State)
	all, err := s.ListSignatureRequests(ctx, c.ContractID, 50, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	page, err := s.ListSignatureRequests(ctx, c.ContractID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, all, page)
}

// Exercise imported boundary revisions on each native backend.
func checkSignatureRevisionExhaustion(t *testing.T, postgres bool) {
	t.Helper()
	for _, action := range []string{"draft-begin", "active", "concurrent-edit", "request-revision"} {
		t.Run(action, func(t *testing.T) {
			ctx := t.Context()
			source := newTestStore(t)
			status := "draft"
			if action == "active" {
				status = "active"
			}
			c, err := source.CreateContract(ctx, ContractInput{Title: "Example", Status: status})
			require.NoError(t, err)
			var req SignatureRequest
			if action == "request-revision" {
				req, err = source.BeginSignatureRequest(ctx, c.ContractID, c.Revision, signatureFixture())
				require.NoError(t, err)
			}
			var exported bytes.Buffer
			require.NoError(t, source.ExportMetadata(ctx, &exported))
			lines := strings.Split(exported.String(), "\n")
			target := `"type":"contract"`
			id := c.ContractID
			revision := int64(math.MaxInt64)
			if action == "concurrent-edit" {
				revision--
			}
			if action == "request-revision" {
				target = `"type":"contract_signature_request"`
				id = req.RequestID
			}
			found := false
			for i, line := range lines {
				if strings.Contains(line, target) && strings.Contains(line, id) {
					lines[i] = strings.Replace(line, `"revision":1`, `"revision":`+strconv.FormatInt(revision, 10), 1)
					require.NotEqual(t, line, lines[i])
					found = true
				}
			}
			require.True(t, found)
			s := newTestStore(t)
			require.NoError(t, s.ImportMetadata(ctx, strings.NewReader(strings.Join(lines, "\n"))))
			if postgres {
				s, _ = copyRetentionStore(t, s)
			}
			before, err := s.GetContract(ctx, c.ContractID, "2026-01-01")
			require.NoError(t, err)
			if action != "request-revision" {
				require.Equal(t, revision, before.Revision)
			}
			if action == "request-revision" {
				req, err = s.GetSignatureRequest(ctx, req.RequestID)
				require.NoError(t, err)
				require.Equal(t, revision, req.Revision)
			}
			if action == "concurrent-edit" || action == "active" {
				req, err = s.BeginSignatureRequest(ctx, c.ContractID, before.Revision, signatureFixture())
				require.NoError(t, err)
			}
			if action == "concurrent-edit" {
				before, err = s.UpdateContract(ctx, c.ContractID, before.Revision, ContractInput{Title: "Changed", Status: "draft"})
				require.NoError(t, err)
				require.Equal(t, int64(math.MaxInt64), before.Revision)
			}
			var snapshot bytes.Buffer
			require.NoError(t, s.ExportMetadata(ctx, &snapshot))
			if action == "draft-begin" {
				_, err = s.BeginSignatureRequest(ctx, c.ContractID, before.Revision, signatureFixture())
				require.ErrorIs(t, err, ErrInvalidContract)
				requests, e := s.ListSignatureRequests(ctx, c.ContractID, 50, 0)
				require.NoError(t, e)
				require.Empty(t, requests)
			} else {
				_, err = s.FinishSignatureCreation(ctx, req.RequestID, req.Revision, SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
				if action == "active" {
					require.NoError(t, err)
				} else if action == "concurrent-edit" {
					require.ErrorIs(t, err, ErrInvalidContract)
				} else {
					require.Error(t, err)
				}
			}
			after, err := s.GetContract(ctx, c.ContractID, "2026-01-01")
			require.NoError(t, err)
			require.Equal(t, before, after)
			if action != "active" {
				if action != "draft-begin" {
					got, e := s.GetSignatureRequest(ctx, req.RequestID)
					require.NoError(t, e)
					require.Equal(t, req, got)
					require.Equal(t, "creating", got.State)
					require.Nil(t, got.ProviderSubmissionID)
					require.Nil(t, got.Signers[0].ProviderSubmitterID)
				}
				var afterExport bytes.Buffer
				require.NoError(t, s.ExportMetadata(ctx, &afterExport))
				require.Equal(t, snapshot.Bytes(), afterExport.Bytes())
			}
		})
	}
}
