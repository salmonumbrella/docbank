package api_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/config"
)

// Creating a signing request must require the current contract revision before any provider call.
func TestSignatureRequestRequiresContractRevision(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	resp, body = do(t, ts, http.MethodPost, "/api/v1/contracts/"+c.ContractID+"/signature-requests", nil, map[string]any{"template_id": 1, "signers": []any{map[string]any{"role": "Signer", "email": "signer@example.com", "name": "Example"}}})
	require.Equal(t, http.StatusPreconditionRequired, resp.StatusCode, body)
}

// A provider failure after the POST must retain an open intent, blocking another invitation.
func TestSignatureRequestUncertainCreationKeepsLocalHistory(t *testing.T) {
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/templates/7":
			_, _ = w.Write([]byte(`{"id":7,"name":"Agreement","submitters":[{"name":"Signer"}]}`))
		case "/api/submissions":
			posts.Add(1)
			http.Error(w, "private provider diagnostic", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	t.Setenv("DOCUSEAL_SIGNATURE_TEST_KEY", "synthetic-key")
	ts, _ := newTestServer(t, func(d *api.Deps) {
		d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIPath: "/api", APIKeyEnv: "DOCUSEAL_SIGNATURE_TEST_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
	})
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Private agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	path := "/api/v1/contracts/" + c.ContractID + "/signature-requests"
	input := map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "Signer", "email": "signer@example.com", "name": "Example"}}}
	resp, body = do(t, ts, http.MethodPost, path, map[string]string{"If-Match": `"1"`}, input)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode, body)
	require.Contains(t, body, "docuseal_creation_unknown")
	require.NotContains(t, body, "private provider diagnostic")
	var problem struct {
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &problem))
	require.NotEmpty(t, problem.RequestID)
	resp, body = do(t, ts, http.MethodGet, "/api/v1/signature-requests/"+problem.RequestID, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, `"state":"creating"`)
	resp, body = do(t, ts, http.MethodPost, path, map[string]string{"If-Match": `"1"`}, input)
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	require.EqualValues(t, 1, posts.Load())
}

// The provider receives only the template and signers, in template role order.
func TestSignatureRequestDisclosesOnlySigners(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/templates/7":
			_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Tenant"},{"name":"Owner"}]}`))
		case "/submissions":
			var got struct {
				TemplateID int              `json:"template_id"`
				SendEmail  bool             `json:"send_email"`
				Submitters []map[string]any `json:"submitters"`
			}
			decoder := jsontext.NewDecoder(r.Body)
			var all map[string]any
			if err := json.UnmarshalDecode(decoder, &all); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if len(all) != 3 {
				t.Errorf("unexpected request fields: %v", all)
				w.WriteHeader(500)
				return
			}
			raw, _ := json.Marshal(all)
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if got.TemplateID != 7 || !got.SendEmail || len(got.Submitters) != 2 {
				t.Error("invalid submission")
				w.WriteHeader(500)
				return
			}
			for i, role := range []string{"Tenant", "Owner"} {
				signer := got.Submitters[i]
				if len(signer) != 4 || signer["role"] != role {
					t.Error("invalid signer projection")
					w.WriteHeader(500)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `[{"id":11,"submission_id":10,"role":"Tenant","external_id":%q,"embed_src":"https://example.com/private-link"},{"id":12,"submission_id":10,"role":"Owner","external_id":%q}]`, got.Submitters[0]["external_id"], got.Submitters[1]["external_id"])
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	t.Setenv("DOCUSEAL_DISCLOSURE_TEST_KEY", "synthetic-key")
	ts, _ := newTestServer(t, func(d *api.Deps) {
		d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIKeyEnv: "DOCUSEAL_DISCLOSURE_TEST_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
	})
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Private agreement", "summary": "Private terms"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	path := "/api/v1/contracts/" + c.ContractID + "/signature-requests"
	resp, body = do(t, ts, http.MethodPost, path, map[string]string{"If-Match": `"1"`}, map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "Owner", "email": "owner@example.com", "name": "Owner"}, map[string]any{"role": "Tenant", "email": "tenant@example.com", "name": "Tenant"}}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	require.Contains(t, body, `"state":"sent"`)
	require.NotContains(t, body, "private-link")
	resp, body = do(t, ts, http.MethodGet, "/api/v1/contracts/"+c.ContractID, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	require.Equal(t, "pending_signature", c.Status)
	require.Equal(t, "Private terms", c.Summary)
}

// Role mismatch must be rejected before the irreversible invitation POST.
func TestSignatureRequestRejectsMismatchedRolesBeforePost(t *testing.T) {
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Tenant"}]}`))
	}))
	defer provider.Close()
	t.Setenv("DOCUSEAL_ROLE_TEST_KEY", "synthetic-key")
	ts, _ := newTestServer(t, func(d *api.Deps) {
		d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIKeyEnv: "DOCUSEAL_ROLE_TEST_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
	})
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	resp, body = do(t, ts, http.MethodPost, "/api/v1/contracts/"+c.ContractID+"/signature-requests", map[string]string{"If-Match": `"1"`}, map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "tenant", "email": "signer@example.com", "name": "Signer"}}})
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, body)
	require.Contains(t, body, "invalid_signature_request")
	require.Zero(t, posts.Load())
}
