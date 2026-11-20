package processing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/document/okf"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
)

var ErrOKFOriginUnavailable = errors.New("okf source links need the stable address readers use; set [web] public_origin (and [web] base_path behind a proxy subpath), then restart the daemon")

func PrepareOKFExportPlan(ctx context.Context, catalog *store.Store, blobs *blob.Store, owner string, request bundle.PlanRequest, origin string, publish, maintain func(context.Context, func() error) error) (plan bundle.Plan, resultErr error) {
	if request.OKF == nil {
		return plan, bundle.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if p, found, err := catalog.ExportPlanReplay(ctx, owner, request); found || err != nil {
		return p, err
	}
	if origin == "" {
		return plan, ErrOKFOriginUnavailable
	}
	release, err := catalog.AcquirePhotoExportPreparation(ctx)
	if err != nil {
		return plan, err
	}
	defer release()
	if p, found, err := catalog.ExportPlanReplay(ctx, owner, request); found || err != nil {
		return p, err
	}
	if blobs == nil {
		return plan, errors.New("OKF export blob store unavailable")
	}
	input, err := catalog.ExportOKFInput(ctx, owner, request)
	if err != nil {
		return plan, err
	}
	artifact, err := readExportBlob(ctx, blobs, input.ArtifactSHA256, input.ArtifactSize, document.MaxRenditionMarkdownBytes+(256<<10))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plan, err
		}
		return plan, fmt.Errorf("%w: retained artifact cannot be verified", bundle.ErrUnavailable)
	}
	front, body, err := document.ParseRenditionFrontMatterV1(artifact)
	if err != nil {
		return plan, fmt.Errorf("%w: reprocess to export OKF", bundle.ErrUnavailable)
	}
	if front.Source.SHA256 != input.Member.SHA256 || front.Rendition.BuildID != request.OKF.BuildID || front.Rendition.RenditionRequestFingerprint != input.Build.RenditionRequestFingerprint || front.Rendition.EvidenceLexicalFingerprint != input.Build.EvidenceLexicalFingerprint {
		return plan, bundle.ErrConflict
	}
	if err := verifyOKFOriginal(ctx, blobs, input.Member.SHA256, input.Member.Size); err != nil {
		return plan, err
	}
	resource := fmt.Sprintf("%s/api/v1/versions/%s/content", origin, input.Member.VersionID)
	title := input.Name
	if front.Document.Title != "" {
		title = front.Document.Title
	}
	header := okf.Header{Title: title, SourceTitle: input.Name, Resource: resource}
	tags := make([]string, 0, len(input.Tags))
	for _, tag := range input.Tags {
		tags = append(tags, tag.Name)
	}
	header.Tags = tags
	if request.OKF.IncludeSourceSHA256 {
		header.SourceSHA256 = input.Member.SHA256
	}
	if input.Generated != nil {
		header.Generated = &okf.Generated{By: input.Generated.By, At: input.Generated.At}
	}
	output, err := okf.Render(header, body)
	if err != nil {
		return plan, err
	}
	frozen := bundle.OKFReceipt{Version: 1, Source: input.Member, NodeRevision: input.Member.Revision, Option: *request.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: resource, Tags: tags}
	if header.Generated != nil {
		frozen.Generated = &bundle.OKFGenerated{By: header.Generated.By, At: header.Generated.At}
	}
	a := store.PreparedOKFExport{Input: input, Receipt: frozen}
	var created, own bool
	err = publish(ctx, func() error {
		return blobs.WithMutation(ctx, func() error {
			written, e := blobs.WriteDetailedWithPublicationContext(ctx, bytes.NewReader(output))
			created = written.Created
			a.SHA256, a.Size = written.Hash, written.Size
			if e != nil {
				return e
			}
			encoding, e := written.EncodingName()
			if e != nil {
				return e
			}
			a.Physical = store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created}
			plan, own, e = catalog.SealOKFExportPlan(ctx, owner, request, a)
			return e
		})
	})
	if own {
		return plan, nil
	}
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

func verifyOKFOriginal(ctx context.Context, source store.RenditionBlobReader, hash string, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size < 0 || size > bundle.MaxRoleBytes {
		return bundle.ErrLimit
	}
	stream, actualSize, err := source.OpenStreamContext(ctx, hash)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: original cannot be verified", bundle.ErrUnavailable)
	}
	if actualSize != size {
		_ = stream.Close()
		return bundle.ErrUnavailable
	}
	n, copyErr := io.CopyBuffer(io.Discard, io.LimitReader(blob.ReaderWithContext(ctx, stream), size+1), make([]byte, bundle.BufferSize))
	verified := stream.Verified()
	closeErr := stream.Close()
	if err = errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return err
	}
	if n != size || !verified {
		return bundle.ErrUnavailable
	}
	return nil
}
