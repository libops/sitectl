package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
	yaml "gopkg.in/yaml.v3"
)

type ContextType string

const (
	ContextLocal  ContextType = "local"
	ContextRemote ContextType = "remote"
)

type Context struct {
	Name                string      `yaml:"name"`
	Site                string      `yaml:"site"`
	Plugin              string      `yaml:"plugin"`
	DockerHostType      ContextType `mapstructure:"type" yaml:"type"`
	Environment         string      `yaml:"environment,omitempty"`
	DockerSocket        string      `yaml:"docker-socket"`
	ProjectName         string      `yaml:"project-name"`
	ComposeProjectName  string      `yaml:"compose-project-name,omitempty"`
	ComposeNetwork      string      `yaml:"compose-network,omitempty"`
	ProjectDir          string      `yaml:"project-dir"`
	DrupalRootfs        string      `yaml:"drupal-rootfs,omitempty"`
	DrupalContainerRoot string      `yaml:"drupal-container-root,omitempty"`
	SSHUser             string      `yaml:"ssh-user"`
	SSHHostname         string      `yaml:"ssh-hostname,omitempty"`
	SSHPort             uint        `yaml:"ssh-port,omitempty"`
	SSHKeyPath          string      `yaml:"ssh-key,omitempty"`
	EnvFile             []string    `yaml:"env-file"`
	ComposeFile         []string    `yaml:"compose-file,omitempty"`

	// Database connection configuration
	DatabaseService        string `yaml:"database-service,omitempty"`
	DatabaseUser           string `yaml:"database-user,omitempty"`
	DatabasePasswordSecret string `yaml:"database-password-secret,omitempty"`
	DatabaseName           string `yaml:"database-name,omitempty"`

	ReadSmallFileFunc func(filename string) (string, error) `yaml:"-"`

	// Extra holds plugin-specific configuration.
	// Each plugin uses its own key (e.g., "drupal", "isle", "wordpress").
	Extra map[string]yaml.Node `yaml:"extra,omitempty"`
}

// FileReader defines the behavior needed to read small files.
type FileReader interface {
	ReadSmallFile(path string) (string, error)
}

var ErrContextNotFound = errors.New("context not found")

func ContextExists(name string) (bool, error) {
	c, err := Load()
	if err != nil {
		return false, err
	}

	for _, context := range c.Contexts {
		if strings.EqualFold(context.Name, name) {
			return true, nil
		}
	}

	return false, nil
}

func GetContext(name string) (Context, error) {
	ctx := Context{Name: name}
	c, err := Load()
	if err != nil {
		return ctx, err
	}

	for _, context := range c.Contexts {
		if strings.EqualFold(context.Name, name) {
			return context, nil
		}
	}

	return ctx, fmt.Errorf("%w: %s", ErrContextNotFound, name)
}

func (context Context) String() (string, error) {
	out, err := yaml.Marshal(context)
	if err != nil {
		return "", fmt.Errorf("unable to parse context: %v", err)
	}

	return string(out), nil
}

func SaveContext(ctx *Context, setDefault bool) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	// Set database defaults if not provided
	if ctx.DatabaseService == "" {
		ctx.DatabaseService = "mariadb"
	}
	if ctx.DatabaseUser == "" {
		ctx.DatabaseUser = "root"
	}
	if ctx.DatabasePasswordSecret == "" {
		ctx.DatabasePasswordSecret = "DB_ROOT_PASSWORD"
	}
	if ctx.DatabaseName == "" {
		ctx.DatabaseName = "drupal_default"
	}

	updated := false
	for i, c := range cfg.Contexts {
		if c.Name == ctx.Name {
			cfg.Contexts[i] = *ctx

			updated = true
			break
		}
	}

	if !updated {
		cfg.Contexts = append(cfg.Contexts, *ctx)
		if cfg.CurrentContext == "" {
			cfg.CurrentContext = ctx.Name
		}
	}

	if setDefault {
		cfg.CurrentContext = ctx.Name
	}

	return Save(cfg)
}

func CurrentContext(f *pflag.FlagSet) (*Context, error) {
	c, err := ResolveCurrentContextName(f)
	if err != nil {
		return nil, err
	}
	cfg, err := Load()
	if err != nil {
		return nil, fmt.Errorf("unable to load sitectl config. Have you ran `sitectl config set-context`?")
	}
	for _, context := range cfg.Contexts {
		if context.Name == c {
			return &context, nil
		}
	}

	return nil, fmt.Errorf("unable to set current context. Have you ran `sitectl config use-context`?")
}

