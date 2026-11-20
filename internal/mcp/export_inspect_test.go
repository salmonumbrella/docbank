package mcp

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/query"
	"go.kenn.io/docbank/internal/store"
)

func TestExportInspectionWorkflow(t *testing.T) {
	f := newNativeExportFixture(t)
	var members []bundle.Member
	var nodes []int64
	for i := range 51 {
		member := f.addFile(t, fmt.Sprintf("synthetic-%02d.bin", i), "synthetic original")
		members = append(members, member)
		nodes = append(nodes, member.NodeID)
	}
	source, err := f.connection.API().CreateExportSource(t.Context(),
		&apiclient.CreateExportSourceRequestOptions{Body: &bundle.SourceRequest{
			OperationID: uuid.New().String(), Kind: "nodes", NodeIDs: nodes,
		}})
	require.NoError(t, err)
	plan, err := f.connection.API().CreateExportPlan(t.Context(),
		&apiclient.CreateExportPlanRequestOptions{Body: &bundle.PlanRequest{
			OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash,
			Roles:        []bundle.RolePolicy{{Role: "original"}, {Role: "text", AllowUnavailable: true}},
			VolumeLimits: &bundle.VolumeLimits{RoleBytes: 1024, Roles: 2}, DuplicatePolicy: "preserve",
		}})
	require.NoError(t, err)
	server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, f.lease)
	for _, transport := range []string{"stdio", "http"} {
		listed := decodeResult(t, exportExchange(t, server, transport, "tools/list", nil))
		for _, name := range []string{"get_export_plan", "get_export_problems"} {
			require.Contains(t, listedToolNames(t, listed), name)
			annotations := objectField(t, listedToolsByName(t, listed)[name], "annotations")
			require.Equal(t, true, annotations["readOnlyHint"])
			require.Equal(t, true, annotations["idempotentHint"])
			require.Equal(t, false, annotations["destructiveHint"])
			require.Equal(t, false, annotations["openWorldHint"])
		}
		result := decodeResult(t, exportExchange(t, server, transport, "tools/call", map[string]any{
			"name": "get_export_plan", "arguments": map[string]any{"plan_id": plan.ID},
		}))
		output := objectField(t, result, "structuredContent")
		raw, err := json.Marshal(output["plan"])
		require.NoError(t, err)
		var got bundle.Plan
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, *plan, got)
		require.Equal(t, "private", output["cacheScope"])
		require.EqualValues(t, 0, output["ttlMs"])
	}
	for _, after := range []int{0, 50, 51} {
		args := map[string]any{"plan_id": plan.ID}
		if after != 0 {
			args["after"] = after
		}
		output := exportCall(t, server, "get_export_problems", args)
		raw, err := json.Marshal(output["problems"])
		require.NoError(t, err)
		var page bundle.OutputProblems
		require.NoError(t, json.Unmarshal(raw, &page))
		require.Equal(t, plan.ID, page.PlanID)
		require.Equal(t, plan.Fingerprint, page.Fingerprint)
		require.Equal(t, after, page.After)
		require.Equal(t, 51, page.Total)
		require.Len(t, page.Items, min(50, 51-after))
		if after == 0 {
			require.Equal(t, 50, page.Next)
		} else {
			require.Zero(t, page.Next)
		}
		for i, item := range page.Items {
			require.Equal(t, members[after+i].NodeID, item.NodeID)
			require.Equal(t, members[after+i].VersionID, item.VersionID)
			require.Equal(t, "text", item.Role)
		}
	}
	require.Equal(t, "export_conflict", exportCall(t, server, "get_export_problems",
		map[string]any{"plan_id": plan.ID, "after": 52})["code"])
	require.Equal(t, "not_found", exportCall(t, server, "get_export_plan",
		map[string]any{"plan_id": uuid.New().String()})["code"])
	before := exportCall(t, server, "get_export_problems", map[string]any{"plan_id": plan.ID})
	header := exportCall(t, server, "get_export_plan", map[string]any{"plan_id": plan.ID})
	node, err := f.catalog.NodeByID(t.Context(), members[0].NodeID)
	require.NoError(t, err)
	_, _, err = f.catalog.ReplaceContent(t.Context(), node.ID, node.Revision,
		strings.Repeat("b", 64), 1, "application/octet-stream")
	require.NoError(t, err)
	require.Equal(t, header, exportCall(t, server, "get_export_plan",
		map[string]any{"plan_id": plan.ID}))
	require.Equal(t, before, exportCall(t, server, "get_export_problems",
		map[string]any{"plan_id": plan.ID}))
}

