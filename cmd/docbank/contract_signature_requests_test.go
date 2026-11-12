package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
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
	request := store.SignatureRequest{RequestID: "request", State: "sent", Revision: 1, EndpointOrigin: "https://sign.example.test", TemplateName: "Example template", Signers: []store.SignatureSigner{{Role: "Signer", Name: "Example", Email: "signer@example.com", State: "awaiting"}}}
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

// A confirmed send remains identifiable even when its output cannot be written.
func TestSignatureRequestConfirmedOutputFailure(t *testing.T) {
	const contractID = "11111111-1111-4111-8111-111111111111"
	const requestID = "22222222-2222-4222-8222-222222222222"
	for _, asJSON := range []bool{false, true} {
		t.Run(strconv.FormatBool(asJSON), func(t *testing.T) {
			var posts atomic.Int32
			setupFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/contracts/"+contractID+"/signature-requests" {
					http.NotFound(w, r)
					return
				}
				posts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", `"1"`)
				w.WriteHeader(http.StatusCreated)
				v := store.SignatureRequest{RequestID: requestID, ContractID: contractID, State: "sent", Revision: 1, EndpointOrigin: "https://sign.example.test", TemplateName: "Example", Signers: []store.SignatureSigner{{Position: 0, Role: "Signer", Email: "signer@example.com", State: "awaiting"}}}
				assert.NoError(t, json.MarshalWrite(w, v))
			})
			writeErr := errors.New("synthetic output failure")
			cmd := newSignatureRequestCommand()
			cmd.SetOut(redactionFailedOutput{writeErr})
			cmd.SetErr(io.Discard)
			args := []string{contractID, "--revision", "1", "--template", "7", "--signer", "Signer=signer@example.com"}
			if asJSON {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(t.Context())
			require.ErrorIs(t, err, writeErr)
			require.EqualValues(t, 1, posts.Load())
			require.ErrorContains(t, err, requestID)
			require.ErrorContains(t, err, "confirmed")
			require.ErrorContains(t, err, "sent")
		})
	}
}

// A full history page must point to the next offset without changing JSON output.
func TestSignatureHistoryContinuation(t *testing.T) {
	const contractID = "11111111-1111-4111-8111-111111111111"
	const requestID = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name                 string
		limit, offset, count int
		asJSON, wantHint     bool
	}{
		{"default", 50, 0, 50, false, true}, {"offset", 2, 7, 2, false, true}, {"short", 2, 0, 1, false, false}, {"json", 2, 7, 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/contracts/"+contractID+"/signature-requests" {
					http.NotFound(w, r)
					return
				}
				assert.Equal(t, strconv.Itoa(tc.limit), r.URL.Query().Get("limit"))
				assert.Equal(t, strconv.Itoa(tc.offset), r.URL.Query().Get("offset"))
				items := make([]store.SignatureRequestSummary, tc.count)
				for i := range items {
					items[i] = store.SignatureRequestSummary{RequestID: requestID, ContractID: contractID, Revision: 1, State: "failed", TemplateName: "Example"}
				}
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.MarshalWrite(w, api.SignatureRequestPage{Items: items, Limit: tc.limit, Offset: tc.offset}))
			})
			cmd := newSignatureStatusCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			args := []string{contractID}
			if tc.name != "default" {
				args = append(args, "--limit", strconv.Itoa(tc.limit), "--offset", strconv.Itoa(tc.offset))
			}
			if tc.asJSON {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			if tc.wantHint {
				require.Contains(t, out.String(), "more signature requests may exist")
				require.Contains(t, out.String(), "--offset "+strconv.Itoa(tc.offset+tc.count))
			} else {
				require.NotContains(t, out.String(), "may exist")
				require.NotContains(t, out.String(), "--offset")
			}
			if tc.asJSON {
				var page api.SignatureRequestPage
				require.NoError(t, json.Unmarshal(out.Bytes(), &page))
				require.Len(t, page.Items, tc.count)
			}
		})
	}
}
