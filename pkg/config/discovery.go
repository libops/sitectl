package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
		data, err := os.ReadFile(path)
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
		data, err := os.ReadFile(path)
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
		data := ctx.ReadSmallFile(path)
		if strings.TrimSpace(data) == "" {
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
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return nil, nil
	}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
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
		storedDir := filepath.Clean(strings.TrimSpace(ctx.ProjectDir))
		if resolved, err := filepath.EvalSymlinks(storedDir); err == nil {
			storedDir = resolved
		}
		if storedDir == projectDir {
			return ctx, nil
		}
	}
	return nil, nil
}

func DiscoverCurrentContext() (*Context, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return FindLocalContextByProjectDir(cwd)
}
