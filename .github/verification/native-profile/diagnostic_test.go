//go:build linux && cgo && sqlite_trace

package processing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/media/mediatest"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/docbank/internal/vectorworker"
	docsqlite "go.kenn.io/docbank/sqlite"
)

// TestNativeWarmSearchProfile is an opt-in, one-call diagnostic. The runner
// invokes only this test binary with the perf FIFO paths set. Fixture creation,
// the 50k source expansion, index construction, and the warmup happen before
// perf is enabled. This does not report latency acceptance or a speedup.
func TestNativeWarmSearchProfile(t *testing.T) {
	if store.DefaultSQLiteDriver().Name() != "mattn/go-sqlite3" {
		t.Skip("native SQLite profiling requires the cgo driver")
	}
	controlPath, ackPath := os.Getenv("DOCBANK_PERF_CONTROL_FIFO"), os.Getenv("DOCBANK_PERF_ACK_FIFO")
	require.NotEmpty(t, controlPath)
	require.NotEmpty(t, ackPath)

	stats := &nativeProfileSQLiteStats{}
	fixture, service, job := newNativeProfileSearchFixture(t, stats)
	source, err := fixture.catalog.NodeByPath(t.Context(), "/source.pdf")
	require.NoError(t, err)
	_, _, err = fixture.catalog.Trash(t.Context(), source.ID, source.Revision)
	require.NoError(t, err)
	versions := growSearchScopeWorkload(t, fixture, job, 50_000)
	require.Len(t, versions, 50_000)

	cache := service.generationSource()
	var loads atomic.Uint64
	load := cache.load
	cache.load = func(ctx context.Context, id string) (store.VectorIndexGenerationRecord, error) {
		loads.Add(1)
		return load(ctx, id)
	}
	request := searchScopeRequest()

	stats.reset()
	stats.enabled.Store(true)
	warmup, err := service.Search(t.Context(), request)
	stats.enabled.Store(false)
	warmupSQL := stats.snapshot()
	require.NoError(t, err)
	require.Len(t, warmup.Results, 10)
	require.Equal(t, 50_000, warmup.Coverage.ScopedDocuments)
	require.Equal(t, 50_000, warmup.Coverage.CompleteDocuments)
	require.Empty(t, warmup.MediaSelections)
	require.False(t, warmup.MediaSourceSelection)
	require.Equal(t, int64(1), warmupLoads(loads.Load()))
	warmEntry := searchBenchmarkEntry(t, cache)
	warmGeneration := warmEntry.generation
	require.NotNil(t, warmGeneration)
	require.Nil(t, warmEntry.record.Bytes)
	require.NotZero(t, warmupSQL.selectExecutions)
	require.NotZero(t, warmupSQL.selectPrepareCallbacks)
	require.Zero(t, warmupSQL.genericFallbacks)

	control, err := openNativeProfileControl(controlPath, ackPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.close() })
	profileMayBeEnabled := false
	defer func() {
		if profileMayBeEnabled {
			_ = control.command("disable", 10*time.Second)
		}
	}()
	beforeFDs, err := nativeProfileFDCount()
	require.NoError(t, err)
	beforeCgo := runtime.NumCgoCall()
	beforeLoads := loads.Load()
	stats.reset()
	stats.enabled.Store(true)
	profileMayBeEnabled = true
	require.NoError(t, control.command("enable", 10*time.Second))
	started := time.Now()
	measured, searchErr := service.Search(t.Context(), request)
	searchElapsed := time.Since(started)
	disableErr := control.command("disable", 10*time.Second)
	if disableErr == nil {
		profileMayBeEnabled = false
	}
	stats.enabled.Store(false)
	afterCgo := runtime.NumCgoCall()
	afterFDs, fdErr := nativeProfileFDCount()
	measuredSQL := stats.snapshot()
	require.NoError(t, disableErr)
	require.NoError(t, searchErr)
	require.NoError(t, fdErr)
	require.Len(t, measured.Results, 10)
	require.Equal(t, 50_000, measured.Coverage.ScopedDocuments)
	require.Equal(t, 50_000, measured.Coverage.CompleteDocuments)
	require.Empty(t, measured.MediaSelections)
	require.False(t, measured.MediaSourceSelection)
	require.Equal(t, beforeLoads, loads.Load(), "warm search must not load or decode the generation")
	require.Same(t, warmGeneration, searchBenchmarkEntry(t, cache).generation)
	require.Equal(t, warmupSQL.selectExecutions, measuredSQL.selectExecutions+1,
		"warmup generation load must execute one SELECT avoided by the cached search")
	require.Zero(t, measuredSQL.selectPrepareCallbacks, "warm search must reuse prepared SQLite statements")
	require.Zero(t, measuredSQL.genericFallbacks, "canonical source validation must not replay the generic path")
	require.Equal(t, beforeFDs, afterFDs, "warm search must not leak process file descriptors")
	require.Positive(t, afterCgo-beforeCgo, "search must execute native SQLite calls")

	t.Logf("profile_guard warmup_selects=%d warmup_select_prepare_callbacks=%d warmup_fallbacks=%d warmup_generation_loads=%d media_resources=%d coverage=%d/%d",
		warmupSQL.selectExecutions, warmupSQL.selectPrepareCallbacks, warmupSQL.genericFallbacks, loads.Load(),
		len(warmup.MediaSelections),
		warmup.Coverage.CompleteDocuments, warmup.Coverage.ScopedDocuments)
	t.Logf("profile_guard measured_selects=%d measured_select_prepare_callbacks=%d measured_fallbacks=%d measured_generation_load_delta=%d same_generation=true fd_delta=%d cgo_call_delta=%d media_resources=%d coverage=%d/%d go_cpu_profile=not_collected",
		measuredSQL.selectExecutions, measuredSQL.selectPrepareCallbacks, measuredSQL.genericFallbacks,
		loads.Load()-beforeLoads, afterFDs-beforeFDs, afterCgo-beforeCgo, len(measured.MediaSelections),
		measured.Coverage.CompleteDocuments, measured.Coverage.ScopedDocuments)
	t.Logf("profile_guard one_search_wall_time=%s; this single diagnostic duration is not a p50 or acceptance result", searchElapsed)
}

