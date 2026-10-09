package push_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/ingest"
	"go.kenn.io/docbank/internal/push"
	"go.kenn.io/docbank/internal/store"
)

type fixture struct {
	connection *daemonconn.Connection
	baseURL    string
	store      *store.Store
	blobs      *blob.Store
	vaultRoot  string
	uploads    atomic.Int64
	drop       atomic.Bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "vault.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	blobsDir := filepath.Join(dir, "blobs")
	require.NoError(t, os.MkdirAll(filepath.Join(blobsDir, "tmp"), 0700))
	blobs, err := blob.New(store.NewPackCatalog(s), blobsDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blobs.Close()) })
	cfg := config.Default()
	cfg.Server.APIKey = "synthetic-key"
	server := api.NewServer(api.Deps{Store: s, Blobs: blobs, VaultRoot: dir, Cfg: cfg})
	t.Cleanup(server.Close)
	f := &fixture{store: s, blobs: blobs, vaultRoot: dir}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/push/uploads" {
			f.uploads.Add(1)
			if f.drop.Swap(false) {
				recorded := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorded, r)
				hijacker, ok := w.(http.Hijacker)
				if !assert.True(t, ok) {
					return
				}
				conn, _, err := hijacker.Hijack()
				if !assert.NoError(t, err) {
					return
				}
				assert.NoError(t, conn.Close())
				return
			}
		}
		server.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	f.baseURL = ts.URL
	f.connection, err = daemonconn.NewPushConnection(ts.URL, "synthetic-key")
	require.NoError(t, err)
	return f
}

func options(dir, policy string) push.Options {
	return push.Options{Folder: config.WatchConfig{Name: "laptop", Source: dir, Destination: "/archive", SettleTime: config.Duration(30 * time.Second), ScanInterval: config.Duration(5 * time.Second)}, Duplicates: policy}
}

func TestPushFolderResumesWithoutUploadsAndKeepsDeletedDocuments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0700))
	file := filepath.Join(dir, "nested", "notes.txt")
	require.NoError(t, os.WriteFile(file, []byte("initial"), 0600))
	opts := options(dir, "create")
	report, err := push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Added)
	assert.EqualValues(t, 1, f.uploads.Load())
	node, err := f.store.NodeByPath(t.Context(), "/archive/nested/notes.txt")
	require.NoError(t, err)
	// A new client on a relocated folder has no cursor. All authority is remote.
	resumed, err := daemonconn.NewPushConnection(f.baseURL, "synthetic-key")
	require.NoError(t, err)
	secondDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(secondDir, "nested"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(secondDir, "nested", "notes.txt"), []byte("initial"), 0600))
	resumedOpts := opts
	resumedOpts.Folder.Source = secondDir
	report, err = push.Run(t.Context(), resumed, resumedOpts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.EqualValues(t, 1, f.uploads.Load(), "unchanged re-push must send zero uploads")
	organized, err := f.store.MkdirAll(t.Context(), "/organized")
	require.NoError(t, err)
	_, _, err = f.store.Move(t.Context(), node.ID, organized.ID, "archive-name.txt", store.UnconditionalRev)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("changed"), 0600))
	report, err = push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Updated)
	changed, err := f.store.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	assert.NotEqual(t, node.CurrentVersionID, changed.CurrentVersionID)
	assert.Equal(t, "archive-name.txt", changed.Name)
	assert.Equal(t, organized.ID, *changed.ParentID)
	require.NoError(t, os.Rename(file, filepath.Join(dir, "nested", "renamed.txt")))
	report, err = push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Added)
	renamed, err := f.store.NodeByPath(t.Context(), "/archive/nested/renamed.txt")
	require.NoError(t, err)
	assert.NotEqual(t, node.ID, renamed.ID)
	require.NoError(t, os.Remove(filepath.Join(dir, "nested", "renamed.txt")))
	report, err = push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Zero(t, report.Added)
	kept, err := f.store.NodeByID(t.Context(), renamed.ID)
	require.NoError(t, err)
	assert.Nil(t, kept.TrashedAt)
}

