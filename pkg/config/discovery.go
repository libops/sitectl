package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	yaml "gopkg.in/yaml.v3"
)

var composeProjectCandidates = []string{
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

func LooksLikeComposeProject(projectDir string) bool {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return false
	}
	for _, name := range composeProjectCandidates {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err == nil {
			return true
		}
	}
	return false
}

func DetectComposeProjectName(projectDir string) string {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return ""
	}

	envPath := filepath.Join(projectDir, ".env")
	if values, err := godotenv.Read(envPath); err == nil {
		if value := strings.TrimSpace(values["COMPOSE_PROJECT_NAME"]); value != "" {
			return value
		}
	}

	for _, name := range composeProjectCandidates {
		path := filepath.Join(projectDir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- projectDir is the caller-selected compose project root.
		if err != nil {
			continue
		}
		var doc struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			continue
		}
		if value := strings.TrimSpace(doc.Name); value != "" {
			return value
		}
	}

	return ""
}

type composeDiscoveryDoc struct {
	Name     string                             `yaml:"name"`
	Services map[string]composeDiscoveryService `yaml:"services"`
	Networks map[string]composeDiscoveryNetwork `yaml:"networks"`
}

type composeDiscoveryService struct {
	Networks any `yaml:"networks"`
}

type composeDiscoveryNetwork struct {
	Name string `yaml:"name"`
}

func DetectComposeNetworkName(projectDir, composeProjectName string) string {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	composeProjectName = strings.TrimSpace(composeProjectName)
	if projectDir == "" || composeProjectName == "" {
		return ""
	}

	doc, ok := readComposeDiscoveryDoc(projectDir)
	if !ok {
		return composeProjectName + "_default"
	}
	return preferredComposeNetworkName(doc, composeProjectName)
}

func FindComposeProjectRoot(startDir string) string {
	startDir = filepath.Clean(strings.TrimSpace(startDir))
	if startDir == "" {
		return ""
	}
	if resolved, err := filepath.Abs(startDir); err == nil {
		startDir = resolved
	}
	for {
		if LooksLikeComposeProject(startDir) {
			return startDir
		}
		parent := filepath.Dir(startDir)
		if parent == startDir {
			return ""
		}
		startDir = parent
	}
}

func DetectComposeServices(projectDir string) []string {
	doc, ok := readComposeDiscoveryDoc(projectDir)
	if !ok {
		return nil
	}
	services := make([]string, 0, len(doc.Services))
	for service := range doc.Services {
		service = strings.TrimSpace(service)
		if service != "" {
			services = append(services, service)
		}
	}
	sort.Strings(services)
	return services
}

func DetectContextComposeNetwork(ctx *Context) string {
	if ctx == nil {
		return ""
	}
	composeProjectName := ctx.EffectiveComposeProjectName()
	if composeProjectName == "" {
		return ""
	}
	doc, ok := readComposeDiscoveryDocForContext(ctx)
	if !ok {
		return ctx.EffectiveComposeNetwork()
	}
	return preferredComposeNetworkName(doc, composeProjectName)
}

func readComposeDiscoveryDoc(projectDir string) (composeDiscoveryDoc, bool) {
	for _, name := range composeProjectCandidates {
		path := filepath.Join(projectDir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- projectDir is the caller-selected compose project root.
		if err != nil {
			continue
		}
		var doc composeDiscoveryDoc
		if err := yaml.Unmarshal(data, &doc); err != nil {
			continue
		}
		return doc, true
	}
	return composeDiscoveryDoc{}, false
}

func readComposeDiscoveryDocForContext(ctx *Context) (composeDiscoveryDoc, bool) {
	if ctx == nil {
		return composeDiscoveryDoc{}, false
	}
	files := ctx.ComposeFile
	if len(files) == 0 {
		files = composeProjectCandidates
	}
	for _, name := range files {
		path := ctx.ResolveProjectPath(name)
		exists, err := ctx.FileExists(path)
		if err != nil || !exists {
			continue
		}
		data, err := ctx.ReadSmallFile(path)
		if err != nil || strings.TrimSpace(data) == "" {
			continue
		}
		var doc composeDiscoveryDoc
		if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
			continue
		}
		return doc, true
	}
	return composeDiscoveryDoc{}, false
}