func warmupLoads(loads uint64) int64 { return int64(loads) }

type nativeProfileSQLiteStats struct {
	enabled                atomic.Bool
	selectExecutions       atomic.Uint64
	selectPrepareCallbacks atomic.Uint64
	genericFallbacks       atomic.Uint64
}

type nativeProfileSQLiteSnapshot struct {
	selectExecutions, selectPrepareCallbacks, genericFallbacks uint64
}

func (stats *nativeProfileSQLiteStats) reset() {
	stats.selectExecutions.Store(0)
	stats.selectPrepareCallbacks.Store(0)
	stats.genericFallbacks.Store(0)
}

func (stats *nativeProfileSQLiteStats) snapshot() nativeProfileSQLiteSnapshot {
	return nativeProfileSQLiteSnapshot{
		selectExecutions: stats.selectExecutions.Load(), selectPrepareCallbacks: stats.selectPrepareCallbacks.Load(),
		genericFallbacks: stats.genericFallbacks.Load(),
	}
}

type nativeProfileSQLiteDriver struct {
	base  docsqlite.Driver
	stats *nativeProfileSQLiteStats
}

func (driver nativeProfileSQLiteDriver) Name() string          { return driver.base.Name() + "-profile-trace" }
func (driver nativeProfileSQLiteDriver) IsBusy(err error) bool { return driver.base.IsBusy(err) }
func (driver nativeProfileSQLiteDriver) IsUniqueViolation(err error) bool {
	return driver.base.IsUniqueViolation(err)
}

