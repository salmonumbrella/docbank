package api_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/store"
)

// Cancel must fence the request before archiving and retain the contract state.
func TestSignatureCancellationConfirmedAndRevisionFence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		revisionOffset int64
		missing        bool
		status         int
		deletes        int32
	}{
		{"missing revision", 0, true, http.StatusPreconditionRequired, 0},
		{"stale revision", -1, false, http.StatusPreconditionFailed, 0},
		{"confirmed cancellation", 0, false, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/templates/7":
					_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Signer"}]}`))
				case "POST /api/submissions":
					var body struct {
						Submitters []struct {
							ExternalID string `json:"external_id"`
						} `json:"submitters"`
					}
					if err := json.UnmarshalRead(r.Body, &body); err != nil || len(body.Submitters) != 1 {
						http.Error(w, "invalid input", http.StatusBadRequest)
						return
					}
					_, _ = fmt.Fprintf(w, `[{"id":11,"submission_id":10,"role":"Signer","external_id":%q}]`, body.Submitters[0].ExternalID)
				case "DELETE /api/submissions/10":
					deletes.Add(1)
					if r.URL.RawQuery != "" {
						http.Error(w, "unexpected query", http.StatusBadRequest)
						return
					}
					_, _ = w.Write([]byte(`{"id":10,"archived_at":"2026-01-01T00:00:00Z"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			t.Setenv("DOCUSEAL_CANCEL_TEST_KEY", "synthetic-key")
			ts, _ := newTestServer(t, func(d *api.Deps) {
				d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIPath: "/api", APIKeyEnv: "DOCUSEAL_CANCEL_TEST_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
			})
			resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
			require.Equal(t, http.StatusCreated, resp.StatusCode, body)
			var contract api.Contract
			require.NoError(t, json.Unmarshal([]byte(body), &contract))
			resp, body = do(t, ts, http.MethodPost, "/api/v1/contracts/"+contract.ContractID+"/signature-requests", map[string]string{"If-Match": `"1"`}, map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "Signer", "email": "signer@example.com"}}})
			require.Equal(t, http.StatusCreated, resp.StatusCode, body)
			var request struct {
				RequestID string `json:"request_id"`
				State     string `json:"state"`
				Revision  int64  `json:"revision"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &request))
			require.Equal(t, "sent", request.State)
			require.NotEmpty(t, request.RequestID)
			headers := map[string]string{}
			if !tc.missing {
				headers["If-Match"] = fmt.Sprintf(`"%d"`, request.Revision+tc.revisionOffset)
			}
			resp, body = do(t, ts, http.MethodPost, "/api/v1/signature-requests/"+request.RequestID+"/cancel", headers, nil)
			require.Equal(t, tc.status, resp.StatusCode, body)
			require.Equal(t, tc.deletes, deletes.Load())
			if tc.status == http.StatusOK {
				require.Contains(t, body, `"state":"cancelled"`)
				resp, body = do(t, ts, http.MethodGet, "/api/v1/contracts/"+contract.ContractID, nil, nil)
				require.Equal(t, http.StatusOK, resp.StatusCode, body)
				require.NoError(t, json.Unmarshal([]byte(body), &contract))
				require.Equal(t, "pending_signature", contract.Status)
			}
		})
	}
}

// Existing audit authority refuses both sent and already cancelled requests before provider I/O.
func TestSignatureCancellationAuditRefusesBeforeProvider(t *testing.T) {
	for _, state := range []string{"sent", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			var deletes atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				deletes.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer provider.Close()
			t.Setenv("DOCUSEAL_CANCEL_AUDIT_KEY", "synthetic-key")
			ts, s := newTestServer(t, func(d *api.Deps) {
				d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIPath: "/api", APIKeyEnv: "DOCUSEAL_CANCEL_AUDIT_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
			})
			contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
			require.NoError(t, err)
			request, err := s.BeginSignatureRequest(t.Context(), contract.ContractID, contract.Revision, store.SignatureRequestInput{EndpointOrigin: provider.URL, APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}})
			require.NoError(t, err)
			request, err = s.FinishSignatureCreation(t.Context(), request.RequestID, request.Revision, store.SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
			require.NoError(t, err)
			if state == "cancelled" {
				request, err = s.FinishSignatureCancellation(t.Context(), request.RequestID, request.Revision, "")
				require.NoError(t, err)
			}
			dir, err := s.Mkdir(t.Context(), s.RootID(), "Audited")
			require.NoError(t, err)
			client := daemonconn.New(ts.URL, testAPIKey)
			preview, err := client.PreviewAudit(t.Context(), daemonconn.AuditPreviewOptions{NodeID: dir.ID})
			require.NoError(t, err)
			_, err = client.EnableAudit(t.Context(), preview.PreviewToken, true)
			require.NoError(t, err)
			resp, body := do(t, ts, http.MethodPost, "/api/v1/signature-requests/"+request.RequestID+"/cancel", map[string]string{"If-Match": fmt.Sprintf(`"%d"`, request.Revision)}, nil)
			require.Equal(t, http.StatusConflict, resp.StatusCode, body)
			require.Contains(t, body, "audit_mutation_unsupported")
			require.Zero(t, deletes.Load())
		})
	}
}

func TestSignatureCancellationAuditEnrolledDuringProviderCall(t *testing.T) {
	var client *daemonconn.Connection
	var target int64
	var deletes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deletes.Add(1)
		preview, err := client.PreviewAudit(r.Context(), daemonconn.AuditPreviewOptions{NodeID: target})
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = client.EnableAudit(r.Context(), preview.PreviewToken, true)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":10,"archived_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer provider.Close()
	t.Setenv("DOCUSEAL_CANCEL_RACE_KEY", "synthetic-key")
	ts, s := newTestServer(t, func(d *api.Deps) {
		d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIPath: "/api", APIKeyEnv: "DOCUSEAL_CANCEL_RACE_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
	})
	client = daemonconn.New(ts.URL, testAPIKey)
	contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
	require.NoError(t, err)
	request, err := s.BeginSignatureRequest(t.Context(), contract.ContractID, contract.Revision, store.SignatureRequestInput{EndpointOrigin: provider.URL, APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}})
	require.NoError(t, err)
	request, err = s.FinishSignatureCreation(t.Context(), request.RequestID, request.Revision, store.SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
	require.NoError(t, err)
	dir, err := s.Mkdir(t.Context(), s.RootID(), "Audited")
	require.NoError(t, err)
	target = dir.ID
	resp, body := do(t, ts, http.MethodPost, "/api/v1/signature-requests/"+request.RequestID+"/cancel", map[string]string{"If-Match": fmt.Sprintf(`"%d"`, request.Revision)}, nil)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode, body)
	require.Contains(t, body, "docuseal_cancellation_unknown")
	require.EqualValues(t, 1, deletes.Load())
	current, err := s.GetSignatureRequest(t.Context(), request.RequestID)
	require.NoError(t, err)
	require.Equal(t, request, current)
}
