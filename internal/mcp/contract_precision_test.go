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
	t.Run("retained_document", func(t *testing.T) {
		const high int64 = 9007199254740993
		ctx := t.Context()
		source, err := store.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, source.Close()) })
		node, err := source.CreateFile(ctx, source.RootID(), "agreement.txt", strings.Repeat("a", 64), 12, "text/plain")
		require.NoError(t, err)
		contract, err := source.CreateContract(ctx, store.ContractInput{Title: "Synthetic agreement"})
		require.NoError(t, err)
		_, err = source.LinkContractDocument(ctx, contract.ContractID, contract.Revision, node.ID, "signed", node.CurrentVersionID)
		require.NoError(t, err)
		_, _, err = source.Trash(ctx, node.ID, node.Revision)
		require.NoError(t, err)
		_, err = source.TrashEmpty(ctx, 0, true)
		require.NoError(t, err)
		var exported bytes.Buffer
		require.NoError(t, source.ExportMetadata(ctx, &exported))
		lines := strings.Split(exported.String(), "\n")
		oldHeader := lines[0]
		lines[0] = strings.Replace(lines[0], fmt.Sprintf(`"node_sequence":%d`, node.ID), fmt.Sprintf(`"node_sequence":%d`, high), 1)
		require.NotEqual(t, oldHeader, lines[0])
		found := false
		for i, line := range lines {
			if strings.Contains(line, `"type":"contract_document"`) {
				lines[i] = strings.Replace(line, fmt.Sprintf(`"node_id":%d`, node.ID), fmt.Sprintf(`"node_id":%d`, high), 1)
				lines[i] = strings.Replace(lines[i], `"size":12`, fmt.Sprintf(`"size":%d`, high), 1)
				require.Contains(t, lines[i], fmt.Sprintf(`"node_id":%d`, high))
				require.Contains(t, lines[i], fmt.Sprintf(`"size":%d`, high))
				found = true
			}
		}
		require.True(t, found)
		vault := t.TempDir()
		catalog, err := store.Open(ctx, filepath.Join(vault, "docbank.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, catalog.Close()) })
		require.NoError(t, catalog.ImportMetadata(ctx, strings.NewReader(strings.Join(lines, "\n"))))
		blobs, err := blob.New(store.NewPackCatalog(catalog), filepath.Join(vault, "blobs"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, blobs.Close()) })
		cfg := config.Default()
		cfg.Server.APIKey = "synthetic-export-key"
		daemon := api.NewServer(api.Deps{Store: catalog, Blobs: blobs, VaultRoot: vault, Cfg: cfg})
		t.Cleanup(daemon.Close)
		httpServer := httptest.NewServer(daemon.Handler())
		t.Cleanup(httpServer.Close)
		server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, exportTestLease(t, httpServer.URL))
		wire := exchangeRaw(t, server, requestFor("tools/call", map[string]any{"name": "get_contract", "arguments": map[string]any{"contract_id": contract.ContractID}}))
		type document struct {
			NodeID int64 `json:"node_id"`
			Size   int64 `json:"size"`
		}
		type payload struct {
			Documents []document `json:"documents"`
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
		require.Equal(t, []document{{NodeID: high, Size: high}}, response.Result.Structured.Documents)
		require.Equal(t, response.Result.Structured.Documents, text.Documents)
	})
	t.Run("link_high_node", func(t *testing.T) {
		const id = "11111111-1111-4111-8111-111111111111"
		var gotPath string
		daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"2"`)
			_, _ = fmt.Fprintf(w, `{"contract_id":%q,"revision":2}`, id)
		}))
		t.Cleanup(daemon.Close)
		server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{AllowContractWrites: true}, exportTestLease(t, daemon.URL))
		args := map[string]any{"contract_id": id, "revision": 1, "node_id": int64(9007199254740993), "content_version_id": "22222222-2222-4222-8222-222222222222", "role": "signed"}
		request := requestFor("tools/call", map[string]any{"name": "link_contract_document", "arguments": args})
		wire := exchangeRaw(t, server, request)
		require.Equal(t, "/api/v1/contracts/"+id+"/documents/9007199254740993", gotPath, "%s", wire)
		assertPrecisionWire(t, wire, 2, false)
	})
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
