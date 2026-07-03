package devmode

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const (
	Name                = "dev-mode"
	defaultOverrideFile = "docker-compose.override.yml"
	defaultAssistant    = "cli-sandbox"
	defaultHarness      = "codex"
)

type Options struct {
	AppService       string
	OverrideFile     string
	Environment      map[string]string
	Volumes          []string
	AssistantService string
	AssistantVolumes []string
}

type AssistantOptions struct {
	Enabled            bool
	Harness            string
	Model              string
	ComposeAccess      bool
	SkipEgressFirewall bool
}

func Component(opts Options) (corecomponent.ComposeServiceComponent, error) {
	opts = normalizeOptions(opts)
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:                Name,
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionDisabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionDisabled, corecomponent.DispositionEnabled},
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "A local docker-compose.override.yml mounts the host codebase and sets UID for writable files.",
			DisabledHelp: "No local development compose override is present.",
			Question:     "Enable local development bind mounts for this application?",
		},
		FollowUps: devModeFollowUps(),
		DefinitionOnRules: []corecomponent.YAMLRule{{
			Files: []string{opts.OverrideFile},
			Op:    corecomponent.OpRestore,
			Path:  ".services." + opts.AppService + ".environment.UID",
		}},
		DefinitionOffRules: []corecomponent.YAMLRule{{
			Files: []string{opts.OverrideFile},
			Op:    corecomponent.OpDelete,
			Path:  ".",
		}},
		AfterEnable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return writeOverride(runtime.Context, opts, AssistantOptions{})
		}},
		AfterEnableOptions: func(values map[string]string) []corecomponent.Hook {
			assistant := assistantOptionsFromFollowUps(values)
			if !assistant.Enabled {
				return nil
			}
			return []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
				return writeOverride(runtime.Context, opts, assistant)
			}}
		},
		AfterDisable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return removeOverride(runtime.Context, opts)
		}},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Writes docker-compose.override.yml for local bind mounts and UID propagation.",
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Removes docker-compose.override.yml.",
			},
		},
	})
}

func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.AppService) == "" {
		opts.AppService = "drupal"
	}
	if strings.TrimSpace(opts.OverrideFile) == "" {
		opts.OverrideFile = defaultOverrideFile
	}
	if strings.TrimSpace(opts.AssistantService) == "" {
		opts.AssistantService = defaultAssistant
	}
	if opts.Environment == nil {
		opts.Environment = map[string]string{}
	}
	if strings.TrimSpace(opts.Environment["UID"]) == "" {
		opts.Environment["UID"] = "${UID:-1000}"
	}
	opts.Volumes = normalizeVolumes(opts.Volumes)
	if opts.AssistantVolumes == nil {
		opts.AssistantVolumes = append([]string{}, opts.Volumes...)
	}
	opts.AssistantVolumes = normalizeVolumes(opts.AssistantVolumes)
	return opts
}

func writeOverride(ctx *config.Context, opts Options, assistant AssistantOptions) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	service := map[string]any{
		"environment": sortedStringMap(opts.Environment),
	}
	if len(opts.Volumes) > 0 {
		service["volumes"] = opts.Volumes
	}
	services := map[string]any{
		opts.AppService: service,
	}
	root := map[string]any{
		"services": services,
	}
	if assistant.Enabled {
		services[opts.AssistantService] = assistantService(ctx, opts, assistant)
		root["networks"] = map[string]any{
			opts.AssistantService: map[string]any{},
		}
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal dev mode override: %w", err)
	}
	return ctx.WriteFile(ctx.ResolveProjectPath(opts.OverrideFile), data)
}