// Open keeps one connection per test pool and attaches hooks before Store uses
// it. Counters stay disabled during setup and workload construction.
func (driver nativeProfileSQLiteDriver) Open(path string, options docsqlite.OpenOptions) (*sql.DB, error) {
	db, err := driver.base.Open(path, options)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	err = conn.Raw(func(raw any) error {
		native, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected SQLite connection type %T", raw)
		}
		native.RegisterAuthorizer(func(action int, _, _, _ string) int {
			if driver.stats.enabled.Load() && action == sqlite3.SQLITE_SELECT {
				driver.stats.selectPrepareCallbacks.Add(1)
			}
			return sqlite3.SQLITE_OK
		})
		return native.SetTrace(&sqlite3.TraceConfig{EventMask: sqlite3.TraceStmt, Callback: func(info sqlite3.TraceInfo) int {
			if !driver.stats.enabled.Load() || info.EventCode != sqlite3.TraceStmt {
				return 0
			}
			statement := strings.TrimSpace(info.StmtOrTrigger)
			upper := strings.ToUpper(statement)
			if strings.HasPrefix(upper, "SELECT") {
				driver.stats.selectExecutions.Add(1)
			}
			if strings.EqualFold(statement, "ROLLBACK TO semantic_source_singleton") {
				driver.stats.genericFallbacks.Add(1)
			}
			return 0
		}})
	})
	closeErr := conn.Close()
	if err != nil || closeErr != nil {
		_ = db.Close()
		return nil, errors.Join(err, closeErr)
	}
	return db, nil
}