func preferredComposeNetworkName(doc composeDiscoveryDoc, composeProjectName string) string {
	if network, ok := doc.Networks["default"]; ok {
		if value := strings.TrimSpace(network.Name); value != "" {
			return value
		}
		return composeProjectName + "_default"
	}

	if usesImplicitDefaultNetwork(doc.Services) {
		return composeProjectName + "_default"
	}

	common := commonServiceNetwork(doc.Services)
	if common != "" {
		return effectiveComposeNetworkName(common, doc.Networks[common], composeProjectName)
	}

	keys := sortedComposeNetworkKeys(doc.Networks)
	if len(keys) == 1 {
		key := keys[0]
		return effectiveComposeNetworkName(key, doc.Networks[key], composeProjectName)
	}

	if len(keys) > 0 {
		key := keys[0]
		return effectiveComposeNetworkName(key, doc.Networks[key], composeProjectName)
	}

	return composeProjectName + "_default"
}

func usesImplicitDefaultNetwork(services map[string]composeDiscoveryService) bool {
	if len(services) == 0 {
		return true
	}
	for _, service := range services {
		if len(serviceNetworkKeys(service.Networks)) == 0 {
			return true
		}
	}
	return false
}

func commonServiceNetwork(services map[string]composeDiscoveryService) string {
	if len(services) == 0 {
		return ""
	}
	counts := map[string]int{}
	total := 0
	for _, service := range services {
		keys := serviceNetworkKeys(service.Networks)
		if len(keys) == 0 {
			return ""
		}
		total++
		for _, key := range keys {
			counts[key]++
		}
	}
	common := ""
	for key, count := range counts {
		if count == total && (common == "" || key < common) {
			common = key
		}
	}
	return common
}

func serviceNetworkKeys(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		keys := make([]string, 0, len(typed))
		for _, item := range typed {
			if name := strings.TrimSpace(fmt.Sprint(item)); name != "" {
				keys = append(keys, name)
			}
		}
		sort.Strings(keys)
		return keys
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if value := strings.TrimSpace(key); value != "" {
				keys = append(keys, value)
			}
		}
		sort.Strings(keys)
		return keys
	default:
		return nil
	}
}

func sortedComposeNetworkKeys(networks map[string]composeDiscoveryNetwork) []string {
	keys := make([]string, 0, len(networks))
	for key := range networks {
		if value := strings.TrimSpace(key); value != "" {
			keys = append(keys, value)
		}
	}
	sort.Strings(keys)
	return keys
}

func effectiveComposeNetworkName(key string, network composeDiscoveryNetwork, composeProjectName string) string {
	if value := strings.TrimSpace(network.Name); value != "" {
		return value
	}
	if key == "default" || strings.TrimSpace(key) == "" {
		return composeProjectName + "_default"
	}
	return composeProjectName + "_" + key
}

func FindLocalContextByProjectDir(projectDir string) (*Context, error) {
	return FindLocalContextByProjectDirAndPlugin(projectDir, "")
}

func FindLocalContextByProjectDirAndPlugin(projectDir, pluginName string) (*Context, error) {
	projectDir = canonicalProjectDir(projectDir)
	pluginName = strings.TrimSpace(pluginName)
	if projectDir == "" {
		return nil, nil
	}
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	for i := range cfg.Contexts {
		ctx := &cfg.Contexts[i]
		if ctx.DockerHostType != ContextLocal {
			continue
		}
		storedDir := canonicalProjectDir(ctx.ProjectDir)
		if storedDir == projectDir {
			if pluginName != "" && !strings.EqualFold(strings.TrimSpace(ctx.Plugin), pluginName) {
				continue
			}
			return ctx, nil
		}
	}
	return nil, nil
}

type ProjectClaim struct {
	Plugin     string `yaml:"plugin"`
	ProjectDir string `yaml:"project-dir"`
	Reason     string `yaml:"reason,omitempty"`
}

type ProjectClaimDetector func(projectDir, requestedPlugin string) (*ProjectClaim, error)

var projectClaimDetector ProjectClaimDetector
var projectClaimCache = struct {
	sync.Mutex
	values map[projectClaimCacheKey]cachedProjectClaim
}{
	values: map[projectClaimCacheKey]cachedProjectClaim{},
}

type projectClaimCacheKey struct {
	ProjectDir      string
	RequestedPlugin string
}