func ResolveCurrentContextName(f *pflag.FlagSet) (string, error) {
	if f != nil && f.Lookup("context") != nil && f.Changed("context") {
		c, err := f.GetString("context")
		if err != nil {
			return "", fmt.Errorf("error getting context flag: %v", err)
		}
		if c == "default" {
			cfg, err := Load()
			if err != nil {
				return "", fmt.Errorf("unable to load sitectl config. Have you ran `sitectl config set-context`?")
			}
			return cfg.CurrentContext, nil
		}
		return c, nil
	}

	c, err := Current()
	if err != nil {
		return "", fmt.Errorf("unable to load sitectl config. Have you ran `sitectl config set-context`?")
	}
	return c, nil
}

func (c *Context) ReadSmallFile(filename string) (string, error) {
	if c.ReadSmallFileFunc != nil {
		return c.ReadSmallFileFunc(filename)
	}

	if c.DockerHostType == ContextLocal {
		data, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read file %q: %w", filename, err)
		}
		return string(data), nil
	}

	accessor, err := c.NewFileAccessor()
	if err != nil {
		return "", fmt.Errorf("create file accessor: %w", err)
	}
	defer accessor.Close()

	data, err := accessor.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("read remote file %q: %w", filename, err)
	}
	return string(data), nil
}

func (c Context) EffectiveComposeProjectName() string {
	return firstNonEmpty(c.ComposeProjectName, c.ProjectName)
}

func (c Context) EffectiveComposeNetwork() string {
	return firstNonEmpty(c.ComposeNetwork, c.EffectiveComposeProjectName()+"_default")
}

func (c *Context) DialSSH() (*ssh.Client, error) {
	key, err := os.ReadFile(c.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("error reading SSH key: %w", err)
	}

	// Try to parse the key without a passphrase first
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		// Check if the error is due to encryption (passphrase required)
		var ppErr *ssh.PassphraseMissingError
		if errors.As(err, &ppErr) {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return nil, fmt.Errorf("ssh key %s requires a passphrase, but no interactive terminal is available", c.SSHKeyPath)
			}
			// Key is encrypted, prompt for passphrase
			fmt.Printf("Enter passphrase for SSH key %s: ", c.SSHKeyPath)
			passphrase, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println() // Print newline after password input
			if err != nil {
				return nil, fmt.Errorf("error reading passphrase: %w", err)
			}

			// Try to parse with the passphrase
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, passphrase)
			if err != nil {
				return nil, fmt.Errorf("error parsing SSH key with passphrase: %w", err)
			}
		} else {
			return nil, fmt.Errorf("error parsing SSH key: %w", err)
		}
	}

	knownHostsPath, err := defaultKnownHostsPath()
	if err != nil {
		return nil, fmt.Errorf("error resolving known_hosts path: %w", err)
	}
	slog.Debug("Setting known_hosts", "known_hosts", knownHostsPath)
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("error creating known_hosts callback: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: c.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         5 * time.Second,
	}

	sshAddr := fmt.Sprintf("%s:%d", c.SSHHostname, c.SSHPort)
	slog.Debug("Dialing " + sshAddr)
	client, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				fmt.Println("The host key for your remote context is not known.")
				fmt.Println("This means your SSH known_hosts file doesn't have an entry for this host.")
			} else {
				fmt.Println("The host key for your remote context does not match the expected key.")
				fmt.Println("This might indicate that the host's key has changed or that there could be a security issue.")
				fmt.Println("Please verify the new key with your host administrator.")
				fmt.Println("If the change is legitimate, update your known_hosts file by removing the old key and adding the new one.")
			}
			fmt.Printf("\nTry running `ssh -p %d -t %s@%s` and trying again\n\n", c.SSHPort, c.SSHUser, c.SSHHostname)

		}
		return nil, fmt.Errorf("error dialing SSH at %s: %w", sshAddr, err)
	}

	return client, nil
}

