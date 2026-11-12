package mcp

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/store"
)

func TestContractMCPImportedRevisionPrecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision int64
		action   string
	}{
		{"boundary_update", 9007199254740991, "update"},
		{"high_update", 9007199254740993, "update"},
		{"high_link", 9007199254740993, "link"},
		{"max_exhaustion", math.MaxInt64, "exhaust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			source, err := store.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, source.Close()) })
			original, err := source.CreateContract(ctx, store.ContractInput{Title: "Synthetic agreement"})
			require.NoError(t, err)
			var exported bytes.Buffer
			require.NoError(t, source.ExportMetadata(ctx, &exported))
			lines := strings.Split(exported.String(), "\n")
			found := false
			for i, line := range lines {
				if strings.Contains(line, `"type":"contract"`) && strings.Contains(line, original.ContractID) {
					lines[i] = strings.Replace(line, `"revision":1`, `"revision":`+strconv.FormatInt(tc.revision, 10), 1)
					require.NotEqual(t, line, lines[i])
					found = true
				}
			}
			require.True(t, found)
			vault := t.TempDir()
			catalog, err := store.Open(ctx, filepath.Join(vault, "docbank.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, catalog.Close()) })
			require.NoError(t, catalog.ImportMetadata(ctx, strings.NewReader(strings.Join(lines, "\n"))))
			node, err := catalog.CreateFile(ctx, catalog.RootID(), "agreement.txt", strings.Repeat("a", 64), 12, "text/plain")
			require.NoError(t, err)
			blobs, err := blob.New(store.NewPackCatalog(catalog), filepath.Join(vault, "blobs"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, blobs.Close()) })
			cfg := config.Default()
			cfg.Server.APIKey = "synthetic-key"
			daemon := api.NewServer(api.Deps{Store: catalog, Blobs: blobs, VaultRoot: vault, Cfg: cfg})
			t.Cleanup(daemon.Close)
			var match string
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-Match") != "" {
					match = r.Header.Get("If-Match")
				}
				daemon.Handler().ServeHTTP(w, r)
			}))
			t.Cleanup(httpServer.Close)
			conn := daemonconn.New(httpServer.URL, cfg.Server.APIKey)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			lease := newDaemonLeaseWith(func(context.Context) (*daemonconn.Connection, error) { return conn, nil }, func(*daemonconn.Connection) error { return nil })
			server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{AllowContractWrites: true}, lease)
			call := func(name string, args map[string]any) []byte {
				t.Helper()
				return exchangeRaw(t, server, requestFor("tools/call", map[string]any{"name": name, "arguments": args}))
			}
			// Each subtest continues after a read failure so hosted logs expose every boundary.
			t.Run("list", func(t *testing.T) {
				assertPrecisionWire(t, call("list_contracts", map[string]any{}), tc.revision, true)
			})
			t.Run("detail", func(t *testing.T) {
				assertPrecisionWire(t, call("get_contract", map[string]any{"contract_id": original.ContractID}), tc.revision, false)
			})
			t.Run("mutation", func(t *testing.T) {
				args := map[string]any{"contract_id": original.ContractID, "revision": tc.revision}
				name := "update_contract"
				if tc.action == "link" {
					name = "link_contract_document"
					args["node_id"] = node.ID
					args["content_version_id"] = node.CurrentVersionID
					args["role"] = "signed"
				} else {
					args["patch"] = map[string]any{"title": "Changed"}
				}
				wire := call(name, args)
				require.Equal(t, fmt.Sprintf("\"%d\"", tc.revision), match)
				if tc.action == "exhaust" {
					var result struct {
						Result struct {
							IsError bool `json:"isError"`
						} `json:"result"`
					}
					require.NoError(t, json.Unmarshal(wire, &result))
					require.True(t, result.Result.IsError, "%s", wire)
					after, err := catalog.GetContract(ctx, original.ContractID, "2026-11-12")
					require.NoError(t, err)
					require.Equal(t, tc.revision, after.Revision)
					require.Equal(t, "Synthetic agreement", after.Title)
					return
				}
				assertPrecisionWire(t, wire, tc.revision+1, false)
				assertPrecisionWire(t, call("get_contract", map[string]any{"contract_id": original.ContractID}), tc.revision+1, false)
			})
		})
	}
}

func assertPrecisionWire(t *testing.T, wire []byte, want int64, list bool) {
	t.Helper()
	type payload struct {
		Revision int64 `json:"revision"`
		Items    []struct {
			Revision int64 `json:"revision"`
		} `json:"items"`
	}
	var response struct {
		Result struct {
			IsError    bool    `json:"isError"`
			Structured payload `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(wire, &response))
	require.False(t, response.Result.IsError, "%s", wire)
	require.NotEmpty(t, response.Result.Content, "%s", wire)
	var text payload
	require.NoError(t, json.Unmarshal([]byte(response.Result.Content[0].Text), &text))
	if list {
		require.Len(t, response.Result.Structured.Items, 1)
		require.Len(t, text.Items, 1)
		require.Equal(t, want, response.Result.Structured.Items[0].Revision)
		require.Equal(t, want, text.Items[0].Revision)
	} else {
		require.Equal(t, want, response.Result.Structured.Revision)
		require.Equal(t, want, text.Revision)
	}
}

func TestBoundedToolSuccessExactIntegerSerialization(t *testing.T) {
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"revision": {Type: "integer"}}, Required: []string{"revision"}}
	output := struct {
		Revision int64 `json:"revision"`
	}{9007199254740993}
	result, err := boundedToolSuccess(mustResolveSchema(schema), output, nil)
	require.NoError(t, err)
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	var decoded struct {
		Structured struct {
			Revision int64 `json:"revision"`
		} `json:"structuredContent"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(wire, &decoded))
	require.Equal(t, output.Revision, decoded.Structured.Revision)
	require.NotEmpty(t, decoded.Content)
	var text struct {
		Revision int64 `json:"revision"`
	}
	require.NoError(t, json.Unmarshal([]byte(decoded.Content[0].Text), &text))
	require.Equal(t, output.Revision, text.Revision)
	_, err = boundedToolSuccess(mustResolveSchema(&jsonschema.Schema{Type: "string"}), output, nil)
	require.Error(t, err)
	_, err = boundedToolSuccess(mustResolveSchema(&jsonschema.Schema{Type: "string"}), strings.Repeat("x", maxToolResponseBytes), nil)
	require.ErrorIs(t, err, errToolResultTooLarge)
}

func TestBoundedToolSuccessPreservesEmptyCollections(t *testing.T) {
	output := struct {
		Items  []string          `json:"items"`
		Labels map[string]string `json:"labels"`
	}{}
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{
		"items": {Type: "array"}, "labels": {Type: "object"},
	}, Required: []string{"items", "labels"}}
	result, err := boundedToolSuccess(mustResolveSchema(schema), output, nil)
	require.NoError(t, err)
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	var decoded struct {
		Structured struct {
			Items  []string          `json:"items"`
			Labels map[string]string `json:"labels"`
		} `json:"structuredContent"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(wire, &decoded))
	require.NotNil(t, decoded.Structured.Items)
	require.NotNil(t, decoded.Structured.Labels)
	require.NotEmpty(t, decoded.Content)
	require.JSONEq(t, `{"items":[],"labels":{}}`, decoded.Content[0].Text)
}
