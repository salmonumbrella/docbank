package processing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
)

func TestRenditionAdmissionBoundsProviderWork(t *testing.T) {
	fixture := newPublicationFixture(t)
	first, second := newWorkerProvider(t), newWorkerProvider(t)
	descriptor := second.descriptor
	descriptor.ID = "synthetic.other-v1"
	var err error
	second.descriptor, err = document.NewRenditionDescriptor(descriptor)
	require.NoError(t, err)
	first.renderStarted, first.renderRelease = make(chan struct{}), make(chan struct{})
	second.renderStarted, second.renderRelease = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-first.renderRelease:
		default:
			close(first.renderRelease)
		}
		select {
		case <-second.renderRelease:
		default:
			close(second.renderRelease)
		}
	})
	registry := NewRenditionRuntimeRegistry()
	require.NoError(t, registry.Register(first.descriptor.Fingerprint, workerRuntime{provider: first}))
	require.NoError(t, registry.Register(second.descriptor.Fingerprint, workerRuntime{provider: second}))
	gate := newTestOperationGate()
	service, err := NewService(t.Context(), ServiceConfig{Catalog: fixture.catalog, Blobs: fixture.blobs, Gate: gate, SpoolDirectory: t.TempDir(), Lifecycle: t.Context(), RenditionRuntimes: registry})
	require.NoError(t, err)
	enqueue := func(provider *workerProvider) (store.RenditionJob, store.RenditionJobWaiter) {
		profile := workerProcessingProfile(t, provider.descriptor)
		request := workerJobRequest(fixture.versionID, profile, provider.descriptor)
		grantWorkerConsent(t, fixture.catalog, request)
		job, waiter, err := fixture.catalog.EnqueueRenditionJob(t.Context(), request)
		require.NoError(t, err)
		return job, waiter
	}
	job1, waiter1 := enqueue(first)
	job2, waiter2 := enqueue(second)
	run := func(ctx context.Context, job, waiter string) <-chan error {
		done := make(chan error, 1)
		go func() { _, err := service.runRenditionJob(ctx, job, waiter); done <- err }()
		return done
	}
	waiters := func(count int) {
		t.Helper()
		require.Eventually(t, func() bool {
			registry.slots.mu.Lock()
			defer registry.slots.mu.Unlock()
			return len(registry.slots.waiters) == count
		}, 10*time.Second, time.Millisecond, "workers must reach the admission queue")
	}
	done1 := run(t.Context(), job1.ID, waiter1.ID)
	select {
	case <-first.renderStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first provider did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done2 := run(ctx, job2.ID, waiter2.ID)
	waiters(1)
	current, err := fixture.catalog.RenditionJobByID(t.Context(), job2.ID)
	require.NoError(t, err)
	require.Equal(t, store.RenditionJobQueued, current.State)
	maintenanceCtx, cancelMaintenance := context.WithTimeout(t.Context(), time.Second)
	defer cancelMaintenance()
	require.NoError(t, gate.MaintainContext(maintenanceCtx, func() error { return nil }))
	cancel()
	require.ErrorIs(t, <-done2, context.Canceled)
	background, err := NewRenditionWorker(RenditionWorkerConfig{Catalog: fixture.catalog, Blobs: fixture.blobs,
		Runtime: registry, Gate: gate, Owner: "synthetic-background", LeaseDuration: time.Minute, IdleDelay: time.Millisecond})
	require.NoError(t, err)
	backgroundCtx, cancelBackground := context.WithCancel(t.Context())
	backgroundDone := make(chan error, 1)
	go func() { _, err := background.RunOne(backgroundCtx); backgroundDone <- err }()
	waiters(1)
	duplicate := run(t.Context(), job1.ID, waiter1.ID)
	waiters(2)
	close(first.renderRelease)
	require.NoError(t, <-done1)
	select {
	case <-second.renderStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("background provider did not start")
	}
	select {
	case err := <-duplicate:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate blocked behind unrelated provider after completion")
	}
	completed := run(t.Context(), job1.ID, waiter1.ID)
	select {
	case err := <-completed:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("completed result blocked behind active provider")
	}
	close(second.renderRelease)
	require.NoError(t, <-backgroundDone)
	cancelBackground()
}

func TestRenditionAdmissionRetryWaitAllowsOtherWork(t *testing.T) {
	fixture := newPublicationFixture(t)
	first, second := newWorkerProvider(t), newWorkerProvider(t)
	descriptor := second.descriptor
	descriptor.ID = "synthetic.other-retry-v1"
	var err error
	second.descriptor, err = document.NewRenditionDescriptor(descriptor)
	require.NoError(t, err)
	first.renderErr, err = document.NewRenditionProviderError(document.RenditionErrorTransient, time.Minute, errors.New("synthetic retry"))
	require.NoError(t, err)
	registry := NewRenditionRuntimeRegistry()
	require.NoError(t, registry.Register(first.descriptor.Fingerprint, workerRuntime{provider: first}))
	require.NoError(t, registry.Register(second.descriptor.Fingerprint, workerRuntime{provider: second}))
	service, err := NewService(t.Context(), ServiceConfig{Catalog: fixture.catalog, Blobs: fixture.blobs, Gate: newTestOperationGate(), SpoolDirectory: t.TempDir(), Lifecycle: t.Context(), RenditionRuntimes: registry})
	require.NoError(t, err)
	enqueue := func(provider *workerProvider) (store.RenditionJob, store.RenditionJobWaiter) {
		request := workerJobRequest(fixture.versionID, workerProcessingProfile(t, provider.descriptor), provider.descriptor)
		grantWorkerConsent(t, fixture.catalog, request)
		job, waiter, err := fixture.catalog.EnqueueRenditionJob(t.Context(), request)
		require.NoError(t, err)
		return job, waiter
	}
	job1, waiter1 := enqueue(first)
	job2, waiter2 := enqueue(second)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := service.runRenditionJob(ctx, job1.ID, waiter1.ID); done <- err }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		job, err := fixture.catalog.RenditionJobByID(t.Context(), job1.ID)
		return err == nil && job.State == store.RenditionJobRetryWait
	}, 10*time.Second, time.Millisecond)
	readyCtx, cancelReady := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelReady()
	_, err = service.runRenditionJob(readyCtx, job2.ID, waiter2.ID)
	require.NoError(t, err)
	current, err := fixture.catalog.RenditionJobByID(t.Context(), job1.ID)
	require.NoError(t, err)
	require.Equal(t, store.RenditionJobRetryWait, current.State)
}
