package config

import (
	"fmt"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// LocalComposeHostNumericIdentity returns POSIX ownership for a local Compose
// host. Native Windows hosts report the capability as unavailable because
// os/user exposes SIDs rather than numeric IDs and Docker bind ownership is not
// represented by a meaningful local UID/GID there.
func LocalComposeHostNumericIdentity() (uid, gid string, available bool, err error) {
	return localComposeHostNumericIdentity(runtime.GOOS, user.Current)
}

func localComposeHostNumericIdentity(goos string, currentUser func() (*user.User, error)) (string, string, bool, error) {
	if goos == "windows" {
		return "", "", false, nil
	}
	current, err := currentUser()
	if err != nil {
		return "", "", false, fmt.Errorf("resolve current user: %w", err)
	}
	if current == nil {
		return "", "", false, fmt.Errorf("resolve current user: no account returned")
	}
	uid := strings.TrimSpace(current.Uid)
	gid := strings.TrimSpace(current.Gid)
	if _, err := strconv.ParseUint(uid, 10, 32); err != nil {
		return "", "", false, fmt.Errorf("invalid host uid %q", uid)
	}
	if _, err := strconv.ParseUint(gid, 10, 32); err != nil {
		return "", "", false, fmt.Errorf("invalid host gid %q", gid)
	}
	return uid, gid, true, nil
}
