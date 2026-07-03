package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RuntimeComposeOverrideName = "docker-compose.override.yml"

func (c Context) OverrideEnvironment() string {
	return strings.TrimSpace(c.Environment)
}

func (c Context) TrackedComposeOverrideName() string {
	environment := c.OverrideEnvironment()
	if environment == "" {
		return ""
	}
	return fmt.Sprintf("docker-compose.%s.yml", environment)
}

func (c Context) TrackedComposeOverridePath() string {
	name := c.TrackedComposeOverrideName()
	if name == "" || strings.TrimSpace(c.ProjectDir) == "" {
		return ""
	}
	return filepath.Join(c.ProjectDir, name)
}

func (c Context) RuntimeComposeOverridePath() string {
	if strings.TrimSpace(c.ProjectDir) == "" {
		return ""
	}
	return filepath.Join(c.ProjectDir, RuntimeComposeOverrideName)
}

func (c Context) EnsureTrackedComposeOverrideSymlink() error {
	trackedPath := c.TrackedComposeOverridePath()
	runtimePath := c.RuntimeComposeOverridePath()
	if trackedPath == "" || runtimePath == "" {
		return nil
	}
	if c.DockerHostType == ContextRemote {
		return c.ensureTrackedComposeOverrideSymlinkRemote(trackedPath, runtimePath)
	}

	_, err := os.Stat(trackedPath)
	if err != nil {
		if os.IsNotExist(err) {
			if info, statErr := os.Lstat(runtimePath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return os.Remove(runtimePath)
			}
			return nil
		}
		return err
	}

	if info, err := os.Lstat(runtimePath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", runtimePath)
		}
		target, err := os.Readlink(runtimePath)
		if err != nil {
			return err
		}
		if filepath.Clean(filepath.Join(c.ProjectDir, target)) == filepath.Clean(trackedPath) {
			return nil
		}
		if err := os.Remove(runtimePath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return os.Symlink(filepath.Base(trackedPath), runtimePath)
}

func (c Context) ensureTrackedComposeOverrideSymlinkRemote(trackedPath, runtimePath string) error {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return err
	}
	defer accessor.Close()

	_, err = accessor.sftp.Stat(trackedPath)
	if err != nil {
		if isSFTPNotExist(err) {
			if info, statErr := accessor.sftp.Lstat(runtimePath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return accessor.sftp.Remove(runtimePath)
			} else if statErr != nil && !isSFTPNotExist(statErr) {
				return statErr
			}
			return nil
		}
		return err
	}

	if info, err := accessor.sftp.Lstat(runtimePath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", runtimePath)
		}
		target, err := accessor.sftp.ReadLink(runtimePath)
		if err != nil {
			return err
		}
		if filepath.Clean(filepath.Join(c.ProjectDir, target)) == filepath.Clean(trackedPath) {
			return nil
		}
		if err := accessor.sftp.Remove(runtimePath); err != nil {
			return err
		}
	} else if !isSFTPNotExist(err) {
		return err
	}

	return accessor.sftp.Symlink(filepath.Base(trackedPath), runtimePath)
}

func (c Context) ValidateTrackedComposeOverrideSymlink() error {
	trackedPath := c.TrackedComposeOverridePath()
	runtimePath := c.RuntimeComposeOverridePath()
	if trackedPath == "" || runtimePath == "" {
		return nil
	}
	if c.DockerHostType == ContextRemote {
		return c.validateTrackedComposeOverrideSymlinkRemote(trackedPath, runtimePath)
	}

	_, trackedErr := os.Stat(trackedPath)
	if trackedErr != nil {
		if os.IsNotExist(trackedErr) {
			if _, runtimeErr := os.Lstat(runtimePath); os.IsNotExist(runtimeErr) {
				return nil
			} else if runtimeErr != nil {
				return runtimeErr
			}
			return fmt.Errorf("%s exists but tracked override %s does not", runtimePath, trackedPath)
		}
		return trackedErr
	}

	info, err := os.Lstat(runtimePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s is missing; expected symlink to %s", runtimePath, filepath.Base(trackedPath))
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symlink", runtimePath)
	}
	target, err := os.Readlink(runtimePath)
	if err != nil {
		return err
	}
	if filepath.Clean(filepath.Join(c.ProjectDir, target)) != filepath.Clean(trackedPath) {
		return fmt.Errorf("%s points to %s; expected %s", runtimePath, target, filepath.Base(trackedPath))
	}
	return nil
}

func (c Context) validateTrackedComposeOverrideSymlinkRemote(trackedPath, runtimePath string) error {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return err
	}
	defer accessor.Close()

	_, trackedErr := accessor.sftp.Stat(trackedPath)
	if trackedErr != nil {
		if isSFTPNotExist(trackedErr) {
			if _, runtimeErr := accessor.sftp.Lstat(runtimePath); isSFTPNotExist(runtimeErr) {
				return nil
			} else if runtimeErr != nil {
				return runtimeErr
			}
			return fmt.Errorf("%s exists but tracked override %s does not", runtimePath, trackedPath)
		}
		return trackedErr
	}

	info, err := accessor.sftp.Lstat(runtimePath)
	if err != nil {
		if isSFTPNotExist(err) {
			return fmt.Errorf("%s is missing; expected symlink to %s", runtimePath, filepath.Base(trackedPath))
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symlink", runtimePath)
	}
	target, err := accessor.sftp.ReadLink(runtimePath)
	if err != nil {
		return err
	}
	if filepath.Clean(filepath.Join(c.ProjectDir, target)) != filepath.Clean(trackedPath) {
		return fmt.Errorf("%s points to %s; expected %s", runtimePath, target, filepath.Base(trackedPath))
	}
	return nil
}
