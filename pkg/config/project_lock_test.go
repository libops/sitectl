package config

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestCanonicalRemoteProjectMutationLockIdentityCollapsesSymlinkAliases(t *testing.T) {
	t.Parallel()

	inspector := fakeProjectFileInspector{realPaths: map[string]string{
		"/srv/sites/alias": "/mnt/customer/sites/museum",
		"/srv/sites/real":  "/mnt/customer/sites/museum",
	}}
	alias, err := canonicalRemoteProjectMutationLockIdentity(inspector, "/srv/sites/alias")
	if err != nil {
		t.Fatalf("canonicalRemoteProjectMutationLockIdentity(alias) error = %v", err)
	}
	real, err := canonicalRemoteProjectMutationLockIdentity(inspector, "/srv/sites/real")
	if err != nil {
		t.Fatalf("canonicalRemoteProjectMutationLockIdentity(real) error = %v", err)
	}
	if projectMutationLockFilename(alias) != projectMutationLockFilename(real) {
		t.Fatalf("remote aliases produced different lock paths: alias=%q real=%q", alias, real)
	}
}

func TestValidateRemoteProjectMutationLockDirectoryRequiresPrivateOwnerDirectory(t *testing.T) {
	t.Parallel()
	const uid = 1234
	valid := fakeRemoteProjectLockFileInfo{name: "locks", mode: fs.ModeDir | 0o700, stat: &sftp.FileStat{UID: uid}}
	if err := validateRemoteProjectMutationLockDirectory("/home/operator/.sitectl/locks", valid, uid, true); err != nil {
		t.Fatalf("valid remote lock directory rejected: %v", err)
	}
	for name, info := range map[string]fakeRemoteProjectLockFileInfo{
		"wrong owner":    {name: "locks", mode: fs.ModeDir | 0o700, stat: &sftp.FileStat{UID: uid + 1}},
		"shared mode":    {name: "locks", mode: fs.ModeDir | 0o770, stat: &sftp.FileStat{UID: uid}},
		"symlink":        {name: "locks", mode: fs.ModeSymlink | 0o700, stat: &sftp.FileStat{UID: uid}},
		"regular object": {name: "locks", mode: 0o700, stat: &sftp.FileStat{UID: uid}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRemoteProjectMutationLockDirectory("/home/operator/.sitectl/locks", info, uid, true); err == nil {
				t.Fatal("unsafe remote lock directory was accepted")
			}
		})
	}
}

type fakeRemoteProjectLockFileInfo struct {
	name string
	mode fs.FileMode
	stat *sftp.FileStat
}

func (f fakeRemoteProjectLockFileInfo) Name() string       { return f.name }
func (f fakeRemoteProjectLockFileInfo) Size() int64        { return 0 }
func (f fakeRemoteProjectLockFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeRemoteProjectLockFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeRemoteProjectLockFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeRemoteProjectLockFileInfo) Sys() any           { return f.stat }

func TestMonitorRemoteProjectMutationLockCancelsContextWhenProcessExits(t *testing.T) {
	t.Parallel()

	releaseStarted := make(chan struct{})
	releaseState := &atomic.Uint32{}
	waitResult := newProjectMutationWaitResult(func() error {
		return errors.New("ssh channel closed")
	}, releaseState)
	<-waitResult.done
	lossState := newProjectMutationLockLossState()
	lockContext, cancel := context.WithCancelCause(context.Background())
	monitorDone := make(chan struct{})
	go func() {
		monitorRemoteProjectMutationLock(waitResult, releaseStarted, releaseState, lossState, cancel)
		close(monitorDone)
	}()
	<-monitorDone
	if !errors.Is(context.Cause(lockContext), ErrProjectMutationLockLost) {
		t.Fatalf("remote lock context cause = %v, want ErrProjectMutationLockLost", context.Cause(lockContext))
	}
	markedContext := context.WithValue(context.Background(), projectMutationLockLossContextKey{}, lossState)
	if !ProjectMutationLockContextLost(markedContext) {
		t.Fatal("remote lock loss was not retained in the lock context state")
	}
}

func TestRemoteProjectMutationLockExitBeforeReleaseCannotBeReclassifiedByDelayedMonitor(t *testing.T) {
	t.Parallel()

	releaseStarted := make(chan struct{})
	releaseState := &atomic.Uint32{}
	waitResult := newProjectMutationWaitResult(func() error {
		return errors.New("ssh channel closed")
	}, releaseState)
	// Delay monitor scheduling until Wait has completed and Release has begun.
	// The wait goroutine must already have claimed physical loss, so Release
	// cannot overwrite it with the normal-release state.
	<-waitResult.done
	if releaseState.CompareAndSwap(0, 1) {
		t.Fatal("Release reclassified an already-completed lock process as a normal release")
	}
	close(releaseStarted)

	lossState := newProjectMutationLockLossState()
	lockContext, cancel := context.WithCancelCause(context.Background())
	monitorRemoteProjectMutationLock(waitResult, releaseStarted, releaseState, lossState, cancel)
	if !errors.Is(context.Cause(lockContext), ErrProjectMutationLockLost) {
		t.Fatalf("remote lock context cause = %v, want ErrProjectMutationLockLost", context.Cause(lockContext))
	}
	if !lossState.lost.Load() {
		t.Fatal("physical lock loss was reclassified as a normal release")
	}
}

func TestMonitorRemoteProjectMutationLockDoesNotCancelAfterReleaseBegins(t *testing.T) {
	t.Parallel()

	waitResult := &projectMutationWaitResult{done: make(chan struct{})}
	releaseStarted := make(chan struct{})
	releaseState := &atomic.Uint32{}
	lossState := newProjectMutationLockLossState()
	releaseState.Store(1)
	close(releaseStarted)
	lockContext, cancel := context.WithCancelCause(context.Background())
	monitorRemoteProjectMutationLock(waitResult, releaseStarted, releaseState, lossState, cancel)
	if err := lockContext.Err(); err != nil {
		t.Fatalf("released remote lock context was cancelled: %v", err)
	}
	if lossState.lost.Load() {
		t.Fatal("normal remote lock release was marked as lock loss")
	}
}

func TestProjectMutationLockFenceContextIgnoresCallerCancellationButStopsOnLockLoss(t *testing.T) {
	t.Parallel()
	base, cancel := context.WithCancel(context.Background())
	lossState := newProjectMutationLockLossState()
	lockContext := context.WithValue(base, projectMutationLockLossContextKey{}, lossState)
	fenceContext := ProjectMutationLockFenceContext(lockContext)
	cancel()
	if err := fenceContext.Err(); err != nil {
		t.Fatalf("fence context followed ordinary caller cancellation: %v", err)
	}
	lossState.markLost()
	select {
	case <-fenceContext.Done():
	default:
		t.Fatal("fence context did not stop after physical lock loss")
	}
	if err := fenceContext.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("fence context error = %v, want cancellation after lock loss", err)
	}
}

func TestWaitForRemoteProjectLockReleaseForcesClosedStuckSession(t *testing.T) {
	t.Parallel()

	closed := make(chan struct{})
	forced := make(chan struct{}, 1)
	err := waitForRemoteProjectLockRelease(
		func() error {
			<-closed
			return nil
		},
		func() {
			forced <- struct{}{}
			close(closed)
		},
		10*time.Millisecond,
		10*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "forcibly closed") {
		t.Fatalf("waitForRemoteProjectLockRelease() error = %v, want forced-close diagnostic", err)
	}
	select {
	case <-forced:
	default:
		t.Fatal("remote project lock session was not forcibly closed")
	}
}
