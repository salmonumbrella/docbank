package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/exporter"
	"go.kenn.io/docbank/internal/processing"
	"uuid"
)

func TestOKFOriginRequired(t *testing.T) {
	ts, s := newTestServer(t, nil)
	hash, size, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader([]byte("synthetic PDF")))
	require.NoError(t, err)
	node, err := s.CreateFile(t.Context(), s.RootID(), "synthetic.pdf", hash, size, "application/pdf")
	require.NoError(t, err)
	m := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	response, body := do(t, ts, http.MethodPost, "/api/v1/exports/sources", nil, bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{m}})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var source bundle.Source
	require.NoError(t, json.Unmarshal([]byte(body), &source))
	fence := strings.Repeat("a", 64)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: fence, AttachmentID: fence, BuildID: fence}}
	response, body = do(t, ts, http.MethodPost, "/api/v1/exports/plans", nil, request)
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, body)
	require.Contains(t, body, "okf_origin_unavailable")
}

//nolint:paralleltest // PDFium uses process-wide resources.
func TestOKFExportSyntheticPDF(t *testing.T) {
	var runner *processing.PDFTextRunner
	var worker *exporter.Worker
	ts, s := newTestServer(t, func(d *api.Deps) {
		runner = configureAutomaticPDFText(t, d)
		d.Cfg.Web.PublicOrigin = "https://archive.example.invalid"
		d.Gate = api.NewOperationGate()
		var err error
		worker, err = exporter.New(d.Store, d.Blobs, d.VaultRoot, d.Gate)
		require.NoError(t, err)
		d.Exports = worker
	})
	pdf, err := os.ReadFile("../../document/testdata/scanassessment/mixed-below-ratio.pdf")
	require.NoError(t, err)
	hash, size, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader(pdf))
	require.NoError(t, err)
	node, err := s.CreateFile(t.Context(), s.RootID(), "synthetic.pdf", hash, size, "application/pdf")
	require.NoError(t, err)
	selector := processing.Selector{NodeID: node.ID, ContentVersionID: node.CurrentVersionID, Profile: processing.PDFTextProfileName}
	processingPlan, err := runner.Service.Plan(t.Context(), selector)
	require.NoError(t, err)
	_, err = runner.Service.Start(t.Context(), processing.StartRequest{Selector: selector, PlanFingerprint: processingPlan.Fingerprint})
	require.NoError(t, err)
	response, body := do(t, ts, http.MethodPost, "/api/v1/renditions/text", nil, map[string]any{"node_id": node.ID, "revision": node.Revision, "version_id": node.CurrentVersionID, "blob_hash": node.BlobHash, "size": node.Size, "profile": processing.PDFTextProfileName})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var observed struct {
		Profile struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"profile"`
		Attachment string `json:"attachment_id"`
		Build      string `json:"build_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &observed))
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	response, body = do(t, ts, http.MethodPost, "/api/v1/exports/sources", nil, bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var source bundle.Source
	require.NoError(t, json.Unmarshal([]byte(body), &source))
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: observed.Profile.Fingerprint, AttachmentID: observed.Attachment, BuildID: observed.Build}}
	response, body = do(t, ts, http.MethodPost, "/api/v1/exports/plans", nil, request)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var plan bundle.Plan
	require.NoError(t, json.Unmarshal([]byte(body), &plan))
	var docs []bundle.Document
	require.NoError(t, s.WalkExportDocuments(t.Context(), plan.ID, func(d bundle.Document) error { docs = append(docs, d); return nil }))
	require.Len(t, docs, 1)
	require.Len(t, docs[0].Roles, 1)
	stream, _, err := s.Blobs.OpenStreamContext(t.Context(), docs[0].Roles[0].SHA256)
	require.NoError(t, err)
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Contains(t, string(data), "type: \"Reference\"")
	build, err := s.RenditionBuild(t.Context(), observed.Build)
	require.NoError(t, err)
	var artifact string
	for _, a := range build.Artifacts {
		if a.Role == "sanitized_markdown" {
			artifact = a.BlobHash
		}
	}
	require.NotEmpty(t, artifact)
	originalStream, _, err := s.Blobs.OpenStreamContext(t.Context(), artifact)
	require.NoError(t, err)
	native, err := io.ReadAll(originalStream)
	require.NoError(t, err)
	require.NoError(t, originalStream.Close())
	_, nativeBody, err := document.ParseRenditionFrontMatterV1(native)
	require.NoError(t, err)
	end := bytes.Index(data[4:], []byte("\n---\n"))
	require.GreaterOrEqual(t, end, 0)
	require.Equal(t, nativeBody, data[end+9:])

	require.Contains(t, string(data), "https://archive.example.invalid/api/v1/versions/"+node.CurrentVersionID+"/content")
	client := daemonconn.New(ts.URL, testAPIKey).API()
	job, err := client.CreateExportJob(t.Context(), &apiclient.CreateExportJobRequestOptions{Body: &bundle.JobRequest{OperationID: uuid.New().String(), PlanID: plan.ID, Fingerprint: plan.Fingerprint}})
	require.NoError(t, err)
	processed, err := worker.RunOne(t.Context())
	require.NoError(t, err)
	require.True(t, processed)
	completed, err := client.GetExportJob(t.Context(), &apiclient.GetExportJobRequestOptions{PathParams: &apiclient.GetExportJobPath{ID: job.ID}})
	require.NoError(t, err)
	require.Equal(t, "completed", completed.State, completed.Failure)
	ticket, err := client.DownloadExportArchive(t.Context(), &apiclient.DownloadExportArchiveRequestOptions{PathParams: &apiclient.DownloadExportArchivePath{ID: job.ID}, Body: &bundle.DownloadRequest{Basename: "reference.zip"}})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+ticket.URL, nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	archive, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	_, err = bundle.Verify(t.Context(), bytes.NewReader(archive), int64(len(archive)), plan.Fingerprint)
	require.NoError(t, err)
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	found := false
	for _, f := range z.File {
		if f.Name == bundle.OKFPath(member) {
			r, e := f.Open()
			require.NoError(t, e)
			got, e := io.ReadAll(r)
			require.NoError(t, e)
			require.NoError(t, r.Close())
			require.Equal(t, data, got)
			found = true
		}
	}
	require.True(t, found)
	newHash, newSize, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader([]byte("new synthetic version")))
	require.NoError(t, err)
	_, _, err = s.ReplaceContent(t.Context(), node.ID, node.Revision, newHash, newSize, "application/pdf")
	require.NoError(t, err)
	unauth, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/versions/"+node.CurrentVersionID+"/content", nil)
	require.NoError(t, err)
	denied, err := http.DefaultClient.Do(unauth)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, denied.StatusCode)
	require.NoError(t, denied.Body.Close())
	response, body = get(t, ts, "/api/v1/versions/"+node.CurrentVersionID+"/content", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, pdf, []byte(body))
	require.NotEmpty(t, response.Trailer.Get("Content-Digest"))
	_, err = client.ReleaseExportJob(t.Context(), &apiclient.ReleaseExportJobRequestOptions{PathParams: &apiclient.ReleaseExportJobPath{ID: job.ID}})
	require.NoError(t, err)

}

