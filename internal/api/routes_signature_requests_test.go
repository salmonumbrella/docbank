package api_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/config"
)

// A provider failure after the POST must retain an open intent, blocking another invitation.
func TestSignatureRequestUncertainCreationKeepsLocalHistory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/templates/7":
			_, _ = w.Write([]byte(`{"id":7,"name":"Agreement","submitters":[{"name":"Signer"}]}`))
		case "/api/submissions":
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
}

// The provider receives only the template and signers, in template role order.
func TestSignatureRequestDisclosesOnlySigners(t *testing.T) {
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/templates/7":
			_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Tenant"},{"name":"Owner"}]}`))
		case "/submissions":
			posts.Add(1)
			var got struct {
				Submitters []struct {
					ExternalID string `json:"external_id"`
				} `json:"submitters"`
			}
			if err := json.UnmarshalRead(r.Body, &got); err != nil || len(got.Submitters) != 2 {
				http.Error(w, "invalid request", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":11,"submission_id":10,"role":"Tenant","external_id":%q},{"id":12,"submission_id":10,"role":"Owner","external_id":%q}]`, got.Submitters[0].ExternalID, got.Submitters[1].ExternalID)
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
	require.Equal(t, "draft", c.Status)
	path := "/api/v1/contracts/" + c.ContractID + "/signature-requests"
	resp, body = do(t, ts, http.MethodPost, path, map[string]string{"If-Match": `"1"`}, map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "Owner", "email": "owner@example.com", "name": "Owner"}, map[string]any{"role": "Tenant", "email": "tenant@example.com", "name": "Tenant"}}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var result struct {
		State                string  `json:"state"`
		ProviderSubmissionID *string `json:"provider_submission_id"`
		Signers              []struct {
			Role                string  `json:"role"`
			ProviderSubmitterID *string `json:"provider_submitter_id"`
		} `json:"signers"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	require.Equal(t, "sent", result.State)
	require.NotNil(t, result.ProviderSubmissionID)
	require.Equal(t, "10", *result.ProviderSubmissionID)
	require.Len(t, result.Signers, 2)
	require.Equal(t, "Tenant", result.Signers[0].Role)
	require.Equal(t, "Owner", result.Signers[1].Role)
	require.NotNil(t, result.Signers[0].ProviderSubmitterID)
	require.NotNil(t, result.Signers[1].ProviderSubmitterID)
	require.Equal(t, "11", *result.Signers[0].ProviderSubmitterID)
	require.Equal(t, "12", *result.Signers[1].ProviderSubmitterID)
	require.EqualValues(t, 1, posts.Load())
	resp, body = do(t, ts, http.MethodGet, "/api/v1/contracts/"+c.ContractID, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	require.Equal(t, "pending_signature", c.Status)
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
		_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Tenant"},{"name":"Owner"}]}`))
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
	signer := func(role string) map[string]any {
		return map[string]any{"role": role, "email": "signer@example.com", "name": "Signer"}
	}
	for _, signers := range [][]any{
		{signer("Tenant")},
		{signer("Tenant"), signer("Owner"), signer("Other")},
		{signer("Tenant"), signer("Tenant")},
		{signer("tenant"), signer("Owner")},
	} {
		resp, body = do(t, ts, http.MethodPost, "/api/v1/contracts/"+c.ContractID+"/signature-requests", map[string]string{"If-Match": `"1"`}, map[string]any{"template_id": 7, "signers": signers})
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, body)
		require.Contains(t, body, "invalid_signature_request")
		require.Zero(t, posts.Load())
	}
}

// Local history is available even when the optional connection is disabled.
func TestSignatureRequestHistoryAvailableWithoutConnection(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	resp, body = do(t, ts, http.MethodGet, "/api/v1/contracts/"+c.ContractID+"/signature-requests", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	var page struct {
		Items  []jsontext.Value `json:"items"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	require.NotNil(t, page.Items)
	require.Empty(t, page.Items)
	require.Equal(t, 50, page.Limit)
	require.Zero(t, page.Offset)
}

func TestSignatureRequestDefiniteProviderFailureAllowsRetry(t *testing.T) {
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			http.Error(w, "private provider diagnostic", http.StatusUnprocessableEntity)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Signer"}]}`))
	}))
	defer provider.Close()
	t.Setenv("DOCUSEAL_RETRY_TEST_KEY", "synthetic-key")
	ts, _ := newTestServer(t, func(d *api.Deps) {
		d.Cfg.Integrations.DocuSeal = config.DocuSealConfig{Enabled: true, Endpoint: provider.URL, APIKeyEnv: "DOCUSEAL_RETRY_TEST_KEY", TrustBoundary: "operator_network", AllowedCIDRs: []string{"127.0.0.0/8"}}
	})
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	path := "/api/v1/contracts/" + c.ContractID + "/signature-requests"
	input := map[string]any{"template_id": 7, "signers": []any{map[string]any{"role": "Signer", "email": "signer@example.com", "name": "Example"}}}
	for range 2 {
		resp, body = do(t, ts, http.MethodPost, path, map[string]string{"If-Match": `"1"`}, input)
		require.Equal(t, http.StatusBadGateway, resp.StatusCode, body)
		require.Contains(t, body, "docuseal_creation_failed")
		require.NotContains(t, body, "private provider diagnostic")
	}
	require.EqualValues(t, 2, posts.Load())
}

func TestSignatureRequestMalformedBodyIsPrivate(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	resp, body := do(t, ts, http.MethodPost, "/api/v1/contracts", nil, map[string]any{"title": "Agreement"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var c api.Contract
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	path := "/api/v1/contracts/" + c.ContractID + "/signature-requests"
	for _, tc := range []struct {
		name, contentType, raw string
		status                 int
	}{
		{"extra", "application/json", `{"template_id":7,"signers":[{"role":"Signer","name":"private-name","email":"private@example.com","extra":"private-link"}]}`, 422},
		{"null", "application/json", `{"template_id":7,"signers":[{"role":"Signer","name":"private-name","email":null}]}`, 422},
		{"malformed", "application/json", `{"signers":[{"name":"private-name"}`, 422},
		{"wrong type", "application/json", `{"template_id":"private-name","signers":[]}`, 422},
		{"duplicate", "application/json", `{"template_id":7,"template_id":8,"signers":[{"name":"private-name"}]}`, 422},
		{"unsupported content type", "text/plain", `{"template_id":7,"signers":[{"name":"private-name"}]}`, 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+path, strings.NewReader(tc.raw))
			require.NoError(t, err)
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("If-Match", `"1"`)
			resp, err := ts.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode, string(data))
			for _, secret := range []string{"private-name", "private@example.com", "private-link", "test-api-key"} {
				require.NotContains(t, string(data), secret)
			}
			require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		})
	}
}
