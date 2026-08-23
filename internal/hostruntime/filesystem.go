package hostruntime

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

func (a Apps) ConvergeFilesystems(account string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("application filesystem convergence must run as root")
	}
	runtimeUser, err := user.Lookup(account)
	if err != nil {
		return fmt.Errorf("look up runtime account %q: %w", account, err)
	}
	uid, err := strconv.Atoi(runtimeUser.Uid)
	if err != nil {
		return fmt.Errorf("parse runtime uid: %w", err)
	}
	gid, err := strconv.Atoi(runtimeUser.Gid)
	if err != nil {
		return fmt.Errorf("parse runtime gid: %w", err)
	}
	for _, name := range a.Manifest.Names() {
		project := a.Manifest[name].ProjectDir
		if err := convergeFilesystemForIDs(project, uid, gid); err != nil {
			return fmt.Errorf("converge %q application filesystem: %w", name, err)
		}
	}
	return nil
}

func convergeFilesystemForIDs(project string, uid, gid int) error {
	projectFD, err := unix.Open(project, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open project directory: %w", err)
	}
	defer unix.Close(projectFD)
	var opened, named unix.Stat_t
	if err := unix.Fstat(projectFD, &opened); err != nil {
		return err
	}
	if err := unix.Lstat(project, &named); err != nil || !sameInode(opened, named) {
		return fmt.Errorf("project path changed while opening")
	}
	if err := unix.Fchmod(projectFD, 0o555); err != nil {
		return err
	}
	if err := unix.Fchown(projectFD, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	if err := unix.Fchmod(projectFD, 0o555); err != nil {
		return err
	}
	if err := unix.Fstat(projectFD, &opened); err != nil || opened.Uid != uint32(os.Geteuid()) || opened.Gid != uint32(os.Getegid()) || opened.Mode&0o7777 != 0o555 {
		return fmt.Errorf("project directory did not freeze safely")
	}
	envFD, err := unix.Openat(projectFD, ".env", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return restoreProjectFilesystem(projectFD, project, uid, gid)
	}
	if err != nil {
		return fmt.Errorf("open application environment: %w", err)
	}
	defer unix.Close(envFD)
	var envOpened, envNamed unix.Stat_t
	if err := unix.Fstat(envFD, &envOpened); err != nil {
		return err
	}
	if envOpened.Mode&unix.S_IFMT != unix.S_IFREG || envOpened.Nlink != 1 {
		return fmt.Errorf("application environment is not a single-link regular file")
	}
	if err := unix.Fstatat(projectFD, ".env", &envNamed, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameInode(envOpened, envNamed) {
		return fmt.Errorf("application environment changed while opening")
	}
	if err := unix.Fchown(envFD, uid, gid); err != nil {
		return err
	}
	if err := unix.Fchmod(envFD, 0o640); err != nil {
		return err
	}
	if err := unix.Fstat(envFD, &envOpened); err != nil || envOpened.Uid != uint32(uid) || envOpened.Gid != uint32(gid) || envOpened.Mode&0o7777 != 0o640 || envOpened.Nlink != 1 {
		return fmt.Errorf("application environment ownership did not converge safely")
	}
	return restoreProjectFilesystem(projectFD, project, uid, gid)
}

func restoreProjectFilesystem(projectFD int, project string, uid, gid int) error {
	if err := unix.Fchown(projectFD, uid, gid); err != nil {
		return err
	}
	if err := unix.Fchmod(projectFD, 0o775); err != nil {
		return err
	}
	var opened, named unix.Stat_t
	if err := unix.Fstat(projectFD, &opened); err != nil {
		return err
	}
	if err := unix.Lstat(project, &named); err != nil || !sameInode(opened, named) || opened.Uid != uint32(uid) || opened.Gid != uint32(gid) || opened.Mode&0o7777 != 0o775 {
		return fmt.Errorf("project ownership did not converge safely")
	}
	return nil
}

func sameInode(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && right.Mode&unix.S_IFMT != unix.S_IFLNK
}
