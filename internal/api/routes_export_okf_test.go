package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-pdf/fpdf"
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
	t.Parallel()
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
		d.Cfg.Web.BasePath = "/archive"
		d.Gate = api.NewOperationGate()
		var err error
		worker, err = exporter.New(d.Store, d.Blobs, d.VaultRoot, d.Gate)
		require.NoError(t, err)
		d.Exports = worker
	})
	syntheticPDF := fpdf.New("P", "pt", "Letter", "")
	syntheticPDF.SetFont("Helvetica", "", 12)
	syntheticPDF.AddPage()
	syntheticPDF.Text(40, 60, "Synthetic caf\xe9 citation")
	var pdfBuffer bytes.Buffer
	require.NoError(t, syntheticPDF.Output(&pdfBuffer))
	pdf := pdfBuffer.Bytes()
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
	buildBefore, err := s.RenditionBuild(t.Context(), observed.Build)
	require.NoError(t, err)
	var nativeHash string
	for _, a := range buildBefore.Artifacts {
		if a.Role == "sanitized_markdown" {
			nativeHash = a.BlobHash
		}
	}
	require.NotEmpty(t, nativeHash)
	nativeReader, _, err := s.Blobs.OpenStreamContext(t.Context(), nativeHash)
	require.NoError(t, err)
	nativeBefore, err := io.ReadAll(nativeReader)
	require.NoError(t, err)
	require.NoError(t, nativeReader.Close())
	prefix, _, foundCitation := strings.Cut(string(nativeBefore), "é")
	require.True(t, foundCitation, "synthetic rendition must contain multibyte text")
	vaultID, err := uuid.Parse(s.VaultID())
	require.NoError(t, err)
	versionID, err := uuid.Parse(node.CurrentVersionID)
	require.NoError(t, err)
	start := utf8.RuneCountInString(prefix)
	citation := document.TextCitation{Version: 1, VaultUID: vaultID, NodeID: node.ID, ContentVersionID: versionID, ContentSHA256: hash, RenditionAttachmentID: observed.Attachment, BuildID: observed.Build, RenditionSHA256: nativeHash, Start: start, End: start + 1}
	citationResponse, citationBefore := do(t, ts, http.MethodPost, citationPath, nil, citation)
	require.Equal(t, http.StatusOK, citationResponse.StatusCode, citationBefore)
	var resolved document.ResolvedTextCitation
	require.NoError(t, json.Unmarshal([]byte(citationBefore), &resolved))
	require.Equal(t, "é", resolved.Text)
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	response, body = do(t, ts, http.MethodPost, "/api/v1/exports/sources", nil, bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}})
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var source bundle.Source
	require.NoError(t, json.Unmarshal([]byte(body), &source))
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: observed.Profile.Fingerprint, AttachmentID: observed.Attachment, BuildID: observed.Build}}
	response, body = do(t, ts, http.MethodPost, "/archive/api/v1/exports/plans", map[string]string{"Forwarded": "host=attacker.invalid", "X-Forwarded-Host": "attacker.invalid"}, request)
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
	var frozen bundle.OKFReceipt
	require.NoError(t, json.Unmarshal(docs[0].Roles[0].Recipe, &frozen))
	require.NotNil(t, frozen.Generated)
	require.Equal(t, "process:"+document.PDFTextProviderID, frozen.Generated.By)
	generatedAt, err := time.Parse(time.RFC3339, frozen.Generated.At)
	require.NoError(t, err)
	completedAt, err := time.Parse(time.RFC3339, build.CompletedAt)
	require.NoError(t, err)
	require.True(t, generatedAt.Equal(completedAt))
	require.NotContains(t, string(data), "x-docbank-sha256")
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
	require.Equal(t, nativeBefore, native)
	citationResponse, citationAfter := do(t, ts, http.MethodPost, citationPath, nil, citation)
	require.Equal(t, http.StatusOK, citationResponse.StatusCode, citationAfter)
	require.JSONEq(t, citationBefore, citationAfter)

	require.Contains(t, string(data), "https://archive.example.invalid/archive/api/v1/versions/"+node.CurrentVersionID+"/content")
	disclosed := request
	disclosed.OperationID = uuid.New().String()
	copyOption := *request.OKF
	copyOption.IncludeSourceSHA256 = true
	disclosed.OKF = &copyOption
	response, body = do(t, ts, http.MethodPost, "/api/v1/exports/plans", nil, disclosed)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var disclosedPlan bundle.Plan
	require.NoError(t, json.Unmarshal([]byte(body), &disclosedPlan))
	require.NoError(t, s.WalkExportDocuments(t.Context(), disclosedPlan.ID, func(d bundle.Document) error {
		r, _, e := s.Blobs.OpenStreamContext(t.Context(), d.Roles[0].SHA256)
		require.NoError(t, e)
		content, e := io.ReadAll(r)
		require.NoError(t, e)
		require.NoError(t, r.Close())
		require.Contains(t, string(content), "x-docbank-sha256: \""+hash+"\"")
		return nil
	}))

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
	updated, _, err := s.ReplaceContent(t.Context(), node.ID, node.Revision, newHash, newSize, "application/pdf")
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
	gate := api.NewOperationGate()
	request.OperationID = uuid.New().String()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = processing.PrepareOKFExportPlan(canceled, s.Store, s.Blobs, "master", request, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
	require.ErrorIs(t, err, context.Canceled)
	member.Revision = updated.Revision
	freshSource, err := s.CreateExportSource(t.Context(), "master", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request.SourceID, request.MemberHash = freshSource.ID, freshSource.MemberHash
	require.NoError(t, s.Blobs.Remove(hash))
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "master", request, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
	require.ErrorIs(t, err, bundle.ErrUnavailable)
}

//nolint:paralleltest // PDFium uses process-wide resources.
//nolint:paralleltest // PDFium uses process-wide resources.
func TestOKFBlankPDF(t *testing.T) {
	var runner *processing.PDFTextRunner
	ts, s := newTestServer(t, func(d *api.Deps) {
		runner = configureAutomaticPDFText(t, d)
		d.Cfg.Web.PublicOrigin = "https://archive.example.invalid"
	})
	pdf, err := os.ReadFile("../../document/testdata/scanassessment/blank.pdf")
	require.NoError(t, err)
	hash, size, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader(pdf))
	require.NoError(t, err)
	node, err := s.CreateFile(t.Context(), s.RootID(), "blank.pdf", hash, size, "application/pdf")
	require.NoError(t, err)
	selector := processing.Selector{NodeID: node.ID, ContentVersionID: node.CurrentVersionID, Profile: processing.PDFTextProfileName}
	p, err := runner.Service.Plan(t.Context(), selector)
	require.NoError(t, err)
	_, err = runner.Service.Start(t.Context(), processing.StartRequest{Selector: selector, PlanFingerprint: p.Fingerprint})
	require.NoError(t, err)
	resp, body := do(t, ts, http.MethodPost, "/api/v1/renditions/text", nil, map[string]any{"node_id": node.ID, "revision": node.Revision, "version_id": node.CurrentVersionID, "blob_hash": node.BlobHash, "size": node.Size, "profile": processing.PDFTextProfileName})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var observed struct {
		State   string `json:"state"`
		Profile struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"profile"`
		Attachment string `json:"attachment_id"`
		Build      string `json:"build_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &observed))
	require.Equal(t, "verified_empty", observed.State)
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: observed.Profile.Fingerprint, AttachmentID: observed.Attachment, BuildID: observed.Build}}
	gate := api.NewOperationGate()
	plan, err := processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
	require.NoError(t, err)
	require.NoError(t, s.WalkExportDocuments(t.Context(), plan.ID, func(d bundle.Document) error {
		stream, _, e := s.Blobs.OpenStreamContext(t.Context(), d.Roles[0].SHA256)
		require.NoError(t, e)
		data, e := io.ReadAll(stream)
		require.NoError(t, e)
		require.NoError(t, stream.Close())
		end := bytes.Index(data[4:], []byte("\n---\n"))
		require.GreaterOrEqual(t, end, 0)
		require.Empty(t, data[end+9:])
		return nil
	}))
}