func newNativeProfileSearchFixture(
	tb testing.TB, stats *nativeProfileSQLiteStats,
) (publicationFixture, *Service, store.EmbeddingJobRequest) {
	tb.Helper()
	root := tb.TempDir()
	driver := nativeProfileSQLiteDriver{base: store.DefaultSQLiteDriver(), stats: stats}
	catalog, err := store.Open(tb.Context(), filepath.Join(root, "docbank.db"), driver)
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, catalog.Close()) })
	blobs, err := blob.New(store.NewPackCatalog(catalog), filepath.Join(root, "blobs"))
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, blobs.Close()) })
	receipt, err := blobs.WriteDetailedContext(tb.Context(), strings.NewReader("synthetic private-free source"))
	require.NoError(tb, err)
	source, err := catalog.CreateFile(tb.Context(), catalog.RootID(), "source.pdf", receipt.Hash, receipt.Size,
		"application/pdf", processingBlobPhysical(tb, receipt))
	require.NoError(tb, err)
	evidencePolicy, err := document.NewEvidencePolicy(100_000)
	require.NoError(tb, err)
	renditionPolicy, err := document.NewRenditionPolicy(document.RenditionLimits{
		MaxDocumentChars: 100_000, MaxUnitRunes: 1000, MaxSegmentRunes: 100,
	})
	require.NoError(tb, err)
	fixture := publicationFixture{catalog: catalog, databasePath: filepath.Join(root, "docbank.db"), blobs: blobs,
		profile: processingProfile(tb), evidencePolicy: evidencePolicy, renditionPolicy: renditionPolicy, versionID: source.CurrentVersionID}

	fake := newEmbeddingWorkerFixture(tb)
	descriptor := embeddingWorkerDescriptor(tb)
	descriptor.Dimension, descriptor.Fingerprint = 1024, ""
	descriptor, err = document.NewEmbeddingDescriptor(descriptor)
	require.NoError(tb, err)
	fake.descriptor, fake.profile = descriptor, embeddingWorkerProfile(tb, descriptor)
	var portable, embeddingProfile document.ProcessingProfileV1
	require.NoError(tb, json.Unmarshal(fixture.profile.CanonicalProfile, &portable))
	require.NoError(tb, json.Unmarshal(fake.profile.CanonicalProfile, &embeddingProfile))
	renditionProvider := newWorkerProvider(tb).Descriptor()
	portable.Rendition.TrustBoundary = string(renditionProvider.TrustBoundary)
	portable.Rendition.Descriptor = document.ProviderDescriptorV1{ID: renditionProvider.ID, Fingerprint: renditionProvider.Fingerprint}
	var binding document.EmbeddingBindingV1
	for _, candidate := range embeddingProfile.Embeddings {
		if candidate.InputKind == document.EmbeddingInputOriginalFile {
			binding = candidate
		}
	}
	binding.MaxBatchItems = 1
	portable.Embeddings = []document.EmbeddingBindingV1{binding}
	canonical, fingerprints, err := document.CanonicalProfile(portable)
	require.NoError(tb, err)
	fixture.profile.CanonicalProfile, fixture.profile.Fingerprint = canonical, fingerprints.Profile
	fixture.profile.RenditionRequestFingerprint = fingerprints.RenditionRequest
	fixture.profile.RetentionDisclosureFingerprint = fingerprints.RetentionDisclosure

	data := mediatest.PNG(2, 2, color.White)
	imageReceipt, err := blobs.WriteDetailedContext(tb.Context(), bytes.NewReader(data))
	require.NoError(tb, err)
	indexed, err := catalog.CreateFile(tb.Context(), catalog.RootID(), "synthetic.png", imageReceipt.Hash, imageReceipt.Size,
		"image/png", processingBlobPhysical(tb, imageReceipt))
	require.NoError(tb, err)
	generation := store.EmbeddingInputGenerationRecord{ID: workerHash("integration-generation"),
		ProcessingProfileFingerprint: fixture.profile.Fingerprint, SourceVersionID: indexed.CurrentVersionID,
		GenerationChecksum: imageReceipt.Hash, EvidenceFingerprint: workerHash("evidence"),
		TokenizerFingerprint: workerHash("tokenizer"), ChunkPolicyFingerprint: workerHash("chunk"),
		FormatterFingerprint: workerHash("formatter"), CreatedAt: metadataEmbeddingTime(time.Now()),
		Inputs: []store.EmbeddingInputReference{{ID: indexed.CurrentVersionID, RenderedChecksum: imageReceipt.Hash}}}
	staged := fixture.stage(tb, publicationIDs{"b9", "59", "99"}, "synthetic evidence", "Example")
	publisher, err := NewArtifactPublisher(catalog, blobs)
	require.NoError(tb, err)
	_, err = publisher.PublishRendition(tb.Context(), staged)
	require.NoError(tb, err)
	consent := store.ProviderOperationAuthorizationRequest{Principal: "operator:integration", Scope: "embedding:integration",
		ProfileFingerprint: fixture.profile.Fingerprint, DisclosureFingerprint: binding.DisclosureFingerprint,
		InputClasses: []string{string(document.EmbeddingInputOriginalFile)}, RetainedArtifactClasses: []string{"embedding_vector_set"}}
	_, err = catalog.GrantConsent(tb.Context(), store.ProcessingConsentGrantRequest{
		Principal: consent.Principal, Scope: consent.Scope, ProfileFingerprint: consent.ProfileFingerprint,
		DisclosureFingerprint: consent.DisclosureFingerprint, InputClasses: consent.InputClasses,
		RetainedArtifactClasses: consent.RetainedArtifactClasses,
	})
	require.NoError(tb, err)
	job := store.EmbeddingJobRequest{ContentVersionID: generation.SourceVersionID, Profile: fixture.profile,
		BindingID: binding.Name, Descriptor: fake.descriptor, InputGeneration: generation, Authorization: consent}
	_, err = catalog.EnqueueEmbeddingJob(tb.Context(), job)
	require.NoError(tb, err)
	expand := func(result document.EmbeddingResult) document.EmbeddingResult {
		for i := range result.Vectors {
			result.Vectors[i].Values = make([]float32, descriptor.Dimension)
			copy(result.Vectors[i].Values, []float32{1, 2})
		}
		return result
	}
	fake.runtime.mutate["direct"], fake.runtime.mutate["chunk"], fake.runtime.mutate["query"] = expand, expand, expand
	runtimeProvider, err := NewProviderEmbeddingRuntime(
		&embeddingWorkerProvider{runtime: fake.runtime, binding: binding.Name, descriptor: fake.descriptor},
		blobs, tb.TempDir(), fake.runtime.Classify,
	)
	require.NoError(tb, err)
	worker, err := NewEmbeddingWorker(EmbeddingWorkerConfig{Catalog: catalog, Authority: catalog, Blobs: blobs,
		GenerationBlobs: blobs, Runtime: runtimeProvider, Gate: newTestOperationGate(), Owner: "native-profile",
		LeaseDuration: 3 * time.Second, IdleDelay: time.Millisecond, RetryLimit: 3, MaxRetryDelay: time.Second,
		AttemptLifetime: time.Minute, MaxRows: 100, MaxDimensions: descriptor.Dimension, MaxVectorBlobBytes: 1 << 20,
		DescriptorFingerprints: []string{fake.descriptor.Fingerprint}})
	require.NoError(tb, err)
	processed, err := worker.ScanOnce(tb.Context())
	require.NoError(tb, err)
	require.Equal(tb, 1, processed)
	spaces, err := catalog.ListVectorIndexSpaces(tb.Context())
	require.NoError(tb, err)
	require.Len(tb, spaces, 1)
	index, err := vectorworker.NewIndexWorker(vectorworker.IndexWorkerConfig{Catalog: catalog, Mutate: newTestOperationGate().MutateContext,
		Owner: "native-profile", BuildLease: time.Minute, ReaderLease: time.Minute, IdleDelay: time.Millisecond,
		ReadVectorSet: func(ctx context.Context, member store.VectorIndexMember) ([]byte, error) {
			return catalog.ReadVectorIndexVectorSet(ctx, blobs, member)
		}})
	require.NoError(tb, err)
	_, err = index.Rebuild(tb.Context(), spaces[0])
	require.NoError(tb, err)
	profile := ProfileConfig{Profile: portable, RenditionProvider: newWorkerProvider(tb),
		EmbeddingProviders: map[string]document.EmbeddingProvider{job.BindingID: &embeddingWorkerProvider{
			runtime: fake.runtime, binding: "query", descriptor: fake.descriptor}},
		EmbeddingClassifiers: map[string]func(error) (EmbeddingProviderFailure, time.Duration){job.BindingID: fake.runtime.Classify}}
	service, err := NewService(tb.Context(), ServiceConfig{Catalog: catalog, Blobs: blobs, Gate: newTestOperationGate(),
		Principal: "scope-test", Scope: "scope-test", SpoolDirectory: tb.TempDir(), Lifecycle: tb.Context(),
		Profiles: map[string]ProfileConfig{"private": profile}})
	require.NoError(tb, err)
	tb.Cleanup(service.Stop)
	_, err = catalog.GrantConsent(tb.Context(), store.ProcessingConsentGrantRequest{
		Principal: service.principal, Scope: service.scope, ProfileFingerprint: fixture.profile.Fingerprint,
		DisclosureFingerprint: portable.Embeddings[0].DisclosureFingerprint,
		InputClasses:          []string{"query_text"}, RetainedArtifactClasses: []string{},
	})
	require.NoError(tb, err)
	return fixture, service, job
}