//nolint:paralleltest // PDFium uses process-wide resources.
func TestOKFFailedSealReclaimsNewBlob(t *testing.T) {
	var runner *processing.PDFTextRunner
	ts, s := newTestServer(t, func(d *api.Deps) { runner = configureAutomaticPDFText(t, d) })
	pdf, err := os.ReadFile("../../document/testdata/scanassessment/mixed-below-ratio.pdf")
	require.NoError(t, err)
	hash, size, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader(pdf))
	require.NoError(t, err)
	node, err := s.CreateFile(t.Context(), s.RootID(), "synthetic.pdf", hash, size, "application/pdf")
	require.NoError(t, err)
	selector := processing.Selector{NodeID: node.ID, ContentVersionID: node.CurrentVersionID, Profile: processing.PDFTextProfileName}
	p, err := runner.Service.Plan(t.Context(), selector)
	require.NoError(t, err)
	_, err = runner.Service.Start(t.Context(), processing.StartRequest{Selector: selector, PlanFingerprint: p.Fingerprint})
	require.NoError(t, err)
	response, body := do(t, ts, http.MethodPost, "/api/v1/renditions/text", nil, map[string]any{"node_id": node.ID, "revision": node.Revision, "version_id": node.CurrentVersionID, "blob_hash": node.BlobHash, "size": node.Size, "profile": processing.PDFTextProfileName})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var observed struct {
		Profile struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"profile"`
		Attachment string `json:"attachment_id"`
		Build      string `json:"build_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &observed))
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: observed.Profile.Fingerprint, AttachmentID: observed.Attachment, BuildID: observed.Build}}
	files := func() []string {
		var out []string
		require.NoError(t, filepath.WalkDir(s.BlobsDir, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				out = append(out, path)
			}
			return nil
		}))
		return out
	}
	before := files()
	spool := t.TempDir()
	publish := func(ctx context.Context, fn func() error) error {
		_, _, e := s.Move(ctx, node.ID, s.RootID(), "renamed.pdf", node.Revision)
		if e != nil {
			return e
		}
		return fn()
	}
	maintain := func(_ context.Context, fn func() error) error { return fn() }
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, spool, "owner", request, "https://archive.example.invalid", publish, maintain)
	require.ErrorIs(t, err, bundle.ErrConflict)
	require.Equal(t, before, files())
	entries, err := os.ReadDir(spool)
	require.NoError(t, err)
	require.Empty(t, entries)
}