func TestPushFolderDuplicatePolicies(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"link", "skip", "create"} {
		t.Run(policy, func(t *testing.T) {
			f := newFixture(t)
			dir := t.TempDir()
			for _, name := range []string{"a.txt", "b.txt"} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("identical"), 0600))
			}
			report, err := push.Run(t.Context(), f.connection, options(dir, policy))
			require.NoError(t, err)
			switch policy {
			case "link":
				assert.Equal(t, 1, report.Added)
				assert.Equal(t, 1, report.Linked)
			case "skip":
				assert.Equal(t, 1, report.Added)
				assert.Equal(t, 1, report.DuplicateSkipped)
			case "create":
				assert.Equal(t, 2, report.Added)
			}
			before := f.uploads.Load()
			report, err = push.Run(t.Context(), f.connection, options(dir, policy))
			require.NoError(t, err)
			if policy == "skip" {
				assert.Equal(t, 1, report.DuplicateSkipped)
			} else {
				assert.Equal(t, 2, report.Skipped)
				assert.Equal(t, before, f.uploads.Load())
			}
		})
	}
}

func TestPushResumesAfterServerCommitsButResponseIsLost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("durable"), 0600))
	f.drop.Store(true)
	report, err := push.Run(t.Context(), f.connection, options(dir, "link"))
	require.Error(t, err)
	assert.Zero(t, report.Added, "a lost acknowledgment is not success")
	report, err = push.Run(t.Context(), f.connection, options(dir, "link"))
	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.EqualValues(t, 1, f.uploads.Load())
}

func TestPushRejectsMalformedSuccessAndDigestMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sum := sha256.Sum256([]byte("expected"))
	_, err := f.connection.PushUpload(t.Context(), f.store.RootID(), "bad.txt", "text/plain", hex.EncodeToString(sum[:]), 8, strings.NewReader("different"), store.PushSource{Name: "laptop", Ref: "bad.txt", Duplicates: "link"})
	require.Error(t, err)
	_, err = f.store.PushSourceState(t.Context(), store.PushSource{Name: "laptop", Ref: "bad.txt", Duplicates: "link"})
	require.ErrorIs(t, err, store.ErrNotFound)
	for _, body := range []string{`{}`, `{"status":"added","node":{"id":1,"kind":"file","revision":1},"computed_hash":"wrong","computed_size":8}`} {
		t.Run(body, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(ts.Close)
			c, err := daemonconn.NewPushConnection(ts.URL, "synthetic-key")
			require.NoError(t, err)
			_, err = c.PushUpload(t.Context(), 1, "bad.txt", "text/plain", hex.EncodeToString(sum[:]), 8, strings.NewReader("expected"), store.PushSource{Name: "laptop", Ref: "bad.txt", Duplicates: "link"})
			require.Error(t, err)
		})
	}
}

func TestPushCancellationDoesNotReportSuccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := push.Run(ctx, f.connection, options(t.TempDir(), "link"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, report.Added)
	assert.Zero(t, f.uploads.Load())
}

func TestPushWatchUploadsSettledChangesAndStopsOnCancellation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("first"), 0600))
	opts := options(dir, "link")
	opts.Watch = true
	opts.Folder.SettleTime = config.Duration(time.Second)
	opts.Folder.ScanInterval = config.Duration(100 * time.Millisecond)
	events := make(chan string, 4)
	opts.Progress = func(_ string, outcome string) error { events <- outcome; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := push.Run(ctx, f.connection, opts); done <- err }()
	require.Eventually(t, func() bool { return len(events) > 0 }, 30*time.Second, 20*time.Millisecond)
	assert.Equal(t, "added", <-events)
	require.NoError(t, os.WriteFile(file, []byte("changed"), 0600))
	require.Eventually(t, func() bool { return len(events) > 0 }, 30*time.Second, 20*time.Millisecond)
	assert.Equal(t, "updated", <-events)
	cancel()
	require.Eventually(t, func() bool { return len(done) > 0 }, 30*time.Second, 20*time.Millisecond)
	require.ErrorIs(t, <-done, context.Canceled)
	assert.EqualValues(t, 2, f.uploads.Load())
}

