package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

const ComposeImageOverrideFile = "docker-compose.override.yaml"

type ComposeImageOverrides struct {
	Images    map[string]string
	BuildArgs map[string]map[string]string
}

func (o ComposeImageOverrides) Empty() bool {
	return len(o.Images) == 0 && len(o.BuildArgs) == 0
}

func (o *ComposeImageOverrides) AddImage(service, image string) {
	service = strings.TrimSpace(service)
	image = strings.TrimSpace(image)
	if service == "" || image == "" {
		return
	}
	if o.Images == nil {
		o.Images = map[string]string{}
	}
	o.Images[service] = image
}

func (o *ComposeImageOverrides) AddBuildArg(service, name, value string) {
	service = strings.TrimSpace(service)
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if service == "" || name == "" || value == "" {
		return
	}
	if o.BuildArgs == nil {
		o.BuildArgs = map[string]map[string]string{}
	}
	if o.BuildArgs[service] == nil {
		o.BuildArgs[service] = map[string]string{}
	}
	o.BuildArgs[service][name] = value
}

func ResolveComposeImageOverrides(pluginName, buildkitRepository, buildkitTag string, imageRefs, buildArgs []string) (ComposeImageOverrides, error) {
	var overrides ComposeImageOverrides
	repository := strings.Trim(strings.TrimSpace(buildkitRepository), "/")
	if repository == "" {
		repository = "libops"
	}
	if tag := strings.TrimSpace(buildkitTag); tag != "" {
		service, imageName, ok := buildkitBaseImageTarget(pluginName)
		if !ok {
			return overrides, fmt.Errorf("--buildkit-tag is not supported for plugin %q; use --build-arg SERVICE.ARG=VALUE instead", pluginName)
		}
		overrides.AddBuildArg(service, "BASE_IMAGE", repository+"/"+imageName+":"+tag)
	}
	for _, value := range imageRefs {
		service, image, err := parseServiceAssignment(value)
		if err != nil {
			return overrides, fmt.Errorf("parse --image-ref: %w", err)
		}
		overrides.AddImage(service, image)
	}
	for _, value := range buildArgs {
		service, name, argValue, err := parseBuildArgAssignment(value)
		if err != nil {
			return overrides, fmt.Errorf("parse --build-arg: %w", err)
		}
		overrides.AddBuildArg(service, name, argValue)
	}
	return overrides, nil
}

func ApplyComposeImageOverrides(projectDir string, overrides ComposeImageOverrides) error {
	if overrides.Empty() {
		return nil
	}
	if strings.TrimSpace(projectDir) == "" {
		return fmt.Errorf("project directory cannot be empty")
	}
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	services := ensureStringMap(doc, "services")
	for service, image := range overrides.Images {
		serviceMap := ensureStringMap(services, service)
		serviceMap["image"] = image
	}
	for service, args := range overrides.BuildArgs {
		serviceMap := ensureStringMap(services, service)
		buildMap := ensureStringMap(serviceMap, "build")
		argsMap := ensureStringMap(buildMap, "args")
		for name, value := range args {
			argsMap[name] = value
		}
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func resolveCreateImageOverrides(cmd *cobra.Command, req ComposeCreateRequest, pluginName string) (ComposeImageOverrides, error) {
	buildkitTag, err := cmd.Flags().GetString("buildkit-tag")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get buildkit-tag flag: %w", err)
	}
	buildkitRepository, err := cmd.Flags().GetString("buildkit-repository")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get buildkit-repository flag: %w", err)
	}
	imageRefs, err := cmd.Flags().GetStringArray("image-ref")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get image-ref flag: %w", err)
	}
	buildArgs, err := cmd.Flags().GetStringArray("build-arg")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get build-arg flag: %w", err)
	}
	name := strings.TrimSpace(pluginName)
	if name == "" {
		name = strings.TrimSpace(req.ProjectName)
	}
	return ResolveComposeImageOverrides(name, buildkitRepository, buildkitTag, imageRefs, buildArgs)
}

func buildkitBaseImageTarget(pluginName string) (service, image string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(pluginName)) {
	case "archivesspace":
		return "archivesspace", "archivesspace", true
	case "drupal", "isle", "islandora":
		return "drupal", "nginx", true
	case "ojs":
		return "ojs", "nginx", true
	case "omeka-classic":
		return "omeka-classic", "nginx", true
	case "omeka-s":
		return "omeka-s", "nginx", true
	case "wp", "wordpress":
		return "wp", "nginx", true
	default:
		return "", "", false
	}
}

func parseServiceAssignment(value string) (string, string, error) {
	left, right, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return "", "", fmt.Errorf("expected SERVICE=IMAGE, got %q", value)
	}
	return strings.TrimSpace(left), strings.TrimSpace(right), nil
}

func parseBuildArgAssignment(value string) (string, string, string, error) {
	left, right, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return "", "", "", fmt.Errorf("expected SERVICE.ARG=VALUE, got %q", value)
	}
	service, name, ok := strings.Cut(strings.TrimSpace(left), ".")
	if !ok || strings.TrimSpace(service) == "" || strings.TrimSpace(name) == "" {
		return "", "", "", fmt.Errorf("expected SERVICE.ARG=VALUE, got %q", value)
	}
	return strings.TrimSpace(service), strings.TrimSpace(name), strings.TrimSpace(right), nil
}

func ensureStringMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}
