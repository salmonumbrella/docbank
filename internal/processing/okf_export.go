package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/document/okf"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/filepublish"
	"go.kenn.io/docbank/internal/store"
)

var ErrOKFOriginUnavailable = errors.New("OKF source links need the stable address readers use. Set [web] public_origin (and [web] base_path behind a proxy subpath), then restart the daemon.")
var okfProviderID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func PrepareOKFExportPlan(ctx context.Context, catalog *store.Store, blobs *blob.Store, spool, owner string, request bundle.PlanRequest, origin string, publish, maintain func(context.Context, func() error) error) (plan bundle.Plan, resultErr error) {
	if request.OKF == nil {
		return plan, bundle.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if p, found, err := catalog.ExportPlanReplay(ctx, owner, request); found || err != nil {
		return p, err
	}
	release, err := catalog.AcquirePhotoExportPreparation(ctx)
	if err != nil {
		return plan, err
	}
	defer release()
	if p, found, err := catalog.ExportPlanReplay(ctx, owner, request); found || err != nil {
		return p, err
	}
	if origin == "" {
		return plan, ErrOKFOriginUnavailable
	}
	if blobs == nil {
		return plan, errors.New("OKF export blob store unavailable")
	}
	input, err := catalog.ExportOKFInput(ctx, owner, request)
	if err != nil {
		return plan, err
	}
	if input.ArtifactSize > document.MaxRenditionMarkdownBytes+(256<<10) {
		return plan, bundle.ErrLimit
	}
	artifact, err := readExportBlob(ctx, blobs, input.ArtifactSHA256, input.ArtifactSize, document.MaxRenditionMarkdownBytes+(256<<10))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plan, err
		}
		return plan, fmt.Errorf("%w: retained artifact cannot be verified", bundle.ErrUnavailable)
	}
	front, body, err := document.ParseRenditionFrontMatterV1(artifact)
	if len(artifact) == 0 && len(input.Build.LexicalSegments) == 0 && input.MediaType == "application/pdf" {
		body = artifact
		err = nil
		front.Source.SHA256 = input.Member.SHA256
		front.Rendition.BuildID = request.OKF.BuildID
		front.Rendition.RenditionRequestFingerprint = input.Build.RenditionRequestFingerprint
		front.Rendition.EvidenceLexicalFingerprint = input.Build.EvidenceLexicalFingerprint
	}
	if err != nil {
		return plan, fmt.Errorf("%w: reprocess to export OKF", bundle.ErrUnavailable)
	}
	if front.Source.SHA256 != input.Member.SHA256 || front.Rendition.BuildID != request.OKF.BuildID || front.Rendition.RenditionRequestFingerprint != input.Build.RenditionRequestFingerprint || front.Rendition.EvidenceLexicalFingerprint != input.Build.EvidenceLexicalFingerprint {
		return plan, bundle.ErrConflict
	}
	if input.Member.Size < 0 || input.Member.Size > bundle.MaxRoleBytes {
		return plan, bundle.ErrLimit
	}
	stream, size, err := blobs.OpenStreamContext(ctx, input.Member.SHA256)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plan, err
		}
		return plan, fmt.Errorf("%w: original cannot be verified", bundle.ErrUnavailable)
	}
	if size != input.Member.Size {
		_ = stream.Close()
		return plan, bundle.ErrUnavailable
	}
	digest := sha256.New()
	n, copyErr := io.CopyBuffer(digest, io.LimitReader(stream, size+1), make([]byte, bundle.BufferSize))
	verified := stream.Verified()
	closeErr := stream.Close()
	if err = errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return plan, err
	}
	if n != size || !verified || hex.EncodeToString(digest.Sum(nil)) != input.Member.SHA256 {
		return plan, bundle.ErrUnavailable
	}
	resource := fmt.Sprintf("%s/api/v1/versions/%s/content", origin, input.Member.VersionID)
	header := okf.Header{Title: input.Name, SourceTitle: input.Name, Resource: resource}
	tags := make([]string, 0, len(input.Tags))
	for _, tag := range input.Tags {
		tags = append(tags, tag.Name)
	}
	header.Tags = tags
	if request.OKF.IncludeSourceSHA256 {
		header.SourceSHA256 = input.Member.SHA256
	}
	var probe struct {
		SourceSHA256 string `json:"source_sha256"`
	}
	if json.Unmarshal(input.Build.ProviderReceipt, &probe) == nil && probe.SourceSHA256 != "" && probe.SourceSHA256 != input.Member.SHA256 {
		return plan, bundle.ErrConflict
	}
	var receipt document.RenditionReceipt
	if json.Unmarshal(input.Build.ProviderReceipt, &receipt, json.RejectUnknownMembers(true)) == nil {
		if receipt.SourceSHA256 != "" && receipt.SourceSHA256 != input.Member.SHA256 {
			return plan, bundle.ErrConflict
		}
		if receipt.SourceSHA256 == input.Member.SHA256 && receipt.RenditionRequestFingerprint == input.Build.RenditionRequestFingerprint && receipt.OperationID == input.Build.ProviderOperationID && receipt.CompletedAt == input.Build.CompletedAt && okfProviderID.MatchString(receipt.ProviderID) {
			if at, e := time.Parse(time.RFC3339, receipt.CompletedAt); e == nil {
				header.Generated = &okf.Generated{By: "process:" + receipt.ProviderID, At: at.UTC().Format(time.RFC3339Nano)}
			}
		}
	}
	output, err := okf.Render(header, body)
	if err != nil {
		return plan, err
	}
	if err = os.MkdirAll(spool, 0700); err != nil {
		return plan, err
	}
	stage, err := filepublish.CreateStage(spool, ".export-okf-")
	if err != nil {
		return plan, err
	}
	defer func() { resultErr = errors.Join(resultErr, stage.Cleanup()) }()
	if _, err = stage.File.Write(output); err != nil {
		return plan, err
	}
	frozen := bundle.OKFReceipt{Version: 1, Source: input.Member, NodeRevision: input.Member.Revision, Option: *request.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: resource, Tags: tags}
	if header.Generated != nil {
		frozen.Generated = &bundle.OKFGenerated{By: header.Generated.By, At: header.Generated.At}
	}
	a := store.PreparedOKFExport{Input: input, Receipt: frozen}
	var created bool
	err = publish(ctx, func() error {
		return blobs.WithMutation(ctx, func() error {
			if _, e := stage.File.Seek(0, io.SeekStart); e != nil {
				return e
			}
			written, e := blobs.WriteDetailedContext(ctx, stage.File)
			if e != nil {
				return e
			}
			created = written.Created
			a.SHA256, a.Size = written.Hash, written.Size
			encoding, e := written.EncodingName()
			if e != nil {
				return e
			}
			a.Physical = store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created}
			plan, e = catalog.SealOKFExportPlan(ctx, owner, request, a)
			return e
		})
	})
	if created {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		cleanupErr := maintain(cleanupCtx, func() error {
			return blobs.WithMaintenance(cleanupCtx, func() error {
				free, e := catalog.OKFBlobUnreferenced(cleanupCtx, a.SHA256)
				if e != nil {
					return e
				}
				if free {
					return blobs.Remove(a.SHA256)
				}
				return nil
			})
		})
		err = errors.Join(err, cleanupErr)
	}
	return plan, err
}
