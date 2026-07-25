package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/libops/sitectl/pkg/ui"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	yaml "gopkg.in/yaml.v3"
)

func GetInput(question ...string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) && len(question) > 0 {
		prompt := question[len(question)-1]
		sections := append([]string{}, question[:len(question)-1]...)
		if value, ok, err := ui.PromptText(ui.TextPromptOptions{
			Sections: sections,
			Prompt:   prompt,
		}); ok {
			return value, err
		}
	}

	reader := bufio.NewReader(os.Stdin)
	out := os.Stdout
	if os.Getenv("SITECTL_RPC") != "" {
		out = os.Stderr
	}
	lastItemIndex := len(question) - 1
	for i := range question {
		if i == lastItemIndex {
			fmt.Fprint(out, question[i])
			continue
		}
		fmt.Fprintln(out, question[i])
	}
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("unable to read from stdin: %v", err)
	}
	input = strings.TrimSpace(input)
	fmt.Fprintln(out)
	return input, nil
}

func LoadFromFlags(f *pflag.FlagSet, context Context) (*Context, error) {
	t := reflect.TypeOf(Context{})
	exists := contextHasStoredValues(context)
	slog.Debug("Loading context from flags", "exists", exists)
	m := make(map[string]interface{}, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("yaml")
		if tag == "" || tag == "name" || tag == "-" {
			continue
		}
		tag = strings.Split(tag, ",")[0]
		if f.Lookup(tag) == nil {
			continue
		}

		// Skip map types as they're not supported as flags
		if field.Type.Kind() == reflect.Map {
			continue
		}

		// if we're loading flags for an existing context
		// do not add default values
		if exists && !f.Changed(tag) {
			continue
		}

		var value interface{}
		switch field.Type.Kind() {
		case reflect.Bool:
			v, err := f.GetBool(tag)
			if err != nil {
				return nil, fmt.Errorf("error getting flag %q: %w", tag, err)
			}
			value = v

		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			v, err := f.GetUint(tag)
			if err != nil {
				return nil, fmt.Errorf("error getting flag %q: %w", tag, err)
			}
			value = v
		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.String {
				v, err := f.GetStringSlice(tag)
				if err != nil {
					return nil, fmt.Errorf("error getting string slice flag %q: %w", tag, err)
				}
				value = v
			}
		default:
			v, err := f.GetString(tag)
			if err != nil {
				return nil, fmt.Errorf("error getting flag %q: %w", tag, err)
			}
			value = v
		}

		slog.Debug("Setting tag", "tag", tag, "value", value)
		m[tag] = value
	}

	data, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}

	cc := context
	if err := yaml.Unmarshal(data, &cc); err != nil {
		return nil, err
	}
	if legacyFlag := f.Lookup("project-name"); legacyFlag != nil && legacyFlag.Changed {
		legacyValue, getErr := f.GetString("project-name")
		if getErr != nil {
			return nil, fmt.Errorf("error getting flag %q: %w", "project-name", getErr)
		}
		composeFlag := f.Lookup("compose-project-name")
		if composeFlag == nil || !composeFlag.Changed {
			cc.ComposeProjectName = strings.TrimSpace(legacyValue)
		}
		if strings.TrimSpace(cc.Site) == "" {
			cc.Site = strings.TrimSpace(legacyValue)
		}
	}
	cc.mergeLegacyProjectName("")

	return &cc, nil
}

func contextHasStoredValues(context Context) bool {
	return context.Site != "" ||
		context.Plugin != "" ||
		context.DockerHostType != "" ||
		context.Environment != "" ||
		context.DockerSocket != "" ||
		context.ProjectName != "" ||
		context.ComposeProjectName != "" ||
		context.ComposeNetwork != "" ||
		context.ProjectDir != "" ||
		context.SSHUser != "" ||
		context.SSHHostname != "" ||
		context.SSHPort != 0 ||
		context.SSHKeyPath != "" ||
		len(context.EnvFile) > 0 ||
		len(context.ComposeFile) > 0 ||
		context.DatabaseService != "" ||
		context.DatabaseUser != "" ||
		context.DatabasePasswordSecret != "" ||
		context.DatabaseName != "" ||
		len(context.Extra) > 0
}