type cachedProjectClaim struct {
	Claim *ProjectClaim
}

func SetProjectClaimDetector(detector ProjectClaimDetector) ProjectClaimDetector {
	previous := projectClaimDetector
	projectClaimDetector = detector
	clearProjectClaimCache()
	return previous
}

type CurrentContextDiscovery struct {
	CWD               string
	ComposeProjectDir string
	Claim             *ProjectClaim
	Context           *Context
}

func DiscoverCurrentContext() (*Context, error) {
	result, err := DiscoverCurrentContextForPlugin("")
	if err != nil {
		return nil, err
	}
	return result.Context, nil
}

func DiscoverCurrentContextForPlugin(requestedPlugin string) (CurrentContextDiscovery, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return CurrentContextDiscovery{}, err
	}
	cwd = canonicalProjectDir(cwd)

	result := CurrentContextDiscovery{CWD: cwd}
	projectDir := FindComposeProjectRoot(cwd)
	if projectDir == "" {
		return result, nil
	}
	projectDir = canonicalProjectDir(projectDir)
	result.ComposeProjectDir = projectDir

	if projectClaimDetector == nil {
		return result, nil
	}
	claim, err := cachedProjectClaimFor(projectDir, requestedPlugin)
	if err != nil {
		return result, err
	}
	if claim == nil || strings.TrimSpace(claim.Plugin) == "" {
		return result, nil
	}
	if strings.TrimSpace(claim.ProjectDir) == "" {
		claim.ProjectDir = projectDir
	}
	result.Claim = claim

	ctx, err := FindLocalContextByProjectDirAndPlugin(projectDir, claim.Plugin)
	if err != nil {
		return result, err
	}
	if ctx == nil {
		ctx = claimedLocalContext(claim)
	}
	result.Context = ctx
	return result, nil
}

func cachedProjectClaimFor(projectDir, requestedPlugin string) (*ProjectClaim, error) {
	key := projectClaimCacheKey{
		ProjectDir:      canonicalProjectDir(projectDir),
		RequestedPlugin: strings.TrimSpace(requestedPlugin),
	}

	projectClaimCache.Lock()
	if cached, ok := projectClaimCache.values[key]; ok {
		projectClaimCache.Unlock()
		return cloneProjectClaim(cached.Claim), nil
	}
	projectClaimCache.Unlock()

	claim, err := projectClaimDetector(key.ProjectDir, key.RequestedPlugin)
	if err != nil {
		return nil, err
	}
	if claim != nil {
		if strings.TrimSpace(claim.ProjectDir) == "" {
			claim.ProjectDir = key.ProjectDir
		} else {
			claim.ProjectDir = canonicalProjectDir(claim.ProjectDir)
		}
	}

	projectClaimCache.Lock()
	projectClaimCache.values[key] = cachedProjectClaim{Claim: cloneProjectClaim(claim)}
	projectClaimCache.Unlock()

	return claim, nil
}

func clearProjectClaimCache() {
	projectClaimCache.Lock()
	defer projectClaimCache.Unlock()
	projectClaimCache.values = map[projectClaimCacheKey]cachedProjectClaim{}
}

func cloneProjectClaim(claim *ProjectClaim) *ProjectClaim {
	if claim == nil {
		return nil
	}
	copied := *claim
	return &copied
}

func canonicalProjectDir(projectDir string) string {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return ""
	}
	if resolved, err := filepath.Abs(projectDir); err == nil {
		projectDir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	return projectDir
}

func claimedLocalContext(claim *ProjectClaim) *Context {
	projectDir := canonicalProjectDir(claim.ProjectDir)
	projectName := filepath.Base(projectDir)
	composeProjectName := DetectComposeProjectName(projectDir)
	if strings.TrimSpace(composeProjectName) == "" {
		composeProjectName = projectName
	}
	return &Context{
		Name:               ".",
		Site:               projectName,
		Plugin:             strings.TrimSpace(claim.Plugin),
		DockerHostType:     ContextLocal,
		Environment:        "local",
		DockerSocket:       GetDefaultLocalDockerSocket("/var/run/docker.sock"),
		ProjectName:        projectName,
		ComposeProjectName: composeProjectName,
		ComposeNetwork:     DetectComposeNetworkName(projectDir, composeProjectName),
		ProjectDir:         projectDir,
		Ephemeral:          true,
	}
}
