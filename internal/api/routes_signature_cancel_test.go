package api_test

import (
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

// Cancel must fence the request before archiving and retain the contract state.
func TestSignatureCancellationConfirmedAndRevisionFence(t *testing.T) {
	for _, tc := range []struct {
		name, revision string
		status         int
		deletes        int32
	}{
		{"missing revision", "", http.StatusPreconditionRequired, 0},
		{"stale revision", `"2"`, http.StatusPreconditionFailed, 0},
		{"confirmed cancellation", `"1"`, http.StatusOK, 1},
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
			}
			require.NoError(t, json.Unmarshal([]byte(body), &request))
			require.Equal(t, "sent", request.State)
			require.NotEmpty(t, request.RequestID)
			headers := map[string]string{}
			if tc.revision != "" {
				headers["If-Match"] = tc.revision
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
