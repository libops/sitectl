package hostruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var appNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type Manifest map[string]Application

type Application struct {
	Name                string   `json:"name"`
	DockerComposeRepo   string   `json:"docker_compose_repo"`
	DockerComposeBranch string   `json:"docker_compose_branch"`
	ProjectDir          string   `json:"project_dir"`
	ComposeProjectName  string   `json:"compose_project_name"`
	IngressPort         int      `json:"ingress_port"`
	SitectlContextName  string   `json:"sitectl_context_name"`
	SitectlPlugin       string   `json:"sitectl_plugin"`
	SitectlEnvironment  string   `json:"sitectl_environment"`
	SitectlVerifyArgs   []string `json:"sitectl_verify_args"`
	Ingress             Ingress  `json:"ingress"`
	InitCommands        []string `json:"init_commands"`
	UpCommands          []string `json:"up_commands"`
	DownCommands        []string `json:"down_commands"`
	RolloutCommands     []string `json:"rollout_commands"`
}

type Ingress struct {
	LetsEncrypt   bool     `json:"letsencrypt"`
	BotMitigation bool     `json:"bot_mitigation"`
	Mode          string   `json:"mode"`
	Domain        string   `json:"domain"`
	ACMEEmail     string   `json:"acme_email"`
	TrustedIPs    []string `json:"trusted_ips"`
	MaxUploadSize string   `json:"max_upload_size"`
	UploadTimeout string   `json:"upload_timeout"`
}

func LoadManifest(path, dataRoot string) (Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect application manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("application manifest must be a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read application manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode application manifest: %w", err)
	}
	if len(manifest) == 0 {
		return nil, fmt.Errorf("application manifest is empty")
	}
	for name, app := range manifest {
		if err := validateApplication(name, app, dataRoot); err != nil {
			return nil, err
		}
		app.Name = name
		manifest[name] = app
	}
	return manifest, nil
}

func validateApplication(name string, app Application, dataRoot string) error {
	if !appNamePattern.MatchString(name) {
		return fmt.Errorf("invalid application name %q", name)
	}
	if app.Name != "" && app.Name != name {
		return fmt.Errorf("application %q declares conflicting name %q", name, app.Name)
	}
	if strings.TrimSpace(app.DockerComposeRepo) == "" {
		return fmt.Errorf("application %q has no repository", name)
	}
	if strings.HasPrefix(app.DockerComposeRepo, "-") || strings.ContainsAny(app.DockerComposeRepo, "\r\n") {
		return fmt.Errorf("application %q has an unsafe repository", name)
	}
	if strings.TrimSpace(app.DockerComposeBranch) == "" {
		return fmt.Errorf("application %q has no repository ref", name)
	}
	if strings.TrimSpace(app.ComposeProjectName) == "" || strings.TrimSpace(app.SitectlContextName) == "" {
		return fmt.Errorf("application %q has incomplete sitectl identity", name)
	}
	root, err := filepath.Abs(filepath.Clean(dataRoot))
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	project, err := filepath.Abs(filepath.Clean(app.ProjectDir))
	if err != nil {
		return fmt.Errorf("resolve application %q directory: %w", name, err)
	}
	relative, err := filepath.Rel(root, project)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("application %q directory is outside data root", name)
	}
	if project != app.ProjectDir {
		return fmt.Errorf("application %q directory is not canonical", name)
	}
	if resolved, err := filepath.EvalSymlinks(project); err == nil && resolved != project {
		return fmt.Errorf("application %q directory contains a symbolic link", name)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("resolve application %q directory links: %w", name, err)
	}
	return nil
}

func (m Manifest) Names() []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a Application) Commands(lifecycle string) ([]string, error) {
	switch lifecycle {
	case "init":
		return a.InitCommands, nil
	case "up":
		return a.UpCommands, nil
	case "down":
		return a.DownCommands, nil
	case "rollout":
		return a.RolloutCommands, nil
	default:
		return nil, fmt.Errorf("unsupported lifecycle %q", lifecycle)
	}
}
