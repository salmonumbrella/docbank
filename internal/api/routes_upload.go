package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/kit/packstore"

	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/ingest"
	"go.kenn.io/docbank/internal/store"
)

const uploadMultipartOverhead = int64(1 << 20)

var errInvalidUploadEnvelope = errors.New("invalid upload multipart envelope")

func registerUploadRoute(mux *http.ServeMux, api huma.API, d Deps, g *gate) {
	registerUploadOpenAPI(api)
	registerPushUploadOpenAPI(api)
	registerPushRoutes(api, d)
	mux.HandleFunc("POST /api/v1/push/uploads", func(w http.ResponseWriter, r *http.Request) { handleUpload(w, r, d, g) })
	mux.HandleFunc("POST /api/v1/uploads", func(w http.ResponseWriter, r *http.Request) {
		handleUpload(w, r, d, g)
	})
}

func registerUploadOpenAPI(api huma.API) {
	registry := api.OpenAPI().Components.Schemas
	receiptSchema := registry.Schema(reflect.TypeFor[UploadReceipt](), true, "")
	errorSchema := registry.Schema(reflect.TypeFor[Error](), true, "")
	response := func(description string) *huma.Response {
		return &huma.Response{Description: description, Content: map[string]*huma.MediaType{
			"application/json": {Schema: receiptSchema},
		}}
	}
	api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: "uploadFile", Method: http.MethodPost, Path: "/api/v1/uploads",
		Summary: "Upload one digest-checked file",
		Description: "Streams exactly one multipart field named `file`. " +
			"X-Docbank-Blob-Hash and X-Docbank-Blob-Size are required declarations for the file payload, " +
			"not the multipart envelope. Success grants node/blob authority only after both match.",
		Parameters: []*huma.Param{
			{Name: "parent_id", In: openAPIQueryLocation, Required: true,
				Description: "Stable destination directory node ID", Schema: &huma.Schema{Type: "integer", Format: "int64"}},
			{Name: "name", In: openAPIQueryLocation, Required: true,
				Description: "Virtual filename; must equal the multipart filename", Schema: &huma.Schema{Type: openAPIStringType, MinLength: new(1)}},
			{Name: BlobHashHeader, In: "header", Required: true,
				Description: "Expected lowercase hexadecimal SHA-256 of the file payload",
				Schema:      &huma.Schema{Type: openAPIStringType, Pattern: "^[0-9a-f]{64}$"}},
			{Name: BlobSizeHeader, In: "header", Required: true,
				Description: "Expected raw file byte length",
				Schema:      &huma.Schema{Type: "integer", Format: "int64", Minimum: new(float64(0))}},
		},
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
			"multipart/form-data": {
				Schema: &huma.Schema{Type: "object", Properties: map[string]*huma.Schema{
					"file": {Type: openAPIStringType, Format: openAPIBinaryFormat},
				}, Required: []string{"file"}},
				Encoding: map[string]*huma.Encoding{"file": {ContentType: "*/*"}},
			},
		}},
		Responses: map[string]*huma.Response{
			"200": response("Idempotent retry converged on the existing node"),
			"201": response("New file node created"),
			"default": {Description: "Error", Content: map[string]*huma.MediaType{
				"application/problem+json": {Schema: errorSchema},
			}},
		},
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request, d Deps, g *gate) {
	parentID, name, expectedHash, expectedSize, identityErr := parseUploadIdentity(r)
	if identityErr != nil {
		writeError(w, identityErr)
		return
	}
	var source *store.PushSource
	if r.URL.Path == "/api/v1/push/uploads" {
		source = &store.PushSource{Name: r.URL.Query().Get("push_name"), Ref: r.URL.Query().Get("source_ref"), ModifiedAt: r.URL.Query().Get("modified_at"), Duplicates: r.URL.Query().Get("duplicates")}
		if err := store.ValidatePushSource(*source); err != nil {
			writeError(w, NewError(http.StatusUnprocessableEntity, "validation", err.Error()))
			return
		}
	}
	status := "skipped"
	maxBody := expectedSize + uploadMultipartOverhead
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	multipartReader, readErr := r.MultipartReader()
	if readErr != nil {
		writeError(w, NewError(http.StatusUnsupportedMediaType, "validation",
			"upload requires multipart/form-data: "+readErr.Error()))
		return
	}
	part, readErr := multipartReader.NextPart()
	if readErr != nil {
		writeError(w, uploadMultipartReadError(readErr, "upload requires exactly one file part"))
		return
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != "file" || part.FileName() == "" {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation",
			"first multipart part must be a file field named \"file\""))
		return
	}
	partName, normalizeErr := store.NormalizeName(part.FileName())
	if normalizeErr != nil || partName != name {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation",
			fmt.Sprintf("multipart filename %q must equal requested name %q", part.FileName(), name)))
		return
	}
	mimeType, mimeErr := uploadMediaType(part.Header.Get("Content-Type"))
	if mimeErr != nil {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation", mimeErr.Error()))
		return
	}

	var result ingest.UploadResult
	opErr := g.mutate(func() error {
		return d.Blobs.WithMutation(r.Context(), func() error {
			limited := &io.LimitedReader{R: part, N: expectedSize + 1}
			ing := &ingest.Ingester{Store: d.Store, Blobs: d.Blobs}
			prepared, prepareErr := ing.PrepareUpload(r.Context(), parentID, name, mimeType,
				limited, expectedHash, expectedSize)
			if prepareErr != nil {
				return prepareErr
			}
			next, nextErr := multipartReader.NextPart()
			if next != nil {
				_ = next.Close()
			}
			if !errors.Is(nextErr, io.EOF) {
				if nextErr == nil {
					return errors.Join(fmt.Errorf(
						"%w: upload contains more than one multipart part", errInvalidUploadEnvelope,
					), prepared.Discard())
				}
				if _, ok := errors.AsType[*http.MaxBytesError](nextErr); ok {
					return errors.Join(
						fmt.Errorf("upload multipart body exceeded limit: %w", nextErr),
						prepared.Discard(),
					)
				}
				return errors.Join(fmt.Errorf("%w: reading end of multipart body: %w",
					errInvalidUploadEnvelope, nextErr), prepared.Discard())
			}
			if source != nil {
				result, status, prepareErr = prepared.CommitPush(r.Context(), *source)
			} else {
				result, prepareErr = prepared.Commit(r.Context())
			}
			return prepareErr
		})
	})
	if opErr != nil {
		writeError(w, uploadError(opErr))
		return
	}
	httpStatus := http.StatusOK
	if result.Added {
		status = "added"
		httpStatus = http.StatusCreated
	}
	writeJSON(w, httpStatus, UploadReceipt{
		Status: status, Node: fromStoreNode(result.Node),
		ComputedHash: result.ComputedHash, ComputedSize: result.ComputedSize,
	})
}