func nativeProfileFDCount() (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	return len(entries), err
}

type nativeProfileControl struct {
	control *os.File
	ack     *os.File
	mu      sync.Mutex
}

func openNativeProfileControl(controlPath, ackPath string) (*nativeProfileControl, error) {
	for _, path := range []string{controlPath, ackPath} {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeNamedPipe == 0 {
			return nil, fmt.Errorf("perf control path is not a FIFO: %s", path)
		}
	}
	control, err := os.OpenFile(controlPath, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	ack, err := os.OpenFile(ackPath, os.O_RDWR, 0)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	if err := syscall.SetNonblock(int(ack.Fd()), true); err != nil {
		_ = ack.Close()
		_ = control.Close()
		return nil, err
	}
	return &nativeProfileControl{control: control, ack: ack}, nil
}

func (control *nativeProfileControl) close() error {
	return errors.Join(control.ack.Close(), control.control.Close())
}

func (control *nativeProfileControl) command(command string, timeout time.Duration) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if _, err := control.control.WriteString(command + "\n"); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var response strings.Builder
	buffer := make([]byte, 128)
	for time.Now().Before(deadline) {
		count, err := syscall.Read(int(control.ack.Fd()), buffer)
		if err == nil && count > 0 {
			response.Write(buffer[:count])
			if strings.Contains(response.String(), "\n") {
				ack := response.String()
				if ack != "ack\n" && ack != "ack\n\x00" {
					return fmt.Errorf("unexpected perf FIFO acknowledgement %q", response.String())
				}
				return nil
			}
		} else if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for perf %q acknowledgement", command)
}
