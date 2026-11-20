package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-pdf/fpdf"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/document/okf"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/exporter"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/kit/pack"
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
	// Independently render the selected retained artifact for hash and race assertions.
	expectedOutput := func(ctx context.Context, req bundle.PlanRequest, origin string) []byte {
		input, e := s.ExportOKFInput(ctx, "owner", req)
		require.NoError(t, e)
		stream, _, e := s.Blobs.OpenStreamContext(ctx, input.ArtifactSHA256)
		require.NoError(t, e)
		artifact, e := io.ReadAll(stream)
		require.NoError(t, e)
		require.NoError(t, stream.Close())
		front, body, e := document.ParseRenditionFrontMatterV1(artifact)
		require.NoError(t, e)
		title := input.Name
		if front.Document.Title != "" {
			title = front.Document.Title
		}
		header := okf.Header{Title: title, SourceTitle: input.Name, Resource: origin + "/api/v1/versions/" + input.Member.VersionID + "/content"}
		for _, tag := range input.Tags {
			header.Tags = append(header.Tags, tag.Name)
		}
		if req.OKF.IncludeSourceSHA256 {
			header.SourceSHA256 = input.Member.SHA256
		}
		header.Generated = input.Generated
		result, e := okf.Render(header, body)
		require.NoError(t, e)
		return result
	}
	assertNoStage := func() {
		t.Helper()
		require.NoError(t, filepath.WalkDir(filepath.Dir(s.BlobsDir), func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			require.False(t, strings.HasPrefix(d.Name(), ".export-okf-"), path)
			return nil
		}))
	}
	passthrough := func(_ context.Context, fn func() error) error { return fn() }
	before := files()
	injected := errors.New("synthetic publish failure")
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://failure.example.invalid", func(context.Context, func() error) error { return injected }, passthrough)
	require.ErrorIs(t, err, injected)
	require.Equal(t, before, files())
	assertNoStage()
	interrupted, stop := context.WithCancel(t.Context())
	_, err = processing.PrepareOKFExportPlan(interrupted, s.Store, s.Blobs, "owner", request, "https://failure.example.invalid", func(_ context.Context, fn func() error) error { stop(); return fn() }, passthrough)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, files())
	assertNoStage()
	syncFailure := errors.New("synthetic shard sync failure")
	originalSync := pack.SyncDir
	writeFail := func(_ context.Context, fn func() error) error {
		assertNoStage()
		output := expectedOutput(t.Context(), request, "https://failed-sync.example.invalid")
		digest := sha256.Sum256(output)
		hash := hex.EncodeToString(digest[:])
		shard := filepath.Join(s.BlobsDir, hash[:2])
		require.NoError(t, os.MkdirAll(shard, 0700))
		pack.SyncDir = func(path string) error {
			if path == shard {
				return syncFailure
			}
			return originalSync(path)
		}
		defer func() { pack.SyncDir = originalSync }()
		return fn()
	}
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://failed-sync.example.invalid", writeFail, passthrough)
	require.ErrorIs(t, err, syncFailure)
	require.Equal(t, before, files(), "failed durable write must not leave a new loose blob")
	assertNoStage()

	// A competing seal wins after admission, with bytes distinct from this candidate.
	for _, failCleanup := range []bool{false, true} {
		candidateRequest := request
		candidateRequest.OperationID = uuid.New().String()
		var winner bundle.Plan
		var candidateHash, winnerHash string
		cleanupFailure := errors.New("synthetic cleanup failure")
		publishWinner := func(ctx context.Context, fn func() error) error {
			candidate := expectedOutput(ctx, candidateRequest, "https://candidate.example.invalid")
			digest := sha256.Sum256(candidate)
			candidateHash = hex.EncodeToString(digest[:])
			winnerBytes := bytes.Replace(candidate, []byte("candidate.example.invalid"), []byte("winner.example.invalid"), 1)
			require.NotEqual(t, candidate, winnerBytes)
			written, e := s.Blobs.WriteDetailedContext(ctx, bytes.NewReader(winnerBytes))
			require.NoError(t, e)
			winnerHash = written.Hash
			encoding, e := written.EncodingName()
			require.NoError(t, e)
			input, e := s.ExportOKFInput(ctx, "owner", candidateRequest)
			require.NoError(t, e)
			tags := make([]string, 0, len(input.Tags))
			for _, tag := range input.Tags {
				tags = append(tags, tag.Name)
			}
			receipt := bundle.OKFReceipt{Version: 1, Source: input.Member, NodeRevision: input.Member.Revision, Option: *candidateRequest.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: "https://winner.example.invalid/api/v1/versions/" + input.Member.VersionID + "/content", Tags: tags, Generated: input.Generated}
			prepared := store.PreparedOKFExport{Input: input, Receipt: receipt, SHA256: written.Hash, Size: written.Size, Physical: store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created}}
			var own bool
			winner, own, e = s.SealOKFExportPlan(ctx, "owner", candidateRequest, prepared)
			require.NoError(t, e)
			require.True(t, own)
			return fn()
		}
		maintain := passthrough
		if failCleanup {
			maintain = func(context.Context, func() error) error { return cleanupFailure }
		}
		result, e := processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", candidateRequest, "https://candidate.example.invalid", publishWinner, maintain)
		require.Equal(t, winner.ID, result.ID)
		if failCleanup {
			require.ErrorIs(t, e, cleanupFailure)
			require.NoError(t, s.Blobs.Remove(candidateHash))
		} else {
			require.NoError(t, e)
			_, _, e = s.Blobs.OpenStreamContext(t.Context(), candidateHash)
			require.Error(t, e)
		}
		stream, _, e := s.Blobs.OpenStreamContext(t.Context(), winnerHash)
		require.NoError(t, e)
		_, e = io.Copy(io.Discard, stream)
		require.NoError(t, e)
		require.NoError(t, stream.Close())
	}
	replayRequest := request
	replayRequest.OperationID = uuid.New().String()
	entered, release := make(chan struct{}), make(chan struct{})
	type outcome struct {
		plan bundle.Plan
		err  error
	}
	outcomes := make(chan outcome, 2)
	var publications atomic.Int32
	firstPublish := func(_ context.Context, fn func() error) error {
		publications.Add(1)
		close(entered)
		<-release
		return fn()
	}
	secondPublish := func(_ context.Context, fn func() error) error { publications.Add(1); return fn() }
	go func() {
		p, e := processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", replayRequest, "https://replay.example.invalid", firstPublish, passthrough)
		outcomes <- outcome{p, e}
	}()
	<-entered
	started := make(chan struct{})
	go func() {
		close(started)
		p, e := processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", replayRequest, "https://replay.example.invalid", secondPublish, passthrough)
		outcomes <- outcome{p, e}
	}()
	<-started
	close(release)
	first, second := <-outcomes, <-outcomes
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, first.plan, second.plan)
	require.EqualValues(t, 1, publications.Load())

	// Inject cancellation precisely after the loose rename and before shard sync.
	cancelCtx, cancelAfterRename := context.WithCancel(t.Context())
	beforeCancel := files()
	cancelPublish := func(_ context.Context, fn func() error) error {
		output := expectedOutput(t.Context(), request, "https://cancel-after-rename.example.invalid")
		sum := sha256.Sum256(output)
		h := hex.EncodeToString(sum[:])
		shard := filepath.Join(s.BlobsDir, h[:2])
		require.NoError(t, os.MkdirAll(shard, 0700))
		pack.SyncDir = func(path string) error {
			if path == shard {
				cancelAfterRename()
				return context.Canceled
			}
			return originalSync(path)
		}
		defer func() { pack.SyncDir = originalSync }()
		return fn()
	}
	_, err = processing.PrepareOKFExportPlan(cancelCtx, s.Store, s.Blobs, "owner", request, "https://cancel-after-rename.example.invalid", cancelPublish, passthrough)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, beforeCancel, files())
	assertNoStage()
	existingRequest := request
	existingRequest.OperationID = uuid.New().String()
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", existingRequest, "https://existing.example.invalid", passthrough, passthrough)
	require.NoError(t, err)
	existing := files()
	retry := request
	retry.OperationID = uuid.New().String()
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", retry, "https://existing.example.invalid", func(_ context.Context, fn func() error) error {
		if e := fn(); e != nil {
			return e
		}
		return injected
	}, passthrough)
	require.NoError(t, err, "a committed seal survives a later publish-wrapper error")
	require.Equal(t, existing, files(), "a previously rooted identical blob must survive")
	before = files()
	publish := func(ctx context.Context, fn func() error) error {
		_, _, e := s.Move(ctx, node.ID, s.RootID(), "renamed.pdf", node.Revision)
		if e != nil {
			return e
		}
		return fn()
	}
	maintain := func(_ context.Context, fn func() error) error { return fn() }
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://archive.example.invalid", publish, maintain)
	require.ErrorIs(t, err, bundle.ErrConflict)
	require.Equal(t, before, files())
	assertNoStage()
	current, err := s.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	member.Revision = current.Revision
	rootSource, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	rootRequest := request
	rootRequest.OperationID, rootRequest.SourceID, rootRequest.MemberHash = uuid.New().String(), rootSource.ID, rootSource.MemberHash
	var rootedHash string
	competingPublish := func(ctx context.Context, fn func() error) error {
		output := expectedOutput(ctx, rootRequest, "https://concurrent.example.invalid")
		_, _, e := s.Move(ctx, current.ID, s.RootID(), "renamed-again.pdf", current.Revision)
		require.NoError(t, e)
		sealErr := fn()
		require.ErrorIs(t, sealErr, bundle.ErrConflict)
		require.NoError(t, s.Blobs.WithMutation(ctx, func() error {
			written, writeErr := s.Blobs.WriteDetailedContext(ctx, bytes.NewReader(output))
			if writeErr != nil {
				return writeErr
			}
			rootedHash = written.Hash
			encoding, encodingErr := written.EncodingName()
			if encodingErr != nil {
				return encodingErr
			}
			_, e = s.CreateFile(ctx, s.RootID(), "concurrent-original.md", written.Hash, written.Size, "text/markdown", store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created})
			return e
		}))
		return sealErr
	}
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", rootRequest, "https://concurrent.example.invalid", competingPublish, passthrough)
	require.ErrorIs(t, err, bundle.ErrConflict)
	require.NotEmpty(t, rootedHash)
	rooted, _, err := s.Blobs.OpenStreamContext(t.Context(), rootedHash)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, rooted)
	require.NoError(t, err)
	require.NoError(t, rooted.Close())
	current, err = s.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	replacementHash, replacementSize, err := s.Blobs.WriteContext(t.Context(), strings.NewReader("synthetic replacement"))
	require.NoError(t, err)
	current, _, err = s.ReplaceContent(t.Context(), current.ID, current.Revision, replacementHash, replacementSize, "application/pdf")
	require.NoError(t, err)
	member.Revision = current.Revision
	oldSource, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	pruneRequest := request
	pruneRequest.OperationID, pruneRequest.SourceID, pruneRequest.MemberHash = uuid.New().String(), oldSource.ID, oldSource.MemberHash
	attempted := false
	prune := func(ctx context.Context, fn func() error) error {
		_, e := s.PruneContentVersions(ctx, node.ID, current.Revision, store.VersionPruneSelector{VersionIDs: []string{node.CurrentVersionID}}, true)
		require.ErrorIs(t, e, bundle.ErrRetained)
		attempted = true
		return fn()
	}
	retainedPlan, err := processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", pruneRequest, "https://prune.example.invalid", prune, passthrough)
	require.NoError(t, err)
	require.True(t, attempted)
	retained, err := s.ContentVersionByID(t.Context(), node.CurrentVersionID)
	require.NoError(t, err)
	require.Equal(t, hash, retained.BlobHash)
	var docs []bundle.Document
	require.NoError(t, s.WalkExportDocuments(t.Context(), retainedPlan.ID, func(d bundle.Document) error { docs = append(docs, d); return nil }))
	require.Len(t, docs, 1)
	require.Equal(t, member, docs[0].Member)
	assertNoStage()
	// A deduplicated, unreferenced blob belongs to an earlier writer and must survive.
	unrootedRequest := pruneRequest
	unrootedRequest.OperationID = uuid.New().String()
	unrootedBytes := expectedOutput(t.Context(), unrootedRequest, "https://unrooted.example.invalid")
	unrootedHash, _, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader(unrootedBytes))
	require.NoError(t, err)
	unreferenced, err := s.OKFBlobUnreferenced(t.Context(), unrootedHash)
	require.NoError(t, err)
	require.True(t, unreferenced)
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", unrootedRequest, "https://unrooted.example.invalid", func(ctx context.Context, fn func() error) error {
		_, _, e := s.Move(ctx, current.ID, s.RootID(), "changed-final.pdf", current.Revision)
		require.NoError(t, e)
		return fn()
	}, passthrough)
	require.ErrorIs(t, err, bundle.ErrConflict)
	// Catalog-backed reads reject unreferenced blobs; a repeated verified write
	// proves the physical object remains and was deduplicated.
	retainedWrite, err := s.Blobs.WriteDetailedContext(t.Context(), bytes.NewReader(unrootedBytes))
	require.NoError(t, err)
	require.Equal(t, unrootedHash, retainedWrite.Hash)
	require.False(t, retainedWrite.Created)
	assertNoStage()
}

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

func TestOKFRejectsLegacyUnenvelopedRendition(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	ts, s := newTestServer(t, func(d *api.Deps) {
		renditionTextConfig(d)
		cfg = d.Cfg
		d.Cfg.Web.PublicOrigin = "https://archive.example.invalid"
	})
	node := createFileWithContent(t, ts, s, "/legacy.pdf", "%PDF-1.4 synthetic")
	fixture := publishRenditionTextSourceFixture(t, s, cfg, node, "Legacy text")
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: fixture.profile.Fingerprint, AttachmentID: fixture.attachment, BuildID: fixture.build}}
	gate := api.NewOperationGate()
	_, err = processing.PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
	require.ErrorIs(t, err, bundle.ErrUnavailable)
	require.ErrorContains(t, err, "reprocess")
}