func defaultKnownHostsPath() (string, error) {
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		home = u.HomeDir
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("unable to determine user home directory")
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

func (c *Context) ProjectDirExists() (bool, error) {
	if c.DockerHostType == ContextLocal {
		_, err := os.Stat(c.ProjectDir)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return true, err
		}

		return !os.IsNotExist(err), nil
	}

	accessor, err := c.NewFileAccessor()
	if err != nil {
		slog.Error("Error establishing SSH connection", "err", err)
		return false, err
	}
	defer accessor.Close()
	_, err = accessor.Stat(c.ProjectDir)
	if err != nil {
		return false, nil
	}

	return true, nil
}

func (cc *Context) VerifyRemoteInput(existingSite bool) error {
	testSsh := false
	if cc.SSHHostname == "" {
		question := []string{
			"What is the hostname the site is installed at? (e.g. docker.example.com): ",
		}
		h, err := GetInput(question...)
		if err != nil || h == "" {
			return fmt.Errorf("error reading input")
		}
		testSsh = true
		cc.SSHHostname = h
	}

	if cc.SSHUser == "" {
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("unable to determine current user: %v", err)
		}
		cc.SSHUser = u.Username
		question := []string{
			fmt.Sprintf("What username do you use to SSH into %s? [%s]: ", cc.SSHHostname, u.Username),
		}
		un, err := GetInput(question...)
		if err != nil {
			return fmt.Errorf("error reading input")
		}
		if un != "" {
			testSsh = true
			cc.SSHUser = un
		}
	}

	if cc.SSHPort == 2222 {
		question := []string{
			"You may have forgot to pass --ssh-port",
			"The default value is 2222, which might be a good default for local contexts",
			"You can enter the port to connect to [2222]: ",
		}
		if existingSite {
			question = []string{
				fmt.Sprintf("If you use a non-standard port to connect to %s over SSH enter it here: [22]: ", cc.SSHHostname),
			}
			cc.SSHPort = 22
		}
		p, err := GetInput(question...)
		if err != nil {
			return fmt.Errorf("error reading input")
		}
		if p != "" {
			port, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("unable to convert port to an integer: %v", err)
			}
			cc.SSHPort = uint(port)
			testSsh = true
		}
	}
	if cc.SSHKeyPath == "" {
		testSsh = true

		cc.SSHKeyPath = filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
		question := []string{
			"Path to your SSH private key",
			fmt.Sprintf("Used when you run ssh %s@%s", cc.SSHUser, cc.SSHHostname),
			fmt.Sprintf("Enter the full path here [%s]: ", cc.SSHKeyPath),
		}
		k, err := GetInput(question...)
		if err != nil {
			return fmt.Errorf("error reading input")
		}
		if k != "" {
			cc.SSHKeyPath = k
		}
		_, err = os.Stat(cc.SSHKeyPath)
		if os.IsNotExist(err) {
			return fmt.Errorf("SSH key does not exist: %s", cc.SSHKeyPath)
		} else if err != nil {
			return fmt.Errorf("could not determine if SSH key exists: %v", err)
		}
	}

	if testSsh {
		sshClient, err := cc.DialSSH()
		if err != nil {
			return fmt.Errorf("ssh config does not seem correct: %v", err)
		}
		sshClient.Close()
		fmt.Println("Tested SSH connection OK!")
	}

	if cc.EffectiveComposeProjectName() == "docker-compose" || cc.EffectiveComposeProjectName() == "" {
		question := []string{
			"What is the docker compose project name (COMPOSE_PROJECT_NAME in your .env)? [docker-compose]: ",
		}
		pn, err := GetInput(question...)
		if err != nil {
			return fmt.Errorf("error reading input")
		}
		if pn != "" {
			cc.ComposeProjectName = pn
		}
	}

	return nil
}

func (c *Context) UploadFile(source, destination string) error {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		slog.Error("Error establishing SSH connection", "err", err)
		return err
	}
	defer accessor.Close()
	return accessor.UploadFile(source, destination)
}

// GetSshUri returns an SSH connection URI
func (c *Context) GetSshUri() string {
	if c.DockerHostType == ContextLocal {
		return ""
	}

	sshPort := c.SSHPort
	if sshPort == 0 {
		sshPort = 22
	}

	values := url.Values{}
	values.Set("ssh_host", c.SSHHostname)
	values.Set("ssh_user", c.SSHUser)
	values.Set("ssh_port", strconv.FormatUint(uint64(sshPort), 10))
	if c.SSHKeyPath != "" {
		values.Set("ssh_keyLocation", c.SSHKeyPath)
		values.Set("ssh_keyLocationEnabled", "1")
	}

	return values.Encode()
}
