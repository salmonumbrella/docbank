package api_test

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
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
