package plugin

import (
	"fmt"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	coredevmode "github.com/libops/sitectl/pkg/services/devmode"
	coretraefik "github.com/libops/sitectl/pkg/services/traefik"
)

// StandardComposeAppPluginOptions configures the common registration shape for
// compose-backed application plugins.
type StandardComposeAppPluginOptions struct {
	PluginName           string
	DisplayName          string
	AppService           string
	Router               string
	DefaultPath          string
	ReadyMessage         string
	Discovery            ComposeProjectDiscovery
	CreateSpec           CreateSpec
	CreateOptions        ComposeTemplateCreateOptions
	IngressOptions       coretraefik.IngressOptions
	DevModeOptions       coredevmode.Options
	Healthcheck          HealthcheckRunner
	IngressRouteProvider IngressRouteProvider
	ExtraComponents      []corecomponent.ComposeServiceComponent
}

// RegisterStandardComposeAppPlugin registers discovery, template create,
// ingress, dev-mode, healthcheck, and route discovery for a standard
// compose-backed web application plugin.
func (s *SDK) RegisterStandardComposeAppPlugin(opts StandardComposeAppPluginOptions) error {
	if s == nil {
		return nil
	}
	pluginName := strings.TrimSpace(opts.PluginName)
	if pluginName == "" {
		pluginName = s.Metadata.Name
	}
	appService := strings.TrimSpace(opts.AppService)
	if appService == "" {
		appService = pluginName
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		displayName = appService
	}

	discovery := opts.Discovery
	if len(discovery.RequiredServices) == 0 {
		discovery.RequiredServices = []string{appService}
	}
	if strings.TrimSpace(discovery.Reason) == "" && len(discovery.RequiredServices) > 0 {
		discovery.Reason = discovery.RequiredServices[0] + " service"
	}
	s.SetComposeProjectDiscovery(discovery)

	createOptions := opts.CreateOptions
	if strings.TrimSpace(createOptions.DefaultPath) == "" {
		createOptions.DefaultPath = opts.DefaultPath
	}
	if strings.TrimSpace(createOptions.DefaultPlugin) == "" {
		createOptions.DefaultPlugin = pluginName
	}
	if strings.TrimSpace(createOptions.ReadyMessage) == "" {
		createOptions.ReadyMessage = opts.ReadyMessage
	}
	createSpec := normalizeCreateSpec(opts.CreateSpec)
	if strings.TrimSpace(createSpec.Name) == "" {
		createSpec.Name = "default"
		createSpec.Default = true
	}
	if strings.TrimSpace(createSpec.Plugin) == "" {
		createSpec.Plugin = pluginName
	}
	s.RegisterComposeTemplateCreateRunner(createSpec, createOptions)

	components, err := standardComposeAppComponents(appService, opts)
	if err != nil {
		return err
	}
	s.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: displayName,
		Components:  components,
	})

	if opts.Healthcheck != nil {
		s.RegisterHealthcheckRunner(opts.Healthcheck)
	}
	if opts.IngressRouteProvider != nil {
		s.RegisterIngressRouteProvider(opts.IngressRouteProvider)
	} else {
		router := strings.TrimSpace(opts.Router)
		if router == "" {
			router = appService
		}
		s.RegisterIngressRouteProvider(StandardComposeWebIngressRoutesWithOptions(StandardComposeWebIngressOptions{
			AppService: appService,
			Router:     router,
		}))
	}
	return nil
}

func standardComposeAppComponents(appService string, opts StandardComposeAppPluginOptions) ([]corecomponent.ComposeServiceComponent, error) {
	ingressOptions := opts.IngressOptions
	if strings.TrimSpace(ingressOptions.AppService) == "" {
		ingressOptions.AppService = appService
	}
	if strings.TrimSpace(ingressOptions.HTTPEntrypoint) == "" {
		ingressOptions.HTTPEntrypoint = "web"
	}
	if strings.TrimSpace(ingressOptions.HTTPSEntrypoint) == "" {
		ingressOptions.HTTPSEntrypoint = "websecure"
	}
	ingress, err := coretraefik.Ingress(ingressOptions)
	if err != nil {
		return nil, fmt.Errorf("build %s ingress component: %w", appService, err)
	}

	devModeOptions := opts.DevModeOptions
	if strings.TrimSpace(devModeOptions.AppService) == "" {
		devModeOptions.AppService = appService
	}
	devMode, err := coredevmode.Component(devModeOptions)
	if err != nil {
		return nil, fmt.Errorf("build %s dev-mode component: %w", appService, err)
	}

	components := []corecomponent.ComposeServiceComponent{ingress, devMode}
	components = append(components, opts.ExtraComponents...)
	return components, nil
}

// MustRegisterStandardComposeAppPlugin is like RegisterStandardComposeAppPlugin,
// but panics when component construction fails during plugin startup.
func (s *SDK) MustRegisterStandardComposeAppPlugin(opts StandardComposeAppPluginOptions) {
	if err := s.RegisterStandardComposeAppPlugin(opts); err != nil {
		panic(err)
	}
}