// for local contexts, try a bunch of common paths grab the docker socket
// this is mostly needed for Mac OS
func GetDefaultLocalDockerSocket(dockerSocket string) string {
	macOsSocket := filepath.Join(os.Getenv("HOME"), ".docker/run/docker.sock")
	if isDockerSocketAlive(macOsSocket) {
		return macOsSocket
	}

	tried := []string{macOsSocket}
	if isDockerSocketAlive(dockerSocket) {
		return strings.TrimPrefix(dockerSocket, "unix://")
	}

	dockerSocket = os.Getenv("DOCKER_HOST")
	if isDockerSocketAlive(dockerSocket) {
		return strings.TrimPrefix(dockerSocket, "unix://")
	}

	tried = append(tried, dockerSocket)
	slog.Error("Unable to determine docker socket from any common paths", "testedSockets", tried)
	return ""
}

func isDockerSocketAlive(socket string) bool {
	socket, ok := normalizeDockerSocketPath(socket)
	if !ok {
		return false
	}
	conn, err := net.DialTimeout("unix", socket, 1*time.Second) // #nosec G704 -- socket is normalized to an absolute Unix socket path before dialing.
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func normalizeDockerSocketPath(socket string) (string, bool) {
	socket = strings.TrimSpace(socket)
	socket = strings.TrimPrefix(socket, "unix://")
	if socket == "" || strings.ContainsRune(socket, 0) {
		return "", false
	}
	if !filepath.IsAbs(socket) {
		return "", false
	}
	return filepath.Clean(socket), true
}

func SetCommandFlags(flags *pflag.FlagSet) {
	if path, err := os.Getwd(); err == nil {
		_ = godotenv.Load(filepath.Join(path, ".env"))
	}

	key := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")

	// NB: these flags must match the corresponding config.Context yaml struct tag
	// though we can add additional flags that have no match for additional functionality
	// in the command logic (e.g. default)
	flags.String("docker-socket", "/var/run/docker.sock", "Unix socket where Docker accepts API requests on the target machine.")
	flags.String("type", "local", "Execution target: local runs Docker on this machine; remote runs it over SSH.")
	flags.String("ssh-hostname", "", "DNS name or IP address of the remote machine that hosts the stack.")
	flags.Uint("ssh-port", 22, "TCP port used to establish the remote SSH connection.")
	flags.String("ssh-user", "", "Remote account whose permissions sitectl uses for Docker and filesystem operations.")
	flags.String("ssh-key", "", "Private key used to authenticate to the remote host; the key contents are not stored in the context. Example: "+key)
	flags.String("project-dir", "", "Docker Compose project directory on the target machine; relative Compose and env files resolve from here.")
	flags.String("site", "", "Logical site shared by related environment contexts, such as museum-local and museum-prod.")
	flags.String("plugin", "core", "Application plugin that owns stack-specific behavior for this context, such as isle or drupal.")
	flags.String("project-name", "", "Deprecated compatibility alias for --compose-project-name.")
	if err := flags.MarkDeprecated("project-name", "use --site and --compose-project-name instead"); err != nil {
		panic(fmt.Sprintf("deprecate project-name flag: %v", err))
	}
	flags.String("compose-project-name", "", "Docker Compose identity used to name and label this stack's containers, volumes, and networks.")
	flags.String("compose-network", "", "Primary Docker Compose network sitectl uses to resolve and connect to stack services.")
	flags.String("environment", "", "Deployment label within the site, such as local, dev, staging, or prod.")
	flags.Bool("sudo", false, "Deprecated compatibility flag; remote execution uses the configured SSH account's Docker permissions.")
	if err := flags.MarkDeprecated("sudo", "configure Docker access for the SSH account instead"); err != nil {
		panic(fmt.Sprintf("deprecate sudo flag: %v", err))
	}
	flags.StringSlice("env-file", []string{}, "Environment files passed to Docker Compose in this order; paths resolve on the target machine.")
	flags.StringSliceP("compose-file", "f", []string{}, "Compose files passed as Docker Compose -f options in this order; may be repeated.")
	flags.String("database-service", "mariadb", "Compose service that shared database backup, restore, and sync jobs execute against.")
	flags.String("database-user", "root", "Database account shared jobs use inside the database service; no password is stored here.")
	flags.String("database-password-secret", "DB_ROOT_PASSWORD", "Compose secret name shared jobs read for the database password; this stores the reference, not the password.")
	flags.String("database-name", "drupal_default", "Default database schema shared backup, restore, and sync jobs operate on.")
}
