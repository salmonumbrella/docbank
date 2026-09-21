package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.kenn.io/docbank/internal/canonical"
	"go.kenn.io/docbank/internal/loadfile"
	"go.kenn.io/docbank/internal/store"
)

type packageImportBinding struct {
	SourceKind        string `json:"source_kind"`
	SourceLocator     string `json:"source_locator"`
	Into              string `json:"into"`
	AcceptPartial     bool   `json:"accept_partial"`
	IndexSuppliedText bool   `json:"index_supplied_text"`
	Total             int    `json:"total"`
}

func handlePackageImport(w http.ResponseWriter, r *http.Request, d Deps, g *gate) {
	var request PackageImportRequest
	if problem := readPackageJSON(w, r, &request); problem != nil {
		writeError(w, problem)
		return
	}
	if parsed, err := uuid.Parse(request.OperationID); err != nil || parsed.Version() != 4 ||
		request.PreflightID == "" || !validPackageName(request.Name) ||
		utf8.RuneCountInString(request.Party) > 64 || !strings.HasPrefix(request.Into, "/") {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation", "a version-4 operation_id, preflight_id, package name and absolute destination are required"))
		return
	}
	owner, problem := packageContainerOwner(r, d)
	if problem != nil {
		writeError(w, problem)
		return
	}
	requestHash, err := packageImportRequestHash(request)
	if err != nil {
		writePackageError(w, err)
		return
	}
	// An exact retry stays usable after its short-lived preflight expires.
	existing, err := d.Store.PackageImportJob(r.Context(), owner, request.OperationID)
	if err == nil {
		if existing.RequestSHA256 != requestHash {
			writePackageError(w, store.ErrPackageConflict)
			return
		}
		writePackageImportStatus(r.Context(), w, d.Store, existing, http.StatusAccepted)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		writePackageError(w, err)
		return
	}
	preview, err := d.Store.PackagePreflight(r.Context(), owner, request.PreflightID)
	if err != nil {
		writePackageError(w, err)
		return
	}
	if preview.Blocking {
		writePackageError(w, store.ErrPackageConflict)
		return
	}
	var summary PackagePreflight
	if summary, err = packagePreflightFromRecord(preview); err != nil {
		writePackageError(w, err)
		return
	}
	if summary.Records < 1 || summary.Records > maxPackageRecords {
		writePackageError(w, store.ErrPackageConflict)
		return
	}
	into, err := d.Store.NodeByPath(r.Context(), request.Into)
	if err != nil {
		writePackageError(w, err)
		return
	}
	if !into.IsDir() {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation", "destination must be an existing folder"))
		return
	}
	var mapping loadfile.Mapping
	if err := json.Unmarshal([]byte(preview.MappingJSON), &mapping, json.RejectUnknownMembers(true)); err != nil {
		writePackageError(w, err)
		return
	}
	volumes := make([]store.PackageVolume, len(summary.Volumes))
	for i, volume := range summary.Volumes {
		mapped := volume.DeclaredRoot
		if override, ok := mapping.VolumeRoots[volume.VolumeName]; ok {
			mapped = override
		}
		rootBinding, err := canonical.Marshal([]string{"package-volume-root/v1", preview.SourceRef, mapped})
		if err != nil {
			writePackageError(w, err)
			return
		}
		digest := sha256.Sum256(rootBinding)
		volumes[i] = store.PackageVolume{Ordinal: volume.Ordinal, VolumeName: volume.VolumeName,
			DeclaredRoot: volume.DeclaredRoot, MappedRoot: mapped,
			ResolvedRootSHA256: hex.EncodeToString(digest[:])}
	}
	binding := packageImportBinding{SourceKind: preview.SourceKind, SourceLocator: preview.SourceLocator,
		Into: request.Into, AcceptPartial: request.AcceptPartial,
		IndexSuppliedText: request.IndexSuppliedText, Total: summary.Records}
	jobJSON, err := canonical.Marshal(binding)
	if err != nil {
		writePackageError(w, err)
		return
	}
	var admitted store.PackageImportJob
	err = g.MutateContext(r.Context(), func() error {
		run, beginErr := d.Store.BeginIngest(r.Context(), "package:loadfile", preview.SourceRef)
		if beginErr != nil {
			return beginErr
		}
		pkg := store.PackageRequest{PackageID: uuid.NewString(), Direction: "received", State: "importing",
			PackageName: request.Name, PartyLabel: request.Party, IngestID: run.ID(),
			ProfileSHA256: preview.ProfileSHA256, ProfileJSON: preview.ProfileJSON,
			MappingSHA256: preview.MappingSHA256, MappingJSON: preview.MappingJSON,
			ManifestSHA256: preview.ManifestSHA256, ManifestBlobSHA256: preview.ManifestBlobSHA256,
			Volumes: volumes}
		admitted, beginErr = d.Store.AdmitPackageImport(r.Context(), run, pkg, store.PackageImportJobRequest{
			ID: uuid.NewString(), Owner: owner, OperationID: request.OperationID,
			RequestSHA256: requestHash, PreflightID: preview.PreflightID,
			PackageID: pkg.PackageID, JobJSON: jobJSON,
		})
		return beginErr
	})
	if err != nil {
		writePackageError(w, err)
		return
	}
	writePackageImportStatus(r.Context(), w, d.Store, admitted, http.StatusAccepted)
}

func validPackageName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func handlePackageImportStatus(w http.ResponseWriter, r *http.Request, d Deps) {
	owner, problem := packageContainerOwner(r, d)
	if problem != nil {
		writeError(w, problem)
		return
	}
	job, err := d.Store.PackageImportJob(r.Context(), owner, r.PathValue("operation_id"))
	if err != nil {
		writePackageError(w, err)
		return
	}
	writePackageImportStatus(r.Context(), w, d.Store, job, http.StatusOK)
}

func handlePackageImportCancel(w http.ResponseWriter, r *http.Request, d Deps, g *gate) {
	owner, problem := packageContainerOwner(r, d)
	if problem != nil {
		writeError(w, problem)
		return
	}
	var job store.PackageImportJob
	err := g.MutateContext(r.Context(), func() error {
		var cancelErr error
		job, cancelErr = d.Store.CancelPackageImportJob(r.Context(), owner, r.PathValue("operation_id"))
		return cancelErr
	})
	if err != nil {
		writePackageError(w, err)
		return
	}
	writePackageImportStatus(r.Context(), w, d.Store, job, http.StatusOK)
}

func writePackageImportStatus(ctx context.Context, w http.ResponseWriter, catalog *store.Store, job store.PackageImportJob, status int) {
	var binding packageImportBinding
	if err := json.Unmarshal(job.JobJSON, &binding, json.RejectUnknownMembers(true)); err != nil {
		writePackageError(w, err)
		return
	}
	progress, err := catalog.PackageImportProgress(ctx, job.PackageID)
	if err != nil {
		writePackageError(w, err)
		return
	}
	writeJSON(w, status, PackageImportJob{OperationID: job.OperationID, JobID: job.ID,
		PackageID: job.PackageID, PreflightID: job.PreflightID, State: job.State,
		Committed: progress.Committed, Total: binding.Total, GapCount: progress.GapCount, Gaps: progress.Gaps,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt})
}