func devModeFollowUps() []corecomponent.FollowUpSpec {
	return []corecomponent.FollowUpSpec{
		{
			Name:         "assistant",
			Label:        "Assistant",
			FlagName:     "assistant",
			FlagUsage:    "Add a cli-sandbox coding-agent service to the development override",
			DefaultValue: "false",
			BoolValue:    true,
			AppliesTo:    corecomponent.StateOn,
		},
		{
			Name:         "harness",
			Label:        "Assistant harness",
			FlagName:     "harness",
			FlagUsage:    "Coding agent harness: codex, claude, pi, opencode, or gemini",
			DefaultValue: defaultHarness,
			Choices: []corecomponent.Choice{
				{Value: "codex", Label: "Codex"},
				{Value: "claude", Label: "Claude"},
				{Value: "pi", Label: "Pi"},
				{Value: "opencode", Label: "OpenCode"},
				{Value: "gemini", Label: "Gemini"},
			},
			AppliesTo: corecomponent.StateOn,
		},
		{
			Name:         "model",
			Label:        "Assistant model",
			FlagName:     "model",
			FlagUsage:    "Model name, endpoint URL, or default to let the harness choose",
			DefaultValue: "default",
			AppliesTo:    corecomponent.StateOn,
		},
		{
			Name:         "compose-access",
			Label:        "Compose access",
			FlagName:     "compose-access",
			FlagUsage:    "Attach the assistant to the Compose network and mount the host Docker socket",
			DefaultValue: "false",
			BoolValue:    true,
			AppliesTo:    corecomponent.StateOn,
		},
		{
			Name:         "skip-egress-firewall",
			Label:        "Skip egress firewall",
			FlagName:     "skip-egress-firewall",
			FlagUsage:    "Set SKIP_EGRESS_FIREWALL=true and omit NET_ADMIN/NET_RAW capabilities",
			DefaultValue: "false",
			BoolValue:    true,
			AppliesTo:    corecomponent.StateOn,
		},
	}
}

func assistantOptionsFromFollowUps(values map[string]string) AssistantOptions {
	return AssistantOptions{
		Enabled:            corecomponent.ParseFollowUpBool(values["assistant"]),
		Harness:            normalizeHarness(values["harness"]),
		Model:              normalizeModel(values["model"]),
		ComposeAccess:      corecomponent.ParseFollowUpBool(values["compose-access"]),
		SkipEgressFirewall: corecomponent.ParseFollowUpBool(values["skip-egress-firewall"]),
	}
}

func assistantService(ctx *config.Context, opts Options, assistant AssistantOptions) map[string]any {
	harness := normalizeHarness(assistant.Harness)
	env := sortedStringMap(assistantEnvironment(ctx, harness, assistant))
	service := map[string]any{
		"image":       "${SITECTL_ASSISTANT_IMAGE:-ghcr.io/libops/cli-sandbox:" + harness + "}",
		"pull_policy": "always",
		"profiles":    []string{"assistant"},
		"working_dir": "/workspace",
		"stdin_open":  true,
		"tty":         true,
		"environment": env,
		"volumes":     assistantVolumes(harness, opts.AssistantVolumes, assistant.ComposeAccess),
		"command":     assistantCommand(harness, assistant.Model),
		"networks":    assistantNetworks(opts.AssistantService, assistant.ComposeAccess),
	}
	if !assistant.SkipEgressFirewall {
		service["cap_add"] = []string{"NET_ADMIN", "NET_RAW"}
	}
	if assistant.ComposeAccess {
		service["group_add"] = []string{dockerSocketGID(ctx)}
	}
	return service
}

