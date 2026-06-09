package plugin

import (
	"fmt"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
)

// StandardServiceComponentPluginOptions configures the common registration
// shape for compose-backed service plugins.
type StandardServiceComponentPluginOptions struct {
	PluginName       string
	DisplayName      string
	DefaultPath      string
	RequiredServices []string
	DiscoveryReason  string
	ReadyMessage     string
	CreateSpec       CreateSpec
	Components       func() ([]corecomponent.ComposeServiceComponent, error)
}

// RegisterStandardServiceComponentPlugin registers compose discovery, create
// template support, lifecycle commands, and service components for a standard
// compose-backed service plugin.
func (s *SDK) RegisterStandardServiceComponentPlugin(opts StandardServiceComponentPluginOptions) error {
	if s == nil {
		return nil
	}
	pluginName := strings.TrimSpace(opts.PluginName)
	if pluginName == "" {
		pluginName = s.Metadata.Name
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		displayName = pluginName
	}

	requiredServices := append([]string{}, opts.RequiredServices...)
	if len(requiredServices) == 0 && pluginName != "" {
		requiredServices = []string{pluginName}
	}
	reason := strings.TrimSpace(opts.DiscoveryReason)
	if reason == "" && len(requiredServices) > 0 {
		reason = requiredServices[0] + " service"
	}
	s.SetComposeProjectDiscovery(ComposeProjectDiscovery{
		RequiredServices: requiredServices,
		Reason:           reason,
	})
	s.RegisterStandardComposeTemplate(opts.CreateSpec, StandardComposeTemplateOptions{
		DefaultPath:   opts.DefaultPath,
		DefaultPlugin: pluginName,
		ReadyMessage:  opts.ReadyMessage,
		DisplayName:   displayName,
	})

	if opts.Components == nil {
		return nil
	}
	components, err := opts.Components()
	if err != nil {
		return fmt.Errorf("build %s service components: %w", pluginName, err)
	}
	s.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: displayName,
		Components:  components,
	})
	return nil
}

// MustRegisterStandardServiceComponentPlugin is like
// RegisterStandardServiceComponentPlugin, but panics when component
// construction fails during plugin startup.
func (s *SDK) MustRegisterStandardServiceComponentPlugin(opts StandardServiceComponentPluginOptions) {
	if err := s.RegisterStandardServiceComponentPlugin(opts); err != nil {
		panic(err)
	}
}
