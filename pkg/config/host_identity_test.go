package config

import (
	"errors"
	"os/user"
	"testing"
)

func TestLocalComposeHostNumericIdentityRepresentsWindowsCapabilityAbsence(t *testing.T) {
	called := false
	uid, gid, available, err := localComposeHostNumericIdentity("windows", func() (*user.User, error) {
		called = true
		return nil, errors.New("must not inspect a Windows SID")
	})
	if err != nil {
		t.Fatalf("localComposeHostNumericIdentity(windows) error = %v", err)
	}
	if called || available || uid != "" || gid != "" {
		t.Fatalf("Windows identity = uid %q gid %q available %t called %t, want unavailable without os/user lookup", uid, gid, available, called)
	}
}

func TestLocalComposeHostNumericIdentityValidatesPOSIXNumbers(t *testing.T) {
	uid, gid, available, err := localComposeHostNumericIdentity("linux", func() (*user.User, error) {
		return &user.User{Uid: " 1000 ", Gid: "1001"}, nil
	})
	if err != nil {
		t.Fatalf("localComposeHostNumericIdentity(linux) error = %v", err)
	}
	if !available || uid != "1000" || gid != "1001" {
		t.Fatalf("POSIX identity = uid %q gid %q available %t", uid, gid, available)
	}
}
