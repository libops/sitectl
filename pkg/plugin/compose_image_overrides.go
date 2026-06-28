package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	serviceTagOverrideRepositoryTagArgs
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
	case serviceTagOverrideRepositoryTagArgs:
		repository, _, ok := strings.Cut(target.Image, "/")
		if !ok || strings.TrimSpace(repository) == "" {
			return fmt.Errorf("cannot derive repository for image %q", target.Image)
		}
		overrides.AddBuildArg(target.Service, "REPOSITORY", repository)
		overrides.AddBuildArg(target.Service, "TAG", tag)
	case serviceTagOverrideImageRef:
		overrides.AddImage(target.Service, target.Image+":"+tag)
	default:
		overrides.AddBuildArg(target.Service, "BASE_IMAGE", target.Image+":"+tag)
	}
	return nil
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
		return serviceTagTarget{Service: "drupal", Image: "libops/drupal", Kind: serviceTagOverrideRepositoryTagArgs}, true
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
		return serviceTagTarget{Service: "solr", Image: "libops/solr", Kind: serviceTagOverrideBaseImageArg}, true
	}
	if isIslandoraPluginName(pluginName) && service == "drupal" {
		return serviceTagTarget{Service: "drupal", Image: "libops/drupal", Kind: serviceTagOverrideRepositoryTagArgs}, true
	}
	if image, ok := publishedImageByService(service); ok {
		return serviceTagTarget{Service: service, Image: image, Kind: serviceTagOverrideImageRef}, true
	}
	return serviceTagTarget{}, false
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

func ensureStringMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}