func assistantEnvironment(ctx *config.Context, harness string, assistant AssistantOptions) map[string]string {
	env := map[string]string{
		"SKIP_EGRESS_FIREWALL": corecomponent.FormatFollowUpBool(assistant.SkipEgressFirewall),
		"COLUMNS":              "${COLUMNS:-120}",
		"LINES":                "${LINES:-40}",
	}
	setIfNotEmpty := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			env[key] = strings.TrimSpace(value)
		}
	}
	setIfNotEmpty("GIT_AUTHOR_NAME", gitIdentity("GIT_AUTHOR_NAME", "user.name"))
	setIfNotEmpty("GIT_AUTHOR_EMAIL", gitIdentity("GIT_AUTHOR_EMAIL", "user.email"))
	setIfNotEmpty("GIT_COMMITTER_NAME", gitIdentity("GIT_COMMITTER_NAME", "user.name"))
	setIfNotEmpty("GIT_COMMITTER_EMAIL", gitIdentity("GIT_COMMITTER_EMAIL", "user.email"))
	if assistant.ComposeAccess {
		env["DOCKER_HOST"] = "unix:///var/run/docker.sock"
	}
	if isURLModel(assistant.Model) {
		modelURL := strings.TrimRight(assistant.Model, "/")
		env["OPENAI_BASE_URL"] = modelURL
		env["OPENAI_API_BASE"] = modelURL
		env["TASK_AGENT_MODEL_BASE_URL"] = modelURL
	}
	if model := assistantModelName(assistant.Model); model != "" {
		env["TASK_AGENT_MODEL"] = model
		if harness == "codex" {
			env["CODEX_MODEL"] = model
		}
	}
	_ = ctx
	return env
}

func assistantVolumes(harness string, pluginVolumes []string, composeAccess bool) []string {
	volumes := []string{
		assistantHomeVolume(harness),
		"./:/workspace:rw",
	}
	volumes = append(volumes, pluginVolumes...)
	if composeAccess {
		volumes = append(volumes, "/var/run/docker.sock:/var/run/docker.sock:ro")
	}
	return normalizeVolumes(volumes)
}

func assistantHomeVolume(harness string) string {
	switch harness {
	case "opencode":
		return "${HOME}/.local/share/opencode:/home/node/.local/share/opencode:rw"
	case "pi":
		return "${HOME}/.pi:/home/node/.pi:rw"
	default:
		return "${HOME}/." + harness + ":/home/node/." + harness + ":rw"
	}
}

func assistantNetworks(service string, composeAccess bool) []string {
	if composeAccess {
		return []string{"default", service}
	}
	return []string{service}
}

func assistantCommand(harness, model string) []string {
	args := []string{harness}
	args = append(args, assistantPermissionArgs(harness)...)
	if model := assistantModelName(model); model != "" {
		args = append(args, assistantModelArgs(harness, model)...)
	}
	return args
}

func assistantPermissionArgs(harness string) []string {
	switch harness {
	case "codex":
		return []string{"--sandbox", "danger-full-access", "--ask-for-approval", "never"}
	case "claude":
		return []string{"--dangerously-skip-permissions"}
	case "gemini":
		return []string{"--approval-mode", "yolo"}
	case "opencode":
		return []string{"--auto"}
	case "pi":
		return []string{"--approve"}
	default:
		return nil
	}
}

func assistantModelArgs(harness, model string) []string {
	switch harness {
	case "opencode":
		return []string{"--model", model}
	case "pi":
		return []string{"--model", model}
	default:
		return []string{"--model", model}
	}
}

func assistantModelName(model string) string {
	model = normalizeModel(model)
	if model == "" || strings.EqualFold(model, "default") || isURLModel(model) {
		return ""
	}
	return model
}

func isURLModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	parsed, err := url.Parse(model)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func normalizeHarness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "pi", "opencode", "gemini":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return defaultHarness
	}
}

func normalizeModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}

func gitIdentity(envName, configKey string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	output, err := exec.Command("git", "config", "--global", configKey).Output() // #nosec G204 -- command and args are fixed.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func dockerSocketGID(ctx *config.Context) string {
	const fallback = "${SOCK_GID:-0}"
	if ctx == nil {
		return fallback
	}
	output, err := ctx.RunQuietCommand(exec.Command("stat", "-c", "%g", "/var/run/docker.sock")) // #nosec G204 -- command and args are fixed.
	if err != nil {
		return fallback
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return fallback
	}
	return output
}

func removeOverride(ctx *config.Context, opts Options) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	return ctx.RemoveFile(ctx.ResolveProjectPath(opts.OverrideFile))
}

func sortedStringMap(values map[string]string) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		if value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func normalizeVolumes(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
