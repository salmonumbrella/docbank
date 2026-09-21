package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
)

const maxBatesRouteLabels = 250

func registerBatesRoutes(mux *http.ServeMux, api huma.API, d Deps, g *gate, downloads *webDownloadRegistry, sessions *webSessionRegistry) {
	huma.Register(api, huma.Operation{OperationID: "createBatesNamespace", Method: http.MethodPost,
		Path: "/api/v1/bates/namespaces", Summary: "Create or find a Bates namespace", DefaultStatus: http.StatusCreated,
		MaxBodyBytes: 4096}, func(ctx context.Context, in *struct{ Body BatesNamespaceRequest }) (*struct{ Body BatesNamespace }, error) {
		var result store.BatesNamespace
		err := g.mutate(func() error {
			var err error
			result, err = d.Store.EnsureBatesNamespace(ctx, in.Body.Prefix, in.Body.Suffix, in.Body.Padding)
			return err
		})
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesNamespace }{Body: batesNamespaceDTO(result)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "listBatesNamespaces", Method: http.MethodGet,
		Path: "/api/v1/bates/namespaces", Summary: "List Bates namespaces"}, func(ctx context.Context, in *struct {
		Cursor string `query:"cursor"`
		Limit  int    `query:"limit" minimum:"0" maximum:"250"`
	}) (*struct{ Body BatesNamespacePage }, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 100
		}
		items, total, next, err := d.Store.BatesNamespaces(ctx, in.Cursor, limit)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := BatesNamespacePage{Items: make([]BatesNamespace, len(items)), Total: total, NextCursor: next}
		for i, item := range items {
			out.Items[i] = batesNamespaceDTO(item)
		}
		return &struct{ Body BatesNamespacePage }{Body: out}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "planBatesStamp", Method: http.MethodPost,
		Path: "/api/v1/bates/preview", Summary: "Preview tentative Bates labels without stamping or reserving",
		MaxBodyBytes: 128 << 10}, func(ctx context.Context, in *struct{ Body BatesPlanRequest }) (*struct{ Body BatesPlan }, error) {
		request, err := bindBatesPlanRequest(ctx, d.Store, in.Body)
		if err != nil {
			return nil, FromStoreError(err)
		}
		plan, err := d.Store.PreviewBatesRange(ctx, request)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesPlan }{Body: BatesPlan{Namespace: batesNamespaceDTO(plan.Namespace),
			StartSequence: plan.StartSequence, EndSequence: plan.EndSequence,
			Labels: batesLabelsDTO(plan.Labels), StampedNothing: true}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "reserveBatesRange", Method: http.MethodPost,
		Path: "/api/v1/bates/allocations", Summary: "Reserve one idempotent Bates range", DefaultStatus: http.StatusCreated,
		MaxBodyBytes: 128 << 10}, func(ctx context.Context, in *struct{ Body BatesPlanRequest }) (*struct{ Body BatesAllocation }, error) {
		if in.Body.RecipeSHA256 == "" {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", "recipe_sha256 is required")
		}
		request, err := bindBatesPlanRequest(ctx, d.Store, in.Body)
		if err != nil {
			return nil, FromStoreError(err)
		}
		var allocation store.BatesAllocation
		err = g.mutate(func() error {
			var err error
			allocation, err = d.Store.ReserveBatesRange(ctx, request)
			return err
		})
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesAllocation }{Body: batesAllocationDTO(allocation)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "readBatesAllocation", Method: http.MethodGet,
		Path: "/api/v1/bates/allocations/{id}", Summary: "Read a Bates allocation"}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body BatesAllocation }, error) {
		allocation, err := d.Store.BatesAllocation(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesAllocation }{Body: batesAllocationDTO(allocation)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "publishBatesExport", Method: http.MethodPost,
		Path: "/api/v1/bates/exports", Summary: "Publish a verified Bates export", DefaultStatus: http.StatusCreated,
		MaxBodyBytes: 16 << 10}, func(ctx context.Context, in *struct{ Body BatesExportRequest }) (*struct{ Body BatesExport }, error) {
		if in.Body.AllocationID == "" {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", "allocation_id is required")
		}
		var artifact store.BatesArtifact
		err := g.MutateContext(ctx, func() error {
			var err error
			artifact, err = processing.PublishBatesExport(ctx, d.Store, d.Blobs, in.Body.AllocationID, in.Body.Recipe)
			return err
		})
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesExport }{Body: batesExportDTO(artifact)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "readBatesExport", Method: http.MethodGet,
		Path: "/api/v1/bates/exports/{id}", Summary: "Read a verified Bates export"}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body BatesExport }, error) {
		artifact, err := d.Store.BatesArtifact(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &struct{ Body BatesExport }{Body: batesExportDTO(artifact)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "listBatesExports", Method: http.MethodGet,
		Path: "/api/v1/bates/exports", Summary: "List verified Bates export history"}, func(ctx context.Context, in *struct {
		After string `query:"after"`
		Limit int    `query:"limit" minimum:"0" maximum:"250"`
	}) (*struct{ Body BatesExportPage }, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 100
		}
		items, err := d.Store.BatesArtifacts(ctx, in.After, limit)
		if err != nil {
			return nil, FromStoreError(err)
		}
		total, err := d.Store.BatesArtifactCount(ctx)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := BatesExportPage{Items: make([]BatesExport, len(items)), Total: total}
		for index, item := range items {
			out.Items[index] = batesExportDTO(item)
		}
		if len(items) == limit {
			out.NextAfter = items[len(items)-1].ArtifactID
		}
		return &struct{ Body BatesExportPage }{Body: out}, nil
	})
	type downloadOutput struct{ Body BatesDownloadTicket }
	huma.Register(api, huma.Operation{OperationID: "downloadBatesExport", Method: http.MethodPost,
		Path: "/api/v1/bates/exports/{id}/download", Summary: "Issue a one-use ticket for a reverified Bates export",
		MaxBodyBytes: 1024}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct{}
	}) (*downloadOutput, error) {
		owner, err := exportOwner(ctx)
		if err != nil {
			return nil, err
		}
		data, artifact, err := processing.ReadBatesExport(ctx, d.Store, d.Blobs, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		file, path, err := downloads.createStagingFile()
		if err != nil {
			return nil, FromStoreError(err)
		}
		keep := false
		defer func() {
			if !keep {
				_ = file.Close()
				_ = os.Remove(path)
			}
		}()
		if _, err = file.Write(data); err != nil {
			return nil, FromStoreError(err)
		}
		if err = file.Sync(); err != nil {
			return nil, FromStoreError(err)
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return nil, FromStoreError(err)
		}
		name := fmt.Sprintf("bates-%s.pdf", artifact.AllocationID)
		ticket := webDownloadTicket{path: path, name: name, mediaType: "application/pdf",
			blobHash: artifact.BlobSHA256, size: artifact.Size, owner: owner, archiveFile: file,
			releaseArchive: func() { _ = file.Close(); _ = os.Remove(path) }}
		var token string
		if browserSessionRequest(ctx) {
			active, issueErr := sessions.withActiveOwner(owner, func() error {
				var issueErr error
				token, issueErr = downloads.issue(ticket)
				return issueErr
			})
			if !active && issueErr == nil {
				issueErr = errors.New("browser session was revoked before Bates download publication")
			}
			err = issueErr
		} else {
			token, err = downloads.issue(ticket)
		}
		if err != nil {
			return nil, FromStoreError(err)
		}
		keep = true
		return &downloadOutput{Body: BatesDownloadTicket{URL: webDownloadFilePath + "?ticket=" + token,
			Name: name, AllocationID: artifact.AllocationID, BlobSHA256: artifact.BlobSHA256, Size: artifact.Size}}, nil
	})
	mux.HandleFunc("GET /api/v1/bates/exports/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		data, artifact, err := processing.ReadBatesExport(r.Context(), d.Store, d.Blobs, r.PathValue("id"))
		if err != nil {
			writeEmailStoreError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="bates-%s.pdf"`, artifact.AllocationID))
		w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
		w.Header().Set(BlobHashHeader, artifact.BlobSHA256)
		w.Header().Set("Content-Digest", contentDigest(mustDecodeHash(artifact.BlobSHA256)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data) //nolint:gosec // Rehashed and independently parsed PDF bytes; attachment is PDF with nosniff.
	})
	api.OpenAPI().AddOperation(&huma.Operation{OperationID: "downloadBatesExportContent", Method: http.MethodGet,
		Path: "/api/v1/bates/exports/{id}/content", Summary: "Download independently reverified Bates export bytes",
		Parameters: []*huma.Param{{Name: "id", In: openAPIPathLocation, Required: true,
			Schema: &huma.Schema{Type: openAPIStringType, Format: "uuid"}}},
		Responses: map[string]*huma.Response{"200": {Description: "Exact retained Bates PDF",
			Content: map[string]*huma.MediaType{"application/pdf": {}}}}})
}

func bindBatesPlanRequest(ctx context.Context, s *store.Store, value BatesPlanRequest) (store.BatesPlanRequest, error) {
	if len(value.Pages) > maxBatesRouteLabels {
		return store.BatesPlanRequest{}, store.ErrBatesPageCountMismatch
	}
	namespace, err := s.BatesNamespace(ctx, value.NamespaceID, value.Prefix, value.Suffix, value.Padding)
	if err != nil {
		return store.BatesPlanRequest{}, err
	}
	request := batesPlanRequestStore(value)
	request.NamespaceID = namespace.NamespaceID
	if len(request.Pages) == 0 {
		request.Pages, err = s.SnapshotBatesPages(ctx, value.SnapshotID, maxBatesRouteLabels)
		if err != nil {
			return store.BatesPlanRequest{}, err
		}
	}
	return request, nil
}
