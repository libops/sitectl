package config

import (
	"strings"
	"testing"
	"time"
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
	if projectMutationLockPath(alias, true) != projectMutationLockPath(real, true) {
		t.Fatalf("remote aliases produced different lock paths: alias=%q real=%q", alias, real)
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
