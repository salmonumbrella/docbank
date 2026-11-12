package main

import (
	"bytes"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/store"
)

func TestSignatureSignerParsing(t *testing.T) {
	for _, tc := range []struct{ input, role, email, name string }{
		{"Signer=signer@example.com:Example", "Signer", "signer@example.com", "Example"},
		{`{"role":"A=B","email":"signer@example.com","name":"Example: Signer"}`, "A=B", "signer@example.com", "Example: Signer"},
	} {
		v, err := parseSignatureSigner(tc.input)
		require.NoError(t, err)
		require.Equal(t, tc.role, v.Role)
		require.Equal(t, tc.email, v.Email)
		require.Equal(t, tc.name, v.Name)
	}
	for _, raw := range []string{`{"role":"Signer","email":"a@example.com","Email":"b@example.com"}`, `{"role":"Signer","role":"Other","email":"a@example.com"}`, `{"role":null,"email":"a@example.com"}`, "Signer=Name <a@example.com>"} {
		_, err := parseSignatureSigner(raw)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "a@example.com")
	}
}

func TestSignatureRequestLostReplyGuidesHistory(t *testing.T) {
	const contractID = "11111111-1111-4111-8111-111111111111"
	var posts atomic.Int32
	setupFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/contracts/"+contractID+"/signature-requests" {
			http.NotFound(w, r)
			return
		}
		posts.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response cannot be disconnected")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	_, err := runCLI(t, "contract", "sign", "request", contractID, "--revision", "1", "--template", "7", "--signer", "Signer=signer@example.com:Example")
	require.ErrorContains(t, err, "signature request outcome unknown")
	require.ErrorContains(t, err, "docbank contract sign status "+contractID+" before retrying")
	require.EqualValues(t, 1, posts.Load())
}

func TestSignatureRequestDisclosureAndOutput(t *testing.T) {
	cmd := newSignatureRequestCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	request := store.SignatureRequest{SignatureRequestSummary: {RequestID: "request", State: "sent", Revision: 1, EndpointOrigin: "https://sign.example.test", TemplateName: "Example template"}, Signers: []store.SignatureSigner{{Role: "Signer", Name: "Example", Email: "signer@example.com", State: "awaiting"}}}
	require.NoError(t, printSignatureRequest(cmd, request, false))
	require.Contains(t, out.String(), "https://sign.example.test")
	require.Contains(t, out.String(), "Example template")
	require.Contains(t, out.String(), `"Signer"	"Example"	"signer@example.com"	awaiting`)
}

func TestSignatureRequestPreflightProblemIsDefinite(t *testing.T) {
	const contractID = "11111111-1111-4111-8111-111111111111"
	var posts atomic.Int32
	setupFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/contracts/"+contractID+"/signature-requests" {
			http.NotFound(w, r)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"title":"Bad Gateway","status":502,"detail":"Check the DocuSeal API path","code":"docuseal_not_found"}`))
	})
	_, err := runCLI(t, "contract", "sign", "request", contractID, "--revision", "1", "--template", "7", "--signer", "Signer=signer@example.com:Example")
	require.Error(t, err)
	code, ok := daemonconn.ProblemCode(err)
	require.True(t, ok)
	require.Equal(t, "docuseal_not_found", code)
	require.ErrorContains(t, err, "Check the DocuSeal API path")
	require.NotContains(t, err.Error(), "outcome unknown")
	require.EqualValues(t, 1, posts.Load())
}
