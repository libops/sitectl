package plugin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	yaml "gopkg.in/yaml.v3"
)

const (
	templateLockAPIVersion        = "sitectl.libops.io/v1alpha1"
	templateLockKind              = "TemplateLock"
	templateLockSchema            = 1
	templateContractKind          = "TemplateContract"
	templateContractPath          = ".libops/template-contract.yaml"
	templateLockPath              = ".libops/template.lock.yaml"
	componentDefaultsRevisionPath = ".libops/component-defaults.revision"
	maxTemplateContractBytes      = 1 << 20
	maxComponentRevisionBytes     = 512
	hostVersionEnvironment        = "SITECTL_HOST_VERSION"
	hostRevisionEnvironment       = "SITECTL_HOST_REVISION"
)

var (
	semverPattern            = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	buildRevisionPattern     = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	templateCommitPattern    = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
	pluginNamePattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	sitectlPackagePattern    = regexp.MustCompile(`^sitectl-[A-Za-z0-9][A-Za-z0-9.+_-]*$`)
	componentRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,199}$`)
)

type templateLock struct {
	APIVersion        string                         `yaml:"apiVersion"`
	Kind              string                         `yaml:"kind"`
	Schema            int                            `yaml:"schema"`
	Template          templateLockSource             `yaml:"template"`
	Sitectl           *templateLockPackage           `yaml:"sitectl,omitempty"`
	Plugins           []templateLockPackage          `yaml:"plugins,omitempty"`
	ComponentDefaults *templateLockComponentDefaults `yaml:"componentDefaults,omitempty"`
}

type templateLockSource struct {
	Repository string                `yaml:"repository"`
	Commit     string                `yaml:"commit"`
	Contract   *templateLockContract `yaml:"contract,omitempty"`
}

type templateLockContract struct {
	Path   string `yaml:"path"`
	Digest string `yaml:"digest"`
}

type templateLockPackage struct {
	Package  string `yaml:"package,omitempty"`
	Version  string `yaml:"version,omitempty"`
	Revision string `yaml:"revision,omitempty"`
}

type templateLockComponentDefaults struct {
	Revision string `yaml:"revision"`
}

type templateContract struct {
	APIVersion string               `yaml:"apiVersion"`
	Kind       string               `yaml:"kind"`
	Schema     int                  `yaml:"schema"`
	Spec       templateContractSpec `yaml:"spec,omitempty"`
}

type templateContractSpec struct {
	ComponentDefaults templateContractComponentDefaults `yaml:"componentDefaults,omitempty"`
}

type templateContractComponentDefaults struct {
	Revision string `yaml:"revision,omitempty"`
}

type templateCheckoutMetadata struct {
	Commit                    string
	Contract                  []byte
	ComponentDefaultsRevision string
}

type buildIdentity struct {
	Version  string
	Revision string
}

var hostBuildInfo = struct {
	sync.RWMutex
	version  string
	revision string
}{}

// SetHostBuildInfo records the sitectl host build embedded into plugin RPC
// subprocesses. Unknown or malformed development values are omitted from the
// generated template lock rather than retained as misleading provenance.
func SetHostBuildInfo(version, revision string) {
	identity := parseBuildIdentity(version, revision)
	hostBuildInfo.Lock()
	hostBuildInfo.version = identity.Version
	hostBuildInfo.revision = identity.Revision
	hostBuildInfo.Unlock()
}

func hostBuildEnvironment() []string {
	hostBuildInfo.RLock()
	defer hostBuildInfo.RUnlock()
	env := make([]string, 0, 2)
	if hostBuildInfo.version != "" {
		env = append(env, hostVersionEnvironment+"="+hostBuildInfo.version)
	}
	if hostBuildInfo.revision != "" {
		env = append(env, hostRevisionEnvironment+"="+hostBuildInfo.revision)
	}
	return env
}

func filterHostBuildEnvironment(env []string) []string {
	filtered := make([]string, 0, len(env)+2)
	for _, value := range env {
		if strings.HasPrefix(value, hostVersionEnvironment+"=") || strings.HasPrefix(value, hostRevisionEnvironment+"=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return append(filtered, hostBuildEnvironment()...)
}

func (s *SDK) templateLockPackages() (*templateLockPackage, []templateLockPackage) {
	host := parseBuildIdentity(os.Getenv(hostVersionEnvironment), os.Getenv(hostRevisionEnvironment))
	var sitectl *templateLockPackage
	if host.Version != "" || host.Revision != "" {
		sitectl = &templateLockPackage{Version: host.Version, Revision: host.Revision}
	}

	plugins := make([]templateLockPackage, 0, len(s.Metadata.Includes)+1)
	seen := map[string]struct{}{}
	appendPlugin := func(name, version string) {
		name = strings.TrimSpace(name)
		if !pluginNamePattern.MatchString(name) {
			return
		}
		packageName := "sitectl-" + name
		if _, exists := seen[packageName]; exists {
			return
		}
		seen[packageName] = struct{}{}
		identity := parseFormattedBuildIdentity(version)
		plugins = append(plugins, templateLockPackage{
			Package:  packageName,
			Version:  identity.Version,
			Revision: identity.Revision,
		})
	}
	appendPlugin(s.Metadata.Name, s.Metadata.Version)
	includes := append([]string(nil), s.Metadata.Includes...)
	includes = append(includes, builtinPluginIncludes[s.Metadata.Name]...)
	for _, include := range includes {
		if installed, ok := FindInstalled(include); ok {
			appendPlugin(installed.Name, installed.Version)
		}
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Package < plugins[j].Package })
	return sitectl, plugins
}

func parseFormattedBuildIdentity(formatted string) buildIdentity {
	formatted = strings.TrimSpace(formatted)
	if formatted == "" {
		return buildIdentity{}
	}
	version := formatted
	revision := ""
	const marker = " (Built on "
	if markerIndex := strings.Index(formatted, marker); markerIndex >= 0 {
		version = formatted[:markerIndex]
		suffix := formatted[markerIndex+len(marker):]
		const revisionMarker = " from Git SHA "
		revisionIndex := strings.LastIndex(suffix, revisionMarker)
		if revisionIndex < 0 || !strings.HasSuffix(suffix, ")") {
			return buildIdentity{}
		}
		revision = strings.TrimSuffix(suffix[revisionIndex+len(revisionMarker):], ")")
	}
	return parseBuildIdentity(version, revision)
}

func parseBuildIdentity(version, revision string) buildIdentity {
	identity := buildIdentity{}
	version = strings.TrimSpace(version)
	if semverPattern.MatchString(version) {
		identity.Version = version
	}
	revision = strings.TrimSpace(revision)
	if buildRevisionPattern.MatchString(revision) {
		identity.Revision = strings.ToLower(revision)
	}
	return identity
}

func validateTemplateRepository(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", fmt.Errorf("template repo cannot be empty")
	}
	if len(repository) > 4096 || !utf8.ValidString(repository) || strings.IndexFunc(repository, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("template repo contains invalid characters")
	}
	if strings.ContainsAny(repository, "?#") {
		return "", fmt.Errorf("template repo must not contain a query or fragment")
	}
	if !strings.Contains(repository, "://") {
		if at := strings.LastIndex(repository, "@"); at >= 0 && strings.Contains(repository[:at], ":") {
			return "", fmt.Errorf("template repo must not contain inline credentials; use an SSH agent")
		}
	}
	if parsed, err := url.Parse(repository); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			return "", fmt.Errorf("template repo must not contain inline credentials; use a credential helper or SSH agent")
		}
	} else if at := strings.LastIndex(repository, "@"); at >= 0 {
		identity := repository[:at]
		if strings.Contains(identity, ":") {
			return "", fmt.Errorf("template repo must not contain inline credentials; use an SSH agent")
		}
	}
	return repository, nil
}

func resolveTemplateCommitWithRunner(projectDir string, runner gitRunner) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runner(&stdout, &stderr, "git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return "", fmt.Errorf("resolve cloned template commit: %w", err)
	}
	commit := strings.TrimSpace(stdout.String())
	if !templateCommitPattern.MatchString(commit) {
		return "", fmt.Errorf("resolve cloned template commit: git returned an invalid object id")
	}
	return strings.ToLower(commit), nil
}

func inspectLocalTemplateCheckoutWithRunner(projectDir string, runner gitRunner) (templateCheckoutMetadata, error) {
	commit, err := resolveTemplateCommitWithRunner(projectDir, runner)
	if err != nil {
		return templateCheckoutMetadata{}, err
	}
	metadata := templateCheckoutMetadata{Commit: commit}
	projectRoot, err := os.OpenRoot(projectDir)
	if err != nil {
		return templateCheckoutMetadata{}, fmt.Errorf("open template checkout: %w", err)
	}
	defer projectRoot.Close()
	info, err := projectRoot.Lstat(".libops")
	if err != nil {
		if os.IsNotExist(err) {
			return metadata, nil
		}
		return templateCheckoutMetadata{}, fmt.Errorf("inspect template metadata directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return templateCheckoutMetadata{}, fmt.Errorf("template metadata path %q must be a real directory, not a symlink or other file", ".libops")
	}
	_, lockErr := projectRoot.Lstat(filepath.FromSlash(templateLockPath))
	if lockErr == nil {
		return templateCheckoutMetadata{}, fmt.Errorf("source template must not contain %q; sitectl creates that downstream provenance file", templateLockPath)
	}
	if lockErr != nil && !os.IsNotExist(lockErr) {
		return templateCheckoutMetadata{}, fmt.Errorf("inspect %s: %w", templateLockPath, lockErr)
	}

	contract, present, err := readSafeTemplateMetadataFile(projectRoot, templateContractPath, maxTemplateContractBytes)
	if err != nil {
		return templateCheckoutMetadata{}, err
	}
	if present {
		metadata.Contract = contract
		metadata.ComponentDefaultsRevision, err = validateTemplateContract(contract)
		if err != nil {
			return templateCheckoutMetadata{}, err
		}
	}
	revisionBytes, present, err := readSafeTemplateMetadataFile(projectRoot, componentDefaultsRevisionPath, maxComponentRevisionBytes)
	if err != nil {
		return templateCheckoutMetadata{}, err
	}
	if present {
		revision, revisionErr := validateComponentDefaultsRevision(string(revisionBytes))
		if revisionErr != nil {
			return templateCheckoutMetadata{}, fmt.Errorf("validate %s: %w", componentDefaultsRevisionPath, revisionErr)
		}
		if metadata.ComponentDefaultsRevision != "" && metadata.ComponentDefaultsRevision != revision {
			return templateCheckoutMetadata{}, fmt.Errorf("component defaults revision differs between %s and %s", templateContractPath, componentDefaultsRevisionPath)
		}
		metadata.ComponentDefaultsRevision = revision
	}
	return metadata, nil
}

func readSafeTemplateMetadataFile(projectRoot *os.Root, relativePath string, maximum int64) ([]byte, bool, error) {
	rootPath := filepath.FromSlash(relativePath)
	info, err := projectRoot.Lstat(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect %s: %w", relativePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("template metadata path %q must be a regular file, not a symlink or other file", relativePath)
	}
	if info.Size() > maximum {
		return nil, false, fmt.Errorf("template metadata path %q exceeds %d bytes", relativePath, maximum)
	}
	data, err := projectRoot.ReadFile(rootPath)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", relativePath, err)
	}
	if int64(len(data)) > maximum {
		return nil, false, fmt.Errorf("template metadata path %q exceeds %d bytes", relativePath, maximum)
	}
	return data, true, nil
}

func validateTemplateContract(data []byte) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var contract templateContract
	if err := decoder.Decode(&contract); err != nil {
		return "", fmt.Errorf("parse %s: %w", templateContractPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", fmt.Errorf("parse %s: multiple YAML documents are not allowed", templateContractPath)
		}
		return "", fmt.Errorf("parse %s: %w", templateContractPath, err)
	}
	if contract.APIVersion != templateLockAPIVersion || contract.Kind != templateContractKind || contract.Schema != templateLockSchema {
		return "", fmt.Errorf("validate %s: expected apiVersion %q, kind %q, and schema %d", templateContractPath, templateLockAPIVersion, templateContractKind, templateLockSchema)
	}
	if strings.TrimSpace(contract.Spec.ComponentDefaults.Revision) == "" {
		return "", nil
	}
	revision, err := validateComponentDefaultsRevision(contract.Spec.ComponentDefaults.Revision)
	if err != nil {
		return "", fmt.Errorf("validate %s component defaults revision: %w", templateContractPath, err)
	}
	return revision, nil
}

func validateComponentDefaultsRevision(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !componentRevisionPattern.MatchString(value) || strings.Contains(value, "..") {
		return "", fmt.Errorf("revision must be a single safe version or object identifier")
	}
	return value, nil
}

func buildTemplateLock(repository string, metadata templateCheckoutMetadata, sitectl *templateLockPackage, plugins []templateLockPackage) ([]byte, error) {
	repository, err := validateTemplateRepository(repository)
	if err != nil {
		return nil, err
	}
	if !templateCommitPattern.MatchString(metadata.Commit) {
		return nil, fmt.Errorf("template commit must be a full Git object id")
	}
	if len(metadata.Contract) > maxTemplateContractBytes {
		return nil, fmt.Errorf("template contract exceeds %d bytes", maxTemplateContractBytes)
	}
	if len(metadata.Contract) > 0 {
		contractRevision, contractErr := validateTemplateContract(metadata.Contract)
		if contractErr != nil {
			return nil, contractErr
		}
		if metadata.ComponentDefaultsRevision != "" && contractRevision != "" && metadata.ComponentDefaultsRevision != contractRevision {
			return nil, fmt.Errorf("component defaults revision differs from %s", templateContractPath)
		}
		if metadata.ComponentDefaultsRevision == "" {
			metadata.ComponentDefaultsRevision = contractRevision
		}
	}
	if metadata.ComponentDefaultsRevision != "" {
		metadata.ComponentDefaultsRevision, err = validateComponentDefaultsRevision(metadata.ComponentDefaultsRevision)
		if err != nil {
			return nil, fmt.Errorf("validate component defaults revision: %w", err)
		}
	}
	lock := templateLock{
		APIVersion: templateLockAPIVersion,
		Kind:       templateLockKind,
		Schema:     templateLockSchema,
		Template: templateLockSource{
			Repository: repository,
			Commit:     strings.ToLower(metadata.Commit),
		},
	}
	if sitectl != nil {
		identity := parseBuildIdentity(sitectl.Version, sitectl.Revision)
		if identity.Version != "" || identity.Revision != "" {
			lock.Sitectl = &templateLockPackage{Version: identity.Version, Revision: identity.Revision}
		}
	}
	seenPackages := map[string]struct{}{}
	for _, candidate := range plugins {
		if !sitectlPackagePattern.MatchString(candidate.Package) {
			return nil, fmt.Errorf("plugin package %q is invalid", candidate.Package)
		}
		if _, duplicate := seenPackages[candidate.Package]; duplicate {
			return nil, fmt.Errorf("plugin package %q is duplicated", candidate.Package)
		}
		seenPackages[candidate.Package] = struct{}{}
		identity := parseBuildIdentity(candidate.Version, candidate.Revision)
		lock.Plugins = append(lock.Plugins, templateLockPackage{
			Package:  candidate.Package,
			Version:  identity.Version,
			Revision: identity.Revision,
		})
	}
	if len(metadata.Contract) > 0 {
		digest := sha256.Sum256(metadata.Contract)
		lock.Template.Contract = &templateLockContract{
			Path:   templateContractPath,
			Digest: "sha256:" + hex.EncodeToString(digest[:]),
		}
	}
	if metadata.ComponentDefaultsRevision != "" {
		lock.ComponentDefaults = &templateLockComponentDefaults{Revision: metadata.ComponentDefaultsRevision}
	}
	sort.Slice(lock.Plugins, func(i, j int) bool { return lock.Plugins[i].Package < lock.Plugins[j].Package })
	data, err := yaml.Marshal(lock)
	if err != nil {
		return nil, fmt.Errorf("marshal template lock: %w", err)
	}
	return data, nil
}

func writeTemplateLockAtomic(projectDir string, data []byte) error {
	projectRoot, err := os.OpenRoot(projectDir)
	if err != nil {
		return fmt.Errorf("open template checkout for lock: %w", err)
	}
	defer projectRoot.Close()
	info, err := projectRoot.Lstat(".libops")
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect template lock directory: %w", err)
		}
		if err := projectRoot.Mkdir(".libops", 0o750); err != nil {
			return fmt.Errorf("create template lock directory: %w", err)
		}
		info, err = projectRoot.Lstat(".libops")
		if err != nil {
			return fmt.Errorf("inspect created template lock directory: %w", err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("template lock directory must be a real directory, not a symlink or other file")
	}
	libopsRoot, err := projectRoot.OpenRoot(".libops")
	if err != nil {
		return fmt.Errorf("open template lock directory: %w", err)
	}
	defer libopsRoot.Close()
	temporary, temporaryName, err := createTemplateLockTemporary(libopsRoot)
	if err != nil {
		return fmt.Errorf("create temporary template lock: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = libopsRoot.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set template lock permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write template lock: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync template lock: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close template lock: %w", err)
	}
	if err := libopsRoot.Rename(temporaryName, "template.lock.yaml"); err != nil {
		return fmt.Errorf("replace template lock: %w", err)
	}
	removeTemporary = false
	directory, err := libopsRoot.Open(".")
	if err != nil {
		return fmt.Errorf("open template lock directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync template lock directory: %w", err)
	}
	return nil
}

func createTemplateLockTemporary(root *os.Root) (*os.File, string, error) {
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := ".template.lock.yaml.tmp-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("exhausted temporary lock name attempts")
}