func TestExportInspectionInputs(t *testing.T) {
	var calls atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(daemon.Close)
	server := newServerWithOptionsAndDaemon(
		testImplementation(), ServerOptions{}, exportTestLease(t, daemon.URL),
	)
	for _, name := range []string{"get_export_plan", "get_export_problems"} {
		cases := []map[string]any{{}, {"plan_id": nil}, {"plan_id": "bad"},
			{"plan_id": "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}, {"plan_id": testVersionID, "extra": true}}
		if name == "get_export_problems" {
			for _, after := range []any{nil, "0", -1, 2600001, 0.5} {
				cases = append(cases, map[string]any{"plan_id": testVersionID, "after": after})
			}
		}
		for _, args := range cases {
			raw := exchangeRaw(t, server, requestFor("tools/call", map[string]any{
				"name": name, "arguments": args,
			}))
			require.EqualValues(t, jsonrpc.CodeInvalidParams, decodeWireError(t, raw).Code, name)
		}
	}
	require.Zero(t, calls.Load())
}

func TestExportInspectionQueryPlans(t *testing.T) {
	f := newNativeExportFixture(t)
	f.addFile(t, "synthetic.txt", "synthetic original")
	q, err := query.Parse([]byte(`{}`))
	require.NoError(t, err)
	saved, err := f.catalog.CreateSavedQuery(t.Context(), "All synthetic", "",
		store.SavedQueryKindQuery, []byte(`{}`))
	require.NoError(t, err)
	var snapshotRequest apiclient.CreateWorkspaceQueryBody
	require.NoError(t, json.Unmarshal([]byte(`{"query":{},"page_size":50}`), &snapshotRequest))
	snapshot, err := f.connection.API().CreateWorkspaceQuery(t.Context(),
		&apiclient.CreateWorkspaceQueryRequestOptions{Body: &snapshotRequest})
	require.NoError(t, err)
	server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, f.lease)
	for _, request := range []bundle.SourceRequest{
		{Kind: "query", Query: &q},
		{Kind: "saved_query", SavedQueryID: saved.ID, SavedQueryRevision: saved.Revision},
		{Kind: "snapshot", SnapshotID: snapshot.SnapshotID, MemberHash: snapshot.MemberHash},
	} {
		t.Run(request.Kind, func(t *testing.T) {
			request.OperationID = uuid.New().String()
			source, err := f.connection.API().CreateExportSource(t.Context(),
				&apiclient.CreateExportSourceRequestOptions{Body: &request})
			require.NoError(t, err)
			if request.Kind != "snapshot" {
				require.Regexp(t, `^sha256:[0-9a-f]{64}$`, source.QueryFingerprint)
			}
			plan, err := f.connection.API().CreateExportPlan(t.Context(),
				&apiclient.CreateExportPlanRequestOptions{Body: &bundle.PlanRequest{
					OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash,
					Roles: []bundle.RolePolicy{{Role: "original"}},
				}})
			require.NoError(t, err)
			output := exportCall(t, server, "get_export_plan", map[string]any{"plan_id": plan.ID})
			raw, err := json.Marshal(output["plan"])
			require.NoError(t, err)
			var got bundle.Plan
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, *plan, got)
		})
	}
}

