package processing

import (
	"context"
	"errors"
	"fmt"
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

func TestRenditionAdmissionPreservesFIFOAcrossStatusChecks(t *testing.T) {
	var admission renditionAdmission
	require.NoError(t, admission.acquire(t.Context(), nil))
	polls := make(chan struct{}, 4)
	bGranted := make(chan error, 1)
	go func() {
		bGranted <- admission.acquire(t.Context(), func(context.Context) (bool, error) {
			select {
			case polls <- struct{}{}:
			default:
			}
			return false, nil
		})
	}()
	// Each observation proves B's original acquisition has passed a poll.
	for range 2 {
		select {
		case <-polls:
		case <-time.After(5 * time.Second):
			t.Fatal("B did not poll")
		}
	}
	admission.mu.Lock()
	var first *renditionAdmissionWaiter
	if len(admission.waiters) == 1 {
		first = admission.waiters[0]
	}
	admission.mu.Unlock()
	require.NotNil(t, first)
	cGranted := make(chan error, 1)
	go func() { cGranted <- admission.acquire(t.Context(), nil) }()
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == 2 && admission.waiters[0] == first
	}, 5*time.Second, time.Millisecond)
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("B did not poll after C queued")
	}
	admission.mu.Lock()
	unchanged := len(admission.waiters) == 2 && admission.waiters[0] == first
	admission.mu.Unlock()
	require.True(t, unchanged, "B retained its position after C queued")
	admission.release()
	select {
	case err := <-bGranted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("B did not receive first slot")
	}
	select {
	case <-cGranted:
		t.Fatal("C overtook B")
	default:
	}
	admission.release()
	select {
	case err := <-cGranted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("C did not receive slot")
	}
	admission.release()
}

func TestRenditionAdmissionSettledDuringGrantReleasesSlot(t *testing.T) {
	var admission renditionAdmission
	require.NoError(t, admission.acquire(t.Context(), nil))
	entered, resume := make(chan struct{}), make(chan struct{})
	bDone := make(chan error, 1)
	go func() {
		bDone <- admission.acquire(t.Context(), func(context.Context) (bool, error) {
			close(entered)
			<-resume
			return true, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("status check did not start")
	}
	cDone := make(chan error, 1)
	go func() { cDone <- admission.acquire(t.Context(), nil) }()
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == 2
	}, 5*time.Second, time.Millisecond)
	admission.release() // Grant B while its terminal-state check is in flight.
	close(resume)
	select {
	case err := <-bDone:
		require.ErrorIs(t, err, errRenditionAdmissionSettled)
	case <-time.After(5 * time.Second):
		t.Fatal("settled waiter did not return")
	}
	select {
	case err := <-cDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("settled waiter leaked its grant")
	}
	admission.release()
}

func TestRenditionAdmissionTargetedFIFOAcrossPolls(t *testing.T) {
	fixture := newPublicationFixture(t)
	providers := []*workerProvider{newWorkerProvider(t), newWorkerProvider(t), newWorkerProvider(t)}
	registry := NewRenditionRuntimeRegistry()
	for i, provider := range providers {
		if i > 0 {
			descriptor := provider.descriptor
			descriptor.ID = fmt.Sprintf("synthetic.fifo-%d-v1", i)
			var err error
			provider.descriptor, err = document.NewRenditionDescriptor(descriptor)
			require.NoError(t, err)
		}
		provider.renderStarted, provider.renderRelease = make(chan struct{}), make(chan struct{})
		p := provider
		t.Cleanup(func() {
			select {
			case <-p.renderRelease:
			default:
				close(p.renderRelease)
			}
		})
		require.NoError(t, registry.Register(provider.descriptor.Fingerprint, workerRuntime{provider: provider}))
	}
	service, err := NewService(t.Context(), ServiceConfig{Catalog: fixture.catalog, Blobs: fixture.blobs, Gate: newTestOperationGate(), SpoolDirectory: t.TempDir(), Lifecycle: t.Context(), RenditionRuntimes: registry})
	require.NoError(t, err)
	var jobs [3]store.RenditionJob
	var waiters [3]store.RenditionJobWaiter
	for i, provider := range providers {
		request := workerJobRequest(fixture.versionID, workerProcessingProfile(t, provider.descriptor), provider.descriptor)
		grantWorkerConsent(t, fixture.catalog, request)
		jobs[i], waiters[i], err = fixture.catalog.EnqueueRenditionJob(t.Context(), request)
		require.NoError(t, err)
	}
	run := func(i int) <-chan error {
		done := make(chan error, 1)
		go func() { _, err := service.runRenditionJob(t.Context(), jobs[i].ID, waiters[i].ID); done <- err }()
		return done
	}
	waitCount := func(n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			registry.slots.mu.Lock()
			defer registry.slots.mu.Unlock()
			return len(registry.slots.waiters) == n
		}, 5*time.Second, time.Millisecond)
	}
	waitStarted := func(i int) {
		t.Helper()
		select {
		case <-providers[i].renderStarted:
		case <-time.After(5 * time.Second):
			t.Fatalf("provider %d did not start", i)
		}
	}
	a := run(0)
	waitStarted(0)
	b := run(1)
	waitCount(1)
	registry.slots.mu.Lock()
	bPosition := registry.slots.waiters[0]
	registry.slots.mu.Unlock()
	// The lower-level test observes individual polls; this checks the service path
	// retains the same entry for several real polling intervals.
	time.Sleep(2*renditionAdmissionPollInterval + 50*time.Millisecond)
	registry.slots.mu.Lock()
	unchanged := len(registry.slots.waiters) == 1 && registry.slots.waiters[0] == bPosition
	registry.slots.mu.Unlock()
	require.True(t, unchanged, "B lost its position across status checks")
	c := run(2)
	waitCount(2)
	duplicate := run(0)
	waitCount(3)
	close(providers[0].renderRelease)
	require.NoError(t, <-a)
	waitStarted(1)
	select {
	case <-providers[2].renderStarted:
		t.Fatal("C overtook B")
	default:
	}
	select {
	case err := <-duplicate:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate A did not return while B ran")
	}
	close(providers[1].renderRelease)
	require.NoError(t, <-b)
	waitStarted(2)
	close(providers[2].renderRelease)
	require.NoError(t, <-c)
}
