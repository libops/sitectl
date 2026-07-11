package plugin

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

const ComposeImageOverrideFile = config.LocalDevComposeOverrideName

type ComposeImageOverrides struct {
	Images    map[string]string
	BuildArgs map[string]map[string]string
}

type serviceTagOverrideKind int

const (
	serviceTagOverrideBaseImageArg serviceTagOverrideKind = iota
	serviceTagOverrideImageRef
)

type serviceTagTarget struct {
	Service string
	Image   string
	Kind    serviceTagOverrideKind
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

func ResolveComposeImageOverrides(pluginName string, imageTags, images, buildArgs []string) (ComposeImageOverrides, error) {
	var overrides ComposeImageOverrides
	for _, value := range imageTags {
		service, tag, err := parseServiceAssignment(value, "TAG")
		if err != nil {
			return overrides, fmt.Errorf("parse --tag: %w", err)
		}
		target, ok := serviceTagTargetForService(pluginName, service)
		if !ok {
			return overrides, fmt.Errorf("--tag does not know service %q for plugin %q; use --image SERVICE=IMAGE or --build-arg SERVICE.ARG=VALUE instead", service, pluginName)
		}
		if err := addServiceTagOverride(&overrides, tag, target); err != nil {
			return overrides, err
		}
	}
	for _, value := range images {
		service, image, err := parseServiceAssignment(value, "IMAGE")
		if err != nil {
			return overrides, fmt.Errorf("parse --image: %w", err)
		}
		if buildableApplicationService(pluginName, service) {
			return overrides, fmt.Errorf("--image cannot override buildable application service %q for plugin %q because Compose would still build that service; use --tag %s=TAG or --build-arg %s.BASE_IMAGE=IMAGE instead", service, pluginName, service, service)
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

func addServiceTagOverride(overrides *ComposeImageOverrides, tag string, target serviceTagTarget) error {
	switch target.Kind {
	case serviceTagOverrideImageRef:
		overrides.AddImage(target.Service, target.Image+":"+tag)
	default:
		overrides.AddBuildArg(target.Service, "BASE_IMAGE", target.Image+":"+tag)
	}
	return nil
}

func ApplyComposeImageOverrides(projectDir string, overrides ComposeImageOverrides) error {
	return ApplyComposeImageOverridesContext(localImageOverrideContext(projectDir), overrides)
}

func ApplyComposeImageOverridesContext(ctx *config.Context, overrides ComposeImageOverrides) error {
	if overrides.Empty() {
		return nil
	}
	ctx, projectDir, err := imageOverrideContext(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	exists, err := ctx.FileExists(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if exists {
		data, err := ctx.ReadFile(path)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			var validation any
			if err := yaml.Unmarshal(data, &validation); err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
		}
	}

	compose, err := corecomponent.LoadComposeFileOptionalForContext(ctx, path)
	if err != nil {
		return err
	}
	for _, service := range sortedStringKeys(overrides.Images) {
		if err := compose.SetServiceOverrideScalar(service, "image", fmt.Sprintf("%q", overrides.Images[service])); err != nil {
			return err
		}
	}
	for _, service := range sortedNestedStringKeys(overrides.BuildArgs) {
		for _, name := range sortedStringKeys(overrides.BuildArgs[service]) {
			if err := compose.SetServiceBuildArg(service, name, overrides.BuildArgs[service][name]); err != nil {
				return err
			}
		}
	}
	return compose.Save()
}

func ClearComposeImageOverrides(projectDir string, services []string) error {
	return ClearComposeImageOverridesContext(localImageOverrideContext(projectDir), services)
}

func ClearComposeImageOverridesContext(ctx *config.Context, services []string) error {
	ctx, projectDir, err := imageOverrideContext(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	exists, err := ctx.FileExists(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if !exists {
		return nil
	}
	data, err := ctx.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	servicesMap, ok := doc["services"].(map[string]any)
	if !ok || len(servicesMap) == 0 {
		return nil
	}
	targets := map[string]bool{}
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service != "" {
			targets[service] = true
		}
	}
	compose, err := corecomponent.LoadComposeFileForContext(ctx, path)
	if err != nil {
		return err
	}
	for _, service := range sortedAnyKeys(servicesMap) {
		if len(targets) > 0 && !targets[service] {
			continue
		}
		raw := servicesMap[service]
		serviceMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, hasImage := serviceMap["image"]; hasImage {
			if err := compose.DeleteServiceKey(service, "image"); err != nil {
				return err
			}
		}
		if buildMap, ok := serviceMap["build"].(map[string]any); ok {
			if _, hasArgs := buildMap["args"]; hasArgs {
				if err := compose.DeleteServiceBuildArgs(service); err != nil {
					return err
				}
			}
		}
		if err := compose.PruneEmptyService(service); err != nil {
			return err
		}
	}
	return compose.Save()
}

func localImageOverrideContext(projectDir string) *config.Context {
	return &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     strings.TrimSpace(projectDir),
	}
}

func imageOverrideContext(ctx *config.Context) (*config.Context, string, error) {
	if ctx == nil {
		return nil, "", fmt.Errorf("context cannot be nil")
	}
	projectDir := strings.TrimSpace(ctx.ProjectDir)
	if projectDir == "" {
		return nil, "", fmt.Errorf("project directory cannot be empty")
	}
	return ctx, projectDir, nil
}

func resolveCreateImageOverrides(cmd *cobra.Command, req ComposeCreateRequest, pluginName string) (ComposeImageOverrides, error) {
	imageTags, err := cmd.Flags().GetStringArray("tag")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get tag flag: %w", err)
	}
	images, err := cmd.Flags().GetStringArray("image")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get image flag: %w", err)
	}
	buildArgs, err := cmd.Flags().GetStringArray("build-arg")
	if err != nil {
		return ComposeImageOverrides{}, fmt.Errorf("get build-arg flag: %w", err)
	}
	name := strings.TrimSpace(pluginName)
	if name == "" {
		name = strings.TrimSpace(req.ProjectName)
	}
	return ResolveComposeImageOverrides(name, imageTags, images, buildArgs)
}

func defaultServiceTagTarget(pluginName string) (serviceTagTarget, bool) {
	switch strings.ToLower(strings.TrimSpace(pluginName)) {
	case "archivesspace":
		return serviceTagTarget{Service: "archivesspace", Image: "libops/archivesspace", Kind: serviceTagOverrideBaseImageArg}, true
	case "drupal":
		return serviceTagTarget{Service: "drupal", Image: "libops/drupal", Kind: serviceTagOverrideBaseImageArg}, true
	case "isle", "islandora":
		return serviceTagTarget{Service: "drupal", Image: "libops/islandora", Kind: serviceTagOverrideBaseImageArg}, true
	case "ojs":
		return serviceTagTarget{Service: "ojs", Image: "libops/ojs", Kind: serviceTagOverrideBaseImageArg}, true
	case "omeka-classic":
		return serviceTagTarget{Service: "omeka-classic", Image: "libops/omeka-classic", Kind: serviceTagOverrideBaseImageArg}, true
	case "omeka-s":
		return serviceTagTarget{Service: "omeka-s", Image: "libops/omeka-s", Kind: serviceTagOverrideBaseImageArg}, true
	case "wp", "wordpress":
		return serviceTagTarget{Service: "wp", Image: "libops/wp", Kind: serviceTagOverrideBaseImageArg}, true
	default:
		return serviceTagTarget{}, false
	}
}

func serviceTagTargetForService(pluginName, service string) (serviceTagTarget, bool) {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "" {
		return serviceTagTarget{}, false
	}
	if target, ok := defaultServiceTagTarget(pluginName); ok && service == target.Service {
		return target, true
	}
	if strings.EqualFold(strings.TrimSpace(pluginName), "archivesspace") && service == "solr" {
		return serviceTagTarget{Service: "solr", Image: "libops/archivesspace-solr", Kind: serviceTagOverrideImageRef}, true
	}
	if isIslandoraPluginName(pluginName) && service == "drupal" {
		return serviceTagTarget{Service: "drupal", Image: "libops/islandora", Kind: serviceTagOverrideBaseImageArg}, true
	}
	if image, ok := publishedImageByService(service); ok {
		return serviceTagTarget{Service: service, Image: image, Kind: serviceTagOverrideImageRef}, true
	}
	return serviceTagTarget{}, false
}

func buildableApplicationService(pluginName, service string) bool {
	pluginName = strings.ToLower(strings.TrimSpace(pluginName))
	service = strings.ToLower(strings.TrimSpace(service))
	switch pluginName {
	case "app-tmpl":
		return service == "app"
	case "archivesspace":
		return service == "archivesspace"
	case "drupal":
		return service == "drupal"
	case "isle", "islandora":
		return service == "drupal"
	case "ojs":
		return service == "ojs"
	case "omeka-classic":
		return service == "omeka-classic"
	case "omeka-s":
		return service == "omeka-s"
	case "wp", "wordpress":
		return service == "wp"
	default:
		return false
	}
}

func isIslandoraPluginName(pluginName string) bool {
	switch strings.ToLower(strings.TrimSpace(pluginName)) {
	case "isle", "islandora":
		return true
	default:
		return false
	}
}

func publishedImageByService(service string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "activemq":
		return "libops/activemq", true
	case "alpaca":
		return "libops/alpaca", true
	case "base", "init":
		return "libops/base", true
	case "blazegraph":
		return "libops/blazegraph", true
	case "crayfits":
		return "libops/crayfits", true
	case "fcrepo":
		return "libops/fcrepo", true
	case "fits":
		return "libops/fits", true
	case "homarus":
		return "libops/homarus", true
	case "houdini":
		return "libops/houdini", true
	case "hypercube":
		return "libops/hypercube", true
	case "mariadb":
		return "libops/mariadb", true
	case "mergepdf":
		return "libops/mergepdf", true
	case "solr":
		return "libops/solr", true
	default:
		return "", false
	}
}

func parseServiceAssignment(value, rightName string) (string, string, error) {
	left, right, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		if strings.TrimSpace(rightName) == "" {
			rightName = "VALUE"
		}
		return "", "", fmt.Errorf("expected SERVICE=%s, got %q", rightName, value)
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

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedNestedStringKeys(values map[string]map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedAnyKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