func TestOKFExportPlanSchema(t *testing.T) {
	option := map[string]any{"profile_fingerprint": strings.Repeat("a", 64), "attachment_id": strings.Repeat("b", 64), "build_id": strings.Repeat("c", 64)}
	properties := retainedExportPlanSchema()["properties"].(schema)
	assertSchemaAccepts(t, properties["okf"], option)
	option["extra"] = true
	assertSchemaRejects(t, properties["okf"], option)
	delete(option, "extra")
	delete(option, "build_id")
	assertSchemaRejects(t, properties["okf"], option)
	assertSchemaAccepts(t, exportRoleSchema(), "okf_markdown")
}

//nolint:paralleltest // PDFium uses process-wide resources.
func TestOKFExportPlanInspectionFromAPI(t *testing.T) {
	f := newNativeExportFixture(t)
	gate := api.NewOperationGate()
	name, profile, err := processing.NewPDFTextProfile()
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "spool"), 0700))
	service, err := processing.NewService(t.Context(), processing.ServiceConfig{Catalog: f.catalog, Blobs: f.blobs, Gate: gate, SpoolDirectory: filepath.Join(root, "spool"), Profiles: map[string]processing.ProfileConfig{name: profile}})
	require.NoError(t, err)
	t.Cleanup(service.Stop)
	runner := &processing.PDFTextRunner{Service: service}
	require.NoError(t, runner.Initialize(t.Context()))
	cfg := config.Default()
	cfg.Server.APIKey = "synthetic-export-key"
	cfg.Web.PublicOrigin = "https://archive.example.invalid"
	daemon := api.NewServer(api.Deps{Store: f.catalog, Blobs: f.blobs, VaultRoot: root, Cfg: cfg, Gate: gate, Processing: service, PDFText: runner})
	t.Cleanup(daemon.Close)
	httpServer := httptest.NewServer(daemon.Handler())
	t.Cleanup(httpServer.Close)
	connection := daemonconn.New(httpServer.URL, cfg.Server.APIKey)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	data, err := os.ReadFile("../../document/testdata/scanassessment/mixed-below-ratio.pdf")
	require.NoError(t, err)
	hash, size, err := f.blobs.WriteContext(t.Context(), bytes.NewReader(data))
	require.NoError(t, err)
	node, err := f.catalog.CreateFile(t.Context(), f.catalog.RootID(), "synthetic.pdf", hash, size, "application/pdf")
	require.NoError(t, err)
	selector := processing.Selector{NodeID: node.ID, ContentVersionID: node.CurrentVersionID, Profile: name}
	processingPlan, err := service.Plan(t.Context(), selector)
	require.NoError(t, err)
	_, err = service.Start(t.Context(), processing.StartRequest{Selector: selector, PlanFingerprint: processingPlan.Fingerprint})
	require.NoError(t, err)
	versionID, err := uuid.Parse(node.CurrentVersionID)
	require.NoError(t, err)
	observed, err := connection.API().ResolveRenditionText(t.Context(), &apiclient.ResolveRenditionTextRequestOptions{Body: &apiclient.RenditionTextRequest{NodeID: node.ID, Revision: node.Revision, VersionID: versionID, BlobHash: hash, Size: size, Profile: &name}})
	require.NoError(t, err)
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	source, err := connection.API().CreateExportSource(t.Context(), &apiclient.CreateExportSourceRequestOptions{Body: &bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}})
	require.NoError(t, err)
	plan, err := connection.API().CreateExportPlan(t.Context(), &apiclient.CreateExportPlanRequestOptions{Body: &bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: observed.Profile.Fingerprint, AttachmentID: observed.AttachmentID, BuildID: observed.BuildID}}})
	require.NoError(t, err)
	server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, exportTestLease(t, httpServer.URL))
	output := exportCall(t, server, "get_export_plan", map[string]any{"plan_id": plan.ID})
	raw, err := json.Marshal(output["plan"])
	require.NoError(t, err)
	var got bundle.Plan
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, *plan, got)
	require.Equal(t, "private", output["cacheScope"])
}