func uploadMultipartReadError(err error, detail string) *Error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return NewError(http.StatusRequestEntityTooLarge, "too_large", detail+": "+err.Error())
	}
	return NewError(http.StatusUnprocessableEntity, "validation", detail+": "+err.Error())
}

func parseUploadIdentity(r *http.Request) (int64, string, string, int64, *Error) {
	parentID, err := strconv.ParseInt(r.URL.Query().Get("parent_id"), 10, 64)
	if err != nil || parentID <= 0 {
		return 0, "", "", 0, NewError(http.StatusUnprocessableEntity, "validation",
			"parent_id must be a positive node ID")
	}
	name, err := store.NormalizeName(r.URL.Query().Get("name"))
	if err != nil {
		return 0, "", "", 0, NewError(http.StatusUnprocessableEntity, "invalid_name", err.Error())
	}
	expectedHash, expectedSize, identityErr := parseExpectedBlobIdentity(r)
	if identityErr != nil {
		return 0, "", "", 0, identityErr
	}
	return parentID, name, expectedHash, expectedSize, nil
}

func parseExpectedBlobIdentity(r *http.Request) (string, int64, *Error) {
	expectedHash := r.Header.Get(BlobHashHeader)
	parsed, err := packstore.ParseHash(expectedHash)
	if err != nil || parsed.String() != expectedHash {
		return "", 0, NewError(http.StatusUnprocessableEntity, "validation",
			BlobHashHeader+" must be canonical lowercase SHA-256")
	}
	expectedSize, err := strconv.ParseInt(r.Header.Get(BlobSizeHeader), 10, 64)
	if err != nil || expectedSize < 0 || expectedSize > blob.MaxIngestBytes {
		return "", 0, NewError(http.StatusUnprocessableEntity, "validation",
			fmt.Sprintf("%s must be between 0 and %d", BlobSizeHeader, blob.MaxIngestBytes))
	}
	return expectedHash, expectedSize, nil
}

func uploadMediaType(value string) (string, error) {
	if value == "" {
		return "application/octet-stream", nil
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "", fmt.Errorf("invalid file Content-Type %q: %w", value, err)
	}
	return mime.FormatMediaType(mediaType, params), nil
}

func uploadError(err error) *Error {
	var maxBytesErr *http.MaxBytesError
	var apiErr *Error
	switch {
	case errors.As(err, &apiErr):
		return apiErr
	case errors.Is(err, ingest.ErrUploadDigestMismatch):
		return NewError(http.StatusUnprocessableEntity, "digest_mismatch", err.Error())
	case errors.Is(err, ingest.ErrUploadSizeMismatch):
		return NewError(http.StatusUnprocessableEntity, "size_mismatch", err.Error())
	case errors.As(err, &maxBytesErr):
		return NewError(http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, io.ErrUnexpectedEOF):
		return NewError(http.StatusUnprocessableEntity, "validation",
			"invalid upload multipart envelope: "+err.Error())
	case errors.Is(err, errInvalidUploadEnvelope):
		return NewError(http.StatusUnprocessableEntity, "validation", err.Error())
	default:
		mapped := &Error{}
		if errors.As(FromStoreError(err), &mapped) {
			return mapped
		}
		return NewError(http.StatusInternalServerError, "internal", err.Error())
	}
}

func registerPushUploadOpenAPI(api huma.API) {
	original := api.OpenAPI().Paths["/api/v1/uploads"].Post
	operation := *original
	operation.Path = "/api/v1/push/uploads"
	operation.OperationID = "uploadPushFile"
	operation.Summary = "Upload one digest-checked push source"
	operation.Description = original.Description + " Records push provenance atomically. Existing source identities follow their node and version changed source bytes. Duplicate policy applies only to new identities."
	operation.Parameters = append([]*huma.Param{}, original.Parameters...)
	for _, field := range []struct {
		name, description string
		required          bool
		max               int
	}{
		{"push_name", "Portable push identity; lowercase letters, digits, -, _, .", true, 64},
		{"source_ref", "Canonical relative slash path", true, 4096},
		{"duplicates", "New-identity duplicate policy: link, skip, or create", true, 6},
		{"modified_at", "Original source modification time in canonical UTC RFC3339Nano", false, 64},
	} {
		operation.Parameters = append(operation.Parameters, &huma.Param{Name: field.name, In: openAPIQueryLocation, Required: field.required, Description: field.description, Schema: &huma.Schema{Type: openAPIStringType, MaxLength: new(field.max)}})
	}
	api.OpenAPI().AddOperation(&operation)
}
