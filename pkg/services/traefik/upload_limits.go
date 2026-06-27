package traefik

import (
	"context"
	"fmt"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

const (
	UploadLimitsName = "upload-limits"

	uploadSizeName    = "max-upload-size"
	uploadTimeoutName = "upload-timeout"

	DefaultMaxUploadSize = "128M"
	DefaultUploadTimeout = "300s"
)

type UploadLimitsOptions struct {
	AppService     string
	TraefikService string
	Entrypoints    []string
}

func UploadLimits(opts UploadLimitsOptions) (corecomponent.ComposeServiceComponent, error) {
	opts = normalizeUploadLimitsOptions(opts)
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:                UploadLimitsName,
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionDisabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionDisabled, corecomponent.DispositionEnabled},
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "The app service has explicit upload size and upload timeout overrides, and Traefik readTimeout matches.",
			DisabledHelp: "Upload size and timeout are inherited from the app image defaults.",
			Question:     "Override upload size or upload timeout for this application?",
		},
		FollowUps: []corecomponent.FollowUpSpec{
			{
				Name:                 uploadSizeName,
				Label:                "Max upload size",
				FlagName:             uploadSizeName,
				FlagUsage:            "Maximum upload size, such as 128M or 2G.",
				Question:             "Enter the maximum upload size.",
				DefaultValue:         DefaultMaxUploadSize,
				AppliesToDisposition: corecomponent.DispositionEnabled,
				PromptOnCreate:       true,
			},
			{
				Name:                 uploadTimeoutName,
				Label:                "Upload timeout",
				FlagName:             uploadTimeoutName,
				FlagUsage:            "Upload read timeout, such as 300s or 10m.",
				Question:             "Enter the upload read timeout.",
				DefaultValue:         DefaultUploadTimeout,
				AppliesToDisposition: corecomponent.DispositionEnabled,
				PromptOnCreate:       true,
			},
		},
		DefinitionOnRules: []corecomponent.YAMLRule{{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpRestore,
			Path:  ".services." + opts.AppService + ".environment.PHP_UPLOAD_MAX_FILESIZE",
		}},
		DefinitionOffRules: []corecomponent.YAMLRule{{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpDelete,
			Path:  ".services." + opts.AppService + ".environment.PHP_UPLOAD_MAX_FILESIZE",
		}},
		AfterEnableOptions: func(values map[string]string) []corecomponent.Hook {
			return []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
				return applyUploadLimits(runtime.Context, opts, values)
			}}
		},
		AfterDisable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return removeUploadLimits(runtime.Context, opts)
		}},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Sets app upload env vars and matching Traefik readTimeout.",
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Removes app upload env overrides and restores Traefik's default readTimeout.",
			},
		},
	})
}

func normalizeUploadLimitsOptions(opts UploadLimitsOptions) UploadLimitsOptions {
	if strings.TrimSpace(opts.AppService) == "" {
		opts.AppService = "drupal"
	}
	if strings.TrimSpace(opts.TraefikService) == "" {
		opts.TraefikService = "traefik"
	}
	if len(opts.Entrypoints) == 0 {
		opts.Entrypoints = []string{"http", "https", "web", "websecure"}
	}
	return opts
}

func applyUploadLimits(ctx *config.Context, opts UploadLimitsOptions, values map[string]string) error {
	size := strings.TrimSpace(values[uploadSizeName])
	if size == "" {
		size = DefaultMaxUploadSize
	}
	timeout := strings.TrimSpace(values[uploadTimeoutName])
	if timeout == "" {
		timeout = DefaultUploadTimeout
	}
	compose, err := corecomponent.LoadComposeFile(composePathForContext(ctx))
	if err != nil {
		return err
	}
	if err := replaceTraefikReadTimeout(compose, opts, timeout, false); err != nil {
		return err
	}
	env := map[string]string{
		"PHP_UPLOAD_MAX_FILESIZE":    size,
		"PHP_POST_MAX_SIZE":          size,
		"NGINX_CLIENT_MAX_BODY_SIZE": size,
		"NGINX_CLIENT_BODY_TIMEOUT":  timeout,
		"NGINX_FASTCGI_READ_TIMEOUT": timeout,
		"NGINX_FASTCGI_SEND_TIMEOUT": timeout,
	}
	for key, value := range env {
		if err := compose.SetServiceEnv(opts.AppService, key, value); err != nil {
			return err
		}
	}
	return compose.Save()
}

func removeUploadLimits(ctx *config.Context, opts UploadLimitsOptions) error {
	compose, err := corecomponent.LoadComposeFile(composePathForContext(ctx))
	if err != nil {
		return err
	}
	if err := replaceTraefikReadTimeout(compose, opts, DefaultUploadTimeout, true); err != nil {
		return err
	}
	for _, key := range []string{
		"PHP_UPLOAD_MAX_FILESIZE",
		"PHP_POST_MAX_SIZE",
		"NGINX_CLIENT_MAX_BODY_SIZE",
		"NGINX_CLIENT_BODY_TIMEOUT",
		"NGINX_FASTCGI_READ_TIMEOUT",
		"NGINX_FASTCGI_SEND_TIMEOUT",
	} {
		if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
			return err
		}
	}
	return compose.Save()
}

func replaceTraefikReadTimeout(compose *corecomponent.ComposeFile, opts UploadLimitsOptions, timeout string, allowMissingEntrypoint bool) error {
	for _, entrypoint := range opts.Entrypoints {
		for _, prefix := range []string{
			fmt.Sprintf("--entryPoints.%s.transport.respondingTimeouts.readTimeout=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.transport.respondingTimeouts.readTimeout=", entrypoint),
		} {
			if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
				return err
			}
		}
	}
	entrypoints := activeEntrypoints(compose, ReverseProxyOptions{
		TraefikService: opts.TraefikService,
		Entrypoints:    opts.Entrypoints,
	})
	if len(entrypoints) == 0 {
		if allowMissingEntrypoint {
			return nil
		}
		return fmt.Errorf("service %q does not declare a Traefik entrypoint address", opts.TraefikService)
	}
	for _, entrypoint := range entrypoints {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.transport.respondingTimeouts.readTimeout=%s", entrypoint, timeout)); err != nil {
			return err
		}
	}
	return nil
}