func TestPushRefusesMalformedKnownSourceInsteadOfSkipping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := []byte("synthetic")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), content, 0600))
	sum := sha256.Sum256(content)
	uploaded := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			uploaded = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"known":true,"hash":%q,"size":9,"node":{"id":1,"kind":"file"}}`, hex.EncodeToString(sum[:]))
	}))
	t.Cleanup(ts.Close)
	c, err := daemonconn.NewPushConnection(ts.URL, "synthetic-key")
	require.NoError(t, err)
	report, err := push.Run(t.Context(), c, options(dir, "link"))
	require.ErrorContains(t, err, "invalid push source cursor")
	assert.Zero(t, report.Skipped)
	assert.False(t, uploaded)
}

func TestPushTakesOverDaemonWatchWithZeroUploads(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	watchDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(watchDir, "nested"), 0700))
	watchFile := filepath.Join(watchDir, "nested", "notes.txt")
	require.NoError(t, os.WriteFile(watchFile, []byte("watch initial"), 0600))
	cfg := options(watchDir, "link").Folder
	cfg.Destination = "/watched"
	cfg.SettleTime = config.Duration(time.Second)
	cfg.ScanInterval = config.Duration(100 * time.Millisecond)
	w, err := ingest.NewWatcher(&ingest.Ingester{Store: f.store, Blobs: f.blobs}, f.vaultRoot, cfg,
		func(fn func() error) error { return fn() }, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var node store.Node
	require.Eventually(t, func() bool {
		var lookupErr error
		node, lookupErr = f.store.NodeByPath(t.Context(), "/watched/nested/notes.txt")
		return lookupErr == nil
	}, 30*time.Second, 20*time.Millisecond)
	content := []byte("watch last accepted")
	sum := sha256.Sum256(content)
	require.NoError(t, os.WriteFile(watchFile, content, 0600))
	require.Eventually(t, func() bool {
		var lookupErr error
		node, lookupErr = f.store.NodeByID(t.Context(), node.ID)
		return lookupErr == nil && node.BlobHash == hex.EncodeToString(sum[:])
	}, 30*time.Second, 20*time.Millisecond)
	stop()
	require.Eventually(t, func() bool { return len(done) > 0 }, 30*time.Second, 20*time.Millisecond)
	require.ErrorIs(t, <-done, context.Canceled)

	localDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(localDir, "nested"), 0700))
	localFile := filepath.Join(localDir, "nested", "notes.txt")
	require.NoError(t, os.WriteFile(localFile, content, 0600))
	opts := options(localDir, "link")
	report, err := push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.Zero(t, report.Added)
	assert.Zero(t, report.Linked)
	assert.Zero(t, f.uploads.Load(), "switch-over must upload no unchanged bytes")
	unchanged, err := f.store.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	assert.Equal(t, node.Revision, unchanged.Revision)
	assert.Equal(t, node.CurrentVersionID, unchanged.CurrentVersionID)
	_, err = f.store.NodeByPath(t.Context(), "/archive")
	require.ErrorIs(t, err, store.ErrNotFound, "switch-over must not create a destination or new node")

	require.NoError(t, os.WriteFile(localFile, []byte("push changed"), 0600))
	report, err = push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Updated)
	assert.EqualValues(t, 1, f.uploads.Load())
	changed, err := f.store.NodeByPath(t.Context(), "/watched/nested/notes.txt")
	require.NoError(t, err)
	assert.Equal(t, node.ID, changed.ID)
	assert.NotEqual(t, node.CurrentVersionID, changed.CurrentVersionID)
	_, err = f.store.NodeByPath(t.Context(), "/archive")
	require.ErrorIs(t, err, store.ErrNotFound, "changed identities must keep their existing destination")
	report, err = push.Run(t.Context(), f.connection, opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.EqualValues(t, 1, f.uploads.Load(), "push's accepted hash must replace the old watch cursor for resume")
}
