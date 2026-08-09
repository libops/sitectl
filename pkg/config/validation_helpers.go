package config

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const defaultDrupalContainerRoot = "/var/www/drupal"

func IsDockerSocketAlive(socket string) bool {
	return isDockerSocketAlive(socket)
}

// ValidateProjectRegularFile requires filename to be a regular file reached
// without traversing symlinks beneath the project's real root. This keeps
// lifecycle interpreter programs inside the checked-out project on both local
// and remote contexts.
func (c *Context) ValidateProjectRegularFile(projectDir, filename string) error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	projectDir, filename = c.projectFilePaths(projectDir, filename)
	if strings.TrimSpace(projectDir) == "" || strings.TrimSpace(filename) == "" {
		return fmt.Errorf("project directory and file are required")
	}
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return fmt.Errorf("open project files: %w", err)
	}
	defer accessor.Close()
	if c.DockerHostType == ContextRemote {
		return validateRemoteProjectRegularFile(accessor, projectDir, filename)
	}
	return validateLocalProjectRegularFile(accessor, projectDir, filename)
}

// ValidateProjectFileWrite requires filename to remain beneath projectDir,
// with no existing symlink or non-directory parent and no existing symlink or
// non-regular target. Missing parents are allowed because WriteFile creates
// them, but every existing ancestor is resolved beneath the real project root.
func (c *Context) ValidateProjectFileWrite(projectDir, filename string) error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	projectDir, filename = c.projectFilePaths(projectDir, filename)
	if strings.TrimSpace(projectDir) == "" || strings.TrimSpace(filename) == "" {
		return fmt.Errorf("project directory and file are required")
	}
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return fmt.Errorf("open project files: %w", err)
	}
	defer accessor.Close()
	return validateProjectFileWrite(accessor, c.DockerHostType == ContextRemote, projectDir, filename)
}

// WriteProjectFile validates and writes a project-owned file through the same
// local or remote accessor. Callers should use this instead of a lexical join
// followed by WriteFile when the checkout controls any parent component.
func (c *Context) WriteProjectFile(projectDir, filename string, data []byte) error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	projectDir, filename = c.projectFilePaths(projectDir, filename)
	if strings.TrimSpace(projectDir) == "" || strings.TrimSpace(filename) == "" {
		return fmt.Errorf("project directory and file are required")
	}
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return fmt.Errorf("open project files: %w", err)
	}
	defer accessor.Close()
	if err := validateProjectFileWrite(accessor, c.DockerHostType == ContextRemote, projectDir, filename); err != nil {
		return err
	}
	if err := accessor.WriteFile(filename, data); err != nil {
		return fmt.Errorf("write project file %q: %w", filename, err)
	}
	return nil
}

func (c *Context) projectFilePaths(projectDir, filename string) (string, string) {
	if c != nil && c.DockerHostType == ContextRemote {
		normalize := func(value string) string {
			if strings.TrimSpace(value) == "" {
				return ""
			}
			return path.Clean(strings.ReplaceAll(value, `\`, "/"))
		}
		return normalize(projectDir), normalize(filename)
	}
	return projectDir, filename
}

func validateProjectFileWrite(accessor projectFileInspector, remote bool, projectDir, filename string) error {
	if remote {
		return validateRemoteProjectFileWrite(accessor, projectDir, filename)
	}
	return validateLocalProjectFileWrite(accessor, projectDir, filename)
}

type projectFileInspector interface {
	Lstat(string) (os.FileInfo, error)
	RealPath(string) (string, error)
}

func validateLocalProjectFileWrite(accessor projectFileInspector, projectDir, filename string) error {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	target, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("resolve project file: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("project write path %q must name a file beneath the project root", filename)
	}
	rootReal, err := accessor.RealPath(root)
	if err != nil {
		return fmt.Errorf("resolve real project root %q: %w", projectDir, err)
	}
	current := root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := accessor.Lstat(current)
		if isFileNotExistError(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect project write path %q: %w", filename, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project write path %q traverses symlink %q", filename, current)
		}
		final := index == len(parts)-1
		if final && !info.Mode().IsRegular() {
			return fmt.Errorf("project write target %q is not a regular file", filename)
		}
		if !final && !info.IsDir() {
			return fmt.Errorf("project write parent %q is not a directory", current)
		}
		resolved, err := accessor.RealPath(current)
		if err != nil {
			return fmt.Errorf("resolve project write path %q: %w", current, err)
		}
		contained, err := filepath.Rel(rootReal, resolved)
		if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
			return fmt.Errorf("project write path %q resolves outside project root", filename)
		}
	}
	return nil
}

func validateRemoteProjectFileWrite(accessor projectFileInspector, projectDir, filename string) error {
	root := path.Clean(projectDir)
	target := path.Clean(filename)
	relative, ok := remoteProjectRelative(root, target)
	if !ok || relative == "." {
		return fmt.Errorf("project write path %q must name a file beneath the project root", filename)
	}
	rootReal, err := accessor.RealPath(root)
	if err != nil {
		return fmt.Errorf("resolve real project root %q: %w", projectDir, err)
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = path.Join(current, part)
		info, err := accessor.Lstat(current)
		if isFileNotExistError(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect project write path %q: %w", filename, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project write path %q traverses symlink %q", filename, current)
		}
		final := index == len(parts)-1
		if final && !info.Mode().IsRegular() {
			return fmt.Errorf("project write target %q is not a regular file", filename)
		}
		if !final && !info.IsDir() {
			return fmt.Errorf("project write parent %q is not a directory", current)
		}
		resolved, err := accessor.RealPath(current)
		if err != nil {
			return fmt.Errorf("resolve project write path %q: %w", current, err)
		}
		if _, ok := remoteProjectRelative(rootReal, resolved); !ok {
			return fmt.Errorf("project write path %q resolves outside project root", filename)
		}
	}
	return nil
}

func validateLocalProjectRegularFile(accessor projectFileInspector, projectDir, filename string) error {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	target, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("resolve project file: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("project file %q escapes project root", filename)
	}
	rootReal, err := accessor.RealPath(root)
	if err != nil {
		return fmt.Errorf("resolve real project root %q: %w", projectDir, err)
	}
	current := root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := accessor.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect project file %q: %w", filename, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project file %q traverses symlink %q", filename, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("project file parent %q is not a directory", current)
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("project file %q is not a regular file", filename)
		}
	}
	finalInfo, err := accessor.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect project file %q: %w", filename, err)
	}
	if finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.Mode().IsRegular() {
		return fmt.Errorf("project file %q is not a regular non-symlink file", filename)
	}
	targetReal, err := accessor.RealPath(target)
	if err != nil {
		return fmt.Errorf("resolve real project file %q: %w", filename, err)
	}
	realRelative, err := filepath.Rel(rootReal, targetReal)
	if err != nil || realRelative == ".." || strings.HasPrefix(realRelative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("project file %q resolves outside project root", filename)
	}
	return nil
}

func validateRemoteProjectRegularFile(accessor projectFileInspector, projectDir, filename string) error {
	root := path.Clean(projectDir)
	target := path.Clean(filename)
	relative, ok := remoteProjectRelative(root, target)
	if !ok {
		return fmt.Errorf("project file %q escapes project root", filename)
	}
	rootReal, err := accessor.RealPath(root)
	if err != nil {
		return fmt.Errorf("resolve real project root %q: %w", projectDir, err)
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = path.Join(current, part)
		info, err := accessor.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect project file %q: %w", filename, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project file %q traverses symlink %q", filename, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("project file parent %q is not a directory", current)
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("project file %q is not a regular file", filename)
		}
	}
	finalInfo, err := accessor.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect project file %q: %w", filename, err)
	}
	if finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.Mode().IsRegular() {
		return fmt.Errorf("project file %q is not a regular non-symlink file", filename)
	}
	targetReal, err := accessor.RealPath(target)
	if err != nil {
		return fmt.Errorf("resolve real project file %q: %w", filename, err)
	}
	_, ok = remoteProjectRelative(rootReal, targetReal)
	if !ok {
		return fmt.Errorf("project file %q resolves outside project root", filename)
	}
	return nil
}

func remoteProjectRelative(root, target string) (string, bool) {
	root = path.Clean(root)
	target = path.Clean(target)
	if path.IsAbs(root) != path.IsAbs(target) {
		return "", false
	}
	if target == root {
		return ".", true
	}
	prefix := root + "/"
	if root == "/" {
		prefix = "/"
	}
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	return strings.TrimPrefix(target, prefix), true
}

func (c *Context) FileExists(path string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(path) == "" {
		return false, nil
	}

	if c.DockerHostType == ContextLocal {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}

	accessor, err := c.NewFileAccessor()
	if err != nil {
		return false, err
	}
	defer accessor.Close()
	_, err = accessor.Stat(path)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (c *Context) ResolveProjectPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if c != nil && c.DockerHostType == ContextRemote {
		value = strings.ReplaceAll(value, `\`, "/")
		if remotePath := strings.TrimSpace(value); remotePath != "" {
			if path.IsAbs(remotePath) {
				return path.Clean(remotePath)
			}
			return path.Join(strings.ReplaceAll(c.ProjectDir, `\`, "/"), remotePath)
		}
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(c.ProjectDir, value)
}

func (c *Context) EffectiveDrupalRootfs() string {
	if c == nil || strings.TrimSpace(c.DrupalRootfs) == "" {
		return "."
	}
	return strings.TrimSpace(c.DrupalRootfs)
}

func (c *Context) EffectiveDrupalContainerRoot() string {
	if c == nil || strings.TrimSpace(c.DrupalContainerRoot) == "" {
		return defaultDrupalContainerRoot
	}
	return strings.TrimSpace(c.DrupalContainerRoot)
}

func (c *Context) HasComposeProject() (bool, error) {
	if c == nil {
		return false, fmt.Errorf("context is nil")
	}
	for _, candidate := range composeProjectCandidates {
		exists, err := c.FileExists(c.ResolveProjectPath(candidate))
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (c *Context) ValidateComposeAccess() error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	_, err := c.RunQuietCommand(c.composeAccessCommand())
	return err
}

func (c *Context) composeAccessCommand() *exec.Cmd {
	cmdArgs := []string{"compose"}
	for _, file := range c.ComposeFile {
		cmdArgs = append(cmdArgs, "-f", file)
	}
	for _, env := range c.EnvFile {
		cmdArgs = append(cmdArgs, "--env-file", env)
	}
	cmdArgs = append(cmdArgs, "ps")
	command := exec.Command("docker", cmdArgs...) // #nosec G204 -- compose paths are distinct argv entries and are never evaluated by a shell.
	command.Dir = c.ProjectDir
	return command
}
