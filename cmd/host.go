package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libops/sitectl/internal/hostruntime"
	"github.com/spf13/cobra"
)

func hostCommand() *cobra.Command {
	options := struct {
		manifest string
		dataRoot string
	}{
		manifest: "/home/cloud-compose/compose-projects.json",
		dataRoot: "/mnt/disks/data",
	}
	command := &cobra.Command{
		Use:     "host",
		Short:   "Provision and operate a sitectl-managed Compose host",
		GroupID: "advanced",
		Long: `Provision and operate the current VM using sitectl's managed-host layout.

These commands run on the target host rather than through a saved sitectl context.
They manage host filesystems, runtime tools, application manifests, systemd units,
backups, credentials, and diagnostics. Most provisioning operations require root
and an existing host environment file and application manifest.`,
		Example: `  sudo sitectl host apps validate
  sudo sitectl host systemd ensure-bootstrap
  ssh root@compose.example.org sitectl host diagnostics status`,
	}
	command.PersistentFlags().StringVar(&options.manifest, "manifest", options.manifest, "Root-owned application manifest to operate")
	command.PersistentFlags().StringVar(&options.dataRoot, "data-root", options.dataRoot, "Persistent data root to provision and operate")
	command.AddCommand(hostAppsCommand(&options.manifest, &options.dataRoot))
	command.AddCommand(hostEnvCommand())
	command.AddCommand(hostKeysCommand(&options.manifest, &options.dataRoot))
	command.AddCommand(hostBackupCommand(&options.manifest, &options.dataRoot))
	command.AddCommand(hostMaintenanceCommand())
	command.AddCommand(hostDiagnosticsCommand(&options.manifest, &options.dataRoot))
	command.AddCommand(hostArtifactsCommand())
	command.AddCommand(hostRuntimeCommand())
	command.AddCommand(hostFilesystemCommand())
	command.AddCommand(hostOverlayCommand())
	command.AddCommand(hostMetadataFirewallCommand())
	command.AddCommand(hostVaultReadinessCommand())
	command.AddCommand(hostVaultAgentCommand())
	command.AddCommand(hostMarkerCommand())
	command.AddCommand(hostSystemdCommand())
	command.AddCommand(hostDockerPluginsCommand())
	command.AddCommand(hostSecurityCommand())
	command.AddCommand(hostRolloutServeCommand())
	command.AddCommand(&cobra.Command{
		Use:   "configure",
		Short: "Configure the managed host account and runtime paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return hostruntime.ConfigureHost(cmd.Context(), hostruntime.ConfigureOptions{
				Provider: os.Getenv("CLOUD_COMPOSE_PROVIDER"), RuntimeHome: "/home/cloud-compose",
				DataRoot: options.dataRoot, VolumesRoot: "/mnt/disks/volumes", InternalEnabled: truthy(os.Getenv("LIBOPS_INTERNAL_SERVICES_ENABLED")),
				Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			})
		},
	})
	return command
}

func hostRolloutServeCommand() *cobra.Command {
	return &cobra.Command{
		Use: "rollout-serve", Short: "Run the validated managed rollout service", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return hostruntime.ServeRollout(hostruntime.RolloutServeOptions{
				Port: os.Getenv("ROLLOUT_PORT"), JWKSURI: os.Getenv("ROLLOUT_JWKS_URI"), Audience: os.Getenv("ROLLOUT_JWT_AUD"),
				CustomClaims: os.Getenv("ROLLOUT_CUSTOM_CLAIMS"), Binary: firstEnvironment("CLOUD_COMPOSE_ROLLOUT_BIN", "/usr/local/bin/cloud-compose-rollout"),
			})
		},
	}
}

func hostVaultAgentCommand() *cobra.Command {
	options := hostruntime.VaultAgentOptions{}
	command := &cobra.Command{
		Use: "vault-agent init|assert-ready", Short: "Initialize or check the managed Vault Agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Enabled = firstEnvironment("VAULT_AGENT_ENABLED", "false")
			options.ReadyMarker = firstEnvironment("VAULT_AGENT_READY_MARKER", "/run/cloud-compose/vault-agent.ready")
			options.Readiness.TokenPath = firstEnvironment("VAULT_AGENT_TOKEN_PATH", "/mnt/disks/data/vault/token")
			options.Stdout, options.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			switch args[0] {
			case "init":
				return hostruntime.InitializeVaultAgent(cmd.Context(), options)
			case "assert-ready":
				return hostruntime.AssertVaultAgentReady(cmd.Context(), options)
			default:
				return fmt.Errorf("unknown Vault Agent operation %q", args[0])
			}
		},
	}
	return command
}

func hostSecurityCommand() *cobra.Command {
	var home string
	command := &cobra.Command{Use: "security", Short: "Enforce the managed host runtime trust boundary"}
	secure := &cobra.Command{
		Use:   "secure-runtime",
		Short: "Normalize and verify managed runtime ownership and modes",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return hostruntime.SecureRuntimeHome(hostruntime.SecureRuntimeOptions{Home: home})
		},
	}
	secure.Flags().StringVar(&home, "home", "/home/cloud-compose", "Managed host runtime home")
	command.AddCommand(secure)
	return command
}

func hostDockerPluginsCommand() *cobra.Command {
	options := hostruntime.DockerPluginOptions{}
	command := &cobra.Command{
		Use: "docker-plugins", Short: "Install verified Docker Compose and Buildx CLI plugins", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return hostruntime.InstallDockerPlugins(cmd.Context(), options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Directory, "directory", defaultEnvironment("DOCKER_CLI_PLUGIN_DIR", "/usr/local/lib/docker/cli-plugins"), "Docker CLI plugin directory")
	flags.StringVar(&options.ComposeVersion, "compose-version", defaultEnvironment("DOCKER_COMPOSE_VERSION", "v5.3.1"), "Docker Compose release version")
	flags.StringVar(&options.BuildxVersion, "buildx-version", defaultEnvironment("DOCKER_BUILDX_VERSION", "v0.35.0"), "Docker Buildx release version")
	return command
}

func hostMarkerCommand() *cobra.Command {
	command := &cobra.Command{Use: "marker", Short: "Manage Cloud Compose readiness markers"}
	command.AddCommand(&cobra.Command{
		Use: "valid PATH", Short: "Check whether a readiness marker is safe and valid", Args: cobra.ExactArgs(1), SilenceUsage: true, SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if !hostruntime.MarkerValid(args[0]) {
				return fmt.Errorf("invalid Cloud Compose readiness marker")
			}
			return nil
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "publish PATH", Short: "Publish a root-owned readiness marker", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return hostruntime.PublishMarker(args[0]) },
	})
	command.AddCommand(&cobra.Command{
		Use: "consume-fresh PATH IDENTITY", Short: "Validate and consume a fresh-filesystem marker", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error { return hostruntime.ConsumeFreshMarker(args[0], args[1]) },
	})
	command.AddCommand(&cobra.Command{
		Use: "app-status DURABLE CURRENT_BOOT", Short: "Report whether application initialization is required", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			status := "ready"
			if hostruntime.ApplicationNeedsInitialization(args[0], args[1]) {
				status = "initialize"
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), status)
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "require-initialized DURABLE CURRENT_BOOT", Short: "Require durable or current-boot application initialization", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if hostruntime.ApplicationNeedsInitialization(args[0], args[1]) {
				return fmt.Errorf("cloud-compose application initialization has not completed for this boot")
			}
			return nil
		},
	})
	return command
}

func hostSystemdCommand() *cobra.Command {
	var timeout time.Duration
	command := &cobra.Command{Use: "systemd", Short: "Converge Cloud Compose systemd services"}
	startWait := &cobra.Command{
		Use: "start-wait UNIT", Short: "Start a oneshot unit and wait for its terminal state", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return hostruntime.StartAndWaitSystemd(cmd.Context(), hostruntime.SystemdOptions{Unit: args[0], Timeout: timeout, Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()})
		},
	}
	startWait.Flags().DurationVar(&timeout, "timeout", 3*time.Hour, "Maximum wait for the oneshot service")
	command.AddCommand(startWait)
	var marker string
	ensure := &cobra.Command{
		Use: "ensure-bootstrap", Short: "Start host bootstrap when its readiness marker is absent", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("timeout") {
				if value := os.Getenv("CLOUD_COMPOSE_BOOTSTRAP_WAIT_SECONDS"); value != "" {
					seconds, err := strconv.Atoi(value)
					if err != nil || seconds < 1 {
						return fmt.Errorf("CLOUD_COMPOSE_BOOTSTRAP_WAIT_SECONDS must be a positive integer")
					}
					timeout = time.Duration(seconds) * time.Second
				}
			}
			return hostruntime.EnsureBootstrap(cmd.Context(), marker, hostruntime.SystemdOptions{Unit: "cloud-compose-bootstrap.service", Timeout: timeout, Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()})
		},
	}
	ensure.Flags().StringVar(&marker, "marker", defaultEnvironment("CLOUD_COMPOSE_BOOTSTRAP_COMPLETE_MARKER", "/var/lib/cloud-compose/bootstrap-complete"), "Durable bootstrap readiness marker")
	ensure.Flags().DurationVar(&timeout, "timeout", 3*time.Hour, "Maximum wait for host bootstrap")
	command.AddCommand(ensure)
	command.AddCommand(&cobra.Command{
		Use: "migrate-legacy", Short: "Remove retired Cloud Compose systemd units", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return hostruntime.MigrateLegacyUnits(cmd.Context(), firstEnvironment("CLOUD_COMPOSE_SYSTEMD_UNIT_DIR", "/etc/systemd/system"), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
	return command
}

func defaultEnvironment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func hostMetadataFirewallCommand() *cobra.Command {
	options := hostruntime.MetadataFirewallOptions{}
	command := &cobra.Command{
		Use:   "metadata-firewall [full|pre-docker]",
		Short: "Configure GCP host and container metadata isolation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Provider = os.Getenv("CLOUD_COMPOSE_PROVIDER")
			options.Mode = "full"
			if len(args) == 1 {
				options.Mode = args[0]
			}
			options.Stdout, options.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return hostruntime.ConfigureMetadataFirewall(cmd.Context(), options)
		},
	}
	return command
}

func hostVaultReadinessCommand() *cobra.Command {
	options := hostruntime.VaultReadinessOptions{}
	command := &cobra.Command{
		Use:   "vault-readiness prepare|wait|clear",
		Short: "Manage Vault Agent sink-token readiness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("timeout") {
				if value := os.Getenv("VAULT_AGENT_START_TIMEOUT_SECONDS"); value != "" {
					seconds, err := strconv.Atoi(value)
					if err != nil {
						return fmt.Errorf("VAULT_AGENT_START_TIMEOUT_SECONDS must be an integer: %w", err)
					}
					options.Timeout = time.Duration(seconds) * time.Second
				}
			}
			switch args[0] {
			case "prepare":
				return hostruntime.PrepareVaultReadiness(options)
			case "wait":
				return hostruntime.WaitForVaultReadiness(cmd.Context(), options)
			case "clear":
				return hostruntime.ClearVaultReadiness(options)
			default:
				return fmt.Errorf("unknown Vault readiness operation %q", args[0])
			}
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.SafeDir, "safe-dir", "/mnt/disks/data/vault", "Root-only Vault Agent state directory")
	flags.StringVar(&options.TokenPath, "token", firstEnvironment("VAULT_AGENT_TOKEN_PATH", "/mnt/disks/data/vault/token"), "Dedicated Vault Agent sink-token path")
	flags.StringVar(&options.ReadyMarker, "ready-marker", "/run/cloud-compose/vault-agent.ready", "Current-boot Vault Agent readiness marker")
	flags.DurationVar(&options.Timeout, "timeout", time.Minute, "Maximum wait for the Vault Agent sink token")
	return command
}

func hostFilesystemCommand() *cobra.Command {
	options := hostruntime.FilesystemOptions{}
	command := &cobra.Command{
		Use:   "filesystems",
		Short: "Prepare and persist managed host filesystems",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Stdout, options.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return hostruntime.PrepareFilesystems(cmd.Context(), options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.DataDevice, "data-device", "", "Block device dedicated to persistent application data")
	flags.StringVar(&options.VolumesDevice, "volumes-device", "", "Block device dedicated to Docker volume data")
	flags.StringVar(&options.OverlayDevice, "overlay-device", "", "Optional read-only production volume block device")
	flags.StringVar(&options.DataMount, "data-mount", "/mnt/disks/data", "Mount target for persistent application data")
	flags.StringVar(&options.VolumesMount, "volumes-mount", "/mnt/disks/volumes", "Mount target for Docker volume data")
	flags.StringVar(&options.OverlayMount, "overlay-mount", "/mnt/disks/prod-readonly", "Mount target for the optional read-only production disk")
	flags.StringVar(&options.FreshIdentity, "fresh-identity", "", "Identity recorded only when the data disk is first formatted")
	flags.StringVar(&options.ReadyMarker, "ready-marker", "", "Root-owned marker published after every required mount is verified")
	flags.StringVar(&options.FstabPath, "fstab", "/etc/fstab", "Filesystem table reconciled with the managed mount block")
	flags.StringVar(&options.FstabLockPath, "fstab-lock", "/run/cloud-compose-fstab.lock", "Lock serializing filesystem table updates")
	flags.StringVar(&options.SystemdDir, "systemd-dir", "/etc/systemd/system", "Directory containing provider-generated mount units")
	flags.DurationVar(&options.DeviceWait, "device-wait", 10*time.Minute, "Maximum wait for each declared block device")
	flags.DurationVar(&options.AutomountWait, "automount-wait", time.Minute, "Maximum wait for provider automount activity")
	_ = command.MarkFlagRequired("data-device")
	_ = command.MarkFlagRequired("volumes-device")
	return command
}

func hostOverlayCommand() *cobra.Command {
	options := hostruntime.OverlayOptions{}
	command := &cobra.Command{
		Use:   "overlays",
		Short: "Mount declared read-only production volume overlays",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv("CLOUD_COMPOSE_PROVIDER") != "gcp" && len(options.Volumes) != 0 {
				return fmt.Errorf("docker volume overlays are supported only on GCP")
			}
			options.Stdout, options.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return hostruntime.MountOverlays(cmd.Context(), options)
		},
	}
	command.Flags().StringVar(&options.VolumesRoot, "volumes-root", firstEnvironment("CLOUD_COMPOSE_VOLUMES_ROOT", "/mnt/disks/volumes"), "Writable Docker volume root")
	command.Flags().StringVar(&options.LowerRoot, "lower-root", firstEnvironment("CLOUD_COMPOSE_OVERLAY_LOWER_ROOT", "/mnt/disks/prod-readonly"), "Read-only production volume root")
	command.Flags().StringSliceVar(&options.Volumes, "volume", strings.Fields(os.Getenv("DOCKER_VOLUME_OVERLAYS")), "Docker volume name to overlay; may be repeated")
	command.Flags().BoolVar(&options.Reset, "reset", false, "Unmount and clear the writable overlay layer before remounting")
	return command
}

func hostRuntimeCommand() *cobra.Command {
	var stateDir, publishedDir, artifactManifest string
	command := &cobra.Command{Use: "runtime", Short: "Install the managed sitectl runtime"}
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the complete verified sitectl package and artifact set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("managed runtime installation must run as root")
			}
			versions := map[string]string{}
			if raw := firstEnvironment("SITECTL_PACKAGE_VERSIONS", "{}"); raw != "" {
				if err := json.Unmarshal([]byte(raw), &versions); err != nil {
					return fmt.Errorf("SITECTL_PACKAGE_VERSIONS must be a JSON object of strings: %w", err)
				}
				if versions == nil {
					return fmt.Errorf("SITECTL_PACKAGE_VERSIONS must be a JSON object of strings")
				}
			}
			return hostruntime.InstallManagedRuntime(cmd.Context(), hostruntime.RuntimeInstallOptions{
				StateDir: stateDir, PublishedDir: publishedDir, Packages: strings.Fields(os.Getenv("SITECTL_PACKAGES")), Versions: versions,
				Fallback: firstEnvironment("SITECTL_VERSION", "latest"), GitHubOwner: firstEnvironment("SITECTL_GITHUB_OWNER", "libops"),
				Artifact: hostruntime.ArtifactInstallOptions{Manifest: artifactManifest, StateDir: filepath.Join(stateDir, "artifacts")},
			})
		},
	}
	install.Flags().StringVar(&stateDir, "state-dir", "/mnt/disks/data/libops-managed", "Root-owned managed runtime state")
	install.Flags().StringVar(&publishedDir, "published-dir", "/home/cloud-compose/bin", "Published managed command directory")
	install.Flags().StringVar(&artifactManifest, "artifact-manifest", "/home/cloud-compose/managed-runtime-artifacts.tsv", "Root-owned managed artifact manifest")
	command.AddCommand(install)
	return command
}

func hostArtifactsCommand() *cobra.Command {
	var manifest, stateDir string
	command := &cobra.Command{Use: "artifacts", Short: "Install verified managed host artifacts"}
	install := &cobra.Command{
		Use:   "install",
		Short: "Install every artifact in the root-owned manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("managed artifact installation must run as root")
			}
			return hostruntime.InstallManagedArtifacts(cmd.Context(), hostruntime.ArtifactInstallOptions{Manifest: manifest, StateDir: stateDir})
		},
	}
	install.Flags().StringVar(&manifest, "manifest", "/home/cloud-compose/managed-runtime-artifacts.tsv", "Root-owned managed artifact manifest")
	install.Flags().StringVar(&stateDir, "state-dir", "/mnt/disks/data/libops-managed/artifacts", "Managed artifact audit state directory")
	command.AddCommand(install)
	return command
}

func hostMaintenanceCommand() *cobra.Command {
	command := &cobra.Command{Use: "maintenance", Short: "Run managed host maintenance"}
	command.AddCommand(&cobra.Command{
		Use:   "docker-prune",
		Short: "Prune old unused Docker data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !truthy(os.Getenv("CLOUD_COMPOSE_DOCKER_PRUNE_ENABLED")) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cloud Compose Docker pruning is disabled")
				return nil
			}
			return hostruntime.PruneDocker(cmd.Context(), firstEnvironment("CLOUD_COMPOSE_DOCKER_PRUNE_UNTIL", "168h"), firstEnvironment("CLOUD_COMPOSE_DOCKER_PRUNE_LOCK_PATH", "/run/cloud-compose-docker-prune.lock"), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
	return command
}

func hostDiagnosticsCommand(manifestPath, dataRoot *string) *cobra.Command {
	command := &cobra.Command{Use: "diagnostics", Short: "Inspect managed host provisioning state"}
	command.AddCommand(&cobra.Command{
		Use:   "state",
		Short: "Print the bootstrap state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state, err := hostruntime.BootstrapState(cmd.Context(), "/var/lib/cloud-compose/bootstrap-complete")
			if err == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), state)
			}
			return err
		},
	})
	for _, action := range []string{"status", "dump"} {
		action := action
		command.AddCommand(&cobra.Command{
			Use:   action,
			Short: "Print managed host " + action,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				manifest := hostruntime.Manifest{}
				if action == "dump" {
					loaded, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
					if err == nil {
						manifest = loaded
					}
				}
				return hostruntime.WriteDiagnostics(cmd.Context(), manifest, action == "dump", cmd.OutOrStdout())
			},
		})
	}
	return command
}

func hostBackupCommand(manifestPath, dataRoot *string) *cobra.Command {
	command := &cobra.Command{Use: "backup", Short: "Run managed backup and recovery operations"}
	command.AddCommand(&cobra.Command{
		Use:   "mariadb",
		Short: "Create daily MariaDB recovery artifacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
			if err != nil {
				return err
			}
			options, err := backupOptions(cmd)
			if err != nil {
				return err
			}
			return hostruntime.RunMariaDBBackups(cmd.Context(), manifest, options)
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "offhost",
		Short: "Hand complete recovery coverage to the off-host driver",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !truthy(os.Getenv("CLOUD_COMPOSE_OFFHOST_BACKUP_REQUIRED")) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Off-host disaster recovery is not required")
				return nil
			}
			manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
			if err != nil {
				return err
			}
			options, err := backupOptions(cmd)
			if err != nil {
				return err
			}
			return hostruntime.RunOffhostBackup(cmd.Context(), manifest, options)
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "restore-test",
		Short: "Prove the latest off-host backup in disposable recovery",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !truthy(os.Getenv("CLOUD_COMPOSE_OFFHOST_BACKUP_REQUIRED")) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Off-host disaster recovery is not required")
				return nil
			}
			options, err := backupOptions(cmd)
			if err != nil {
				return err
			}
			return hostruntime.RunRestoreTest(cmd.Context(), options)
		},
	})
	return command
}

func backupOptions(cmd *cobra.Command) (hostruntime.BackupOptions, error) {
	retention := 14
	if value := os.Getenv("MARIADB_BACKUP_RETENTION_DAYS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return hostruntime.BackupOptions{}, fmt.Errorf("MARIADB_BACKUP_RETENTION_DAYS must be positive")
		}
		retention = parsed
	}
	return hostruntime.BackupOptions{
		Root: firstEnvironment("MARIADB_BACKUP_ROOT", "/mnt/disks/data/backups/mariadb"), RetentionDays: retention,
		StateRoot: firstEnvironment("CLOUD_COMPOSE_DR_STATE_ROOT", "/mnt/disks/data/.cloud-compose-disaster-recovery"),
		Driver:    firstEnvironment("CLOUD_COMPOSE_OFFHOST_BACKUP_DRIVER", "/etc/cloud-compose/libexec/offhost-backup-driver"),
		DataRoot:  firstEnvironment("CLOUD_COMPOSE_DATA_ROOT", "/mnt/disks/data"), VolumesRoot: firstEnvironment("CLOUD_COMPOSE_VOLUMES_ROOT", "/mnt/disks/volumes"),
		Provider: firstEnvironment("CLOUD_COMPOSE_PROVIDER", "unknown"), Instance: firstEnvironment("CLOUD_COMPOSE_INSTANCE_NAME", "cloud-compose"),
		Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
	}, nil
}

func hostKeysCommand(manifestPath, dataRoot *string) *cobra.Command {
	command := &cobra.Command{Use: "keys", Short: "Rotate managed GCP service-account keys"}
	for _, target := range []string{"app", "internal"} {
		target := target
		targetCommand := &cobra.Command{Use: target, Short: "Operate " + target + " service-account credentials"}
		for _, action := range []string{"rotate", "rollback", "retire"} {
			action := action
			if target == "internal" && action == "retire" {
				continue
			}
			targetCommand.AddCommand(&cobra.Command{
				Use:   action,
				Short: action + " " + target + " service-account credentials",
				Args:  cobra.NoArgs,
				RunE: func(cmd *cobra.Command, _ []string) error {
					return runManagedKeyAction(cmd, target, action, *manifestPath, *dataRoot)
				},
			})
		}
		command.AddCommand(targetCommand)
	}
	command.AddCommand(&cobra.Command{
		Use:   "daily",
		Short: "Converge every enabled managed service-account key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv("CLOUD_COMPOSE_PROVIDER") != "gcp" {
				return nil
			}
			if truthy(os.Getenv("LIBOPS_INTERNAL_SERVICES_ENABLED")) {
				if err := runManagedKeyAction(cmd, "internal", "rotate", *manifestPath, *dataRoot); err != nil {
					return err
				}
			}
			if truthy(os.Getenv("GCP_APP_CREDENTIALS_ENABLED")) {
				return runManagedKeyAction(cmd, "app", "rotate", *manifestPath, *dataRoot)
			}
			return nil
		},
	})
	return command
}

func runManagedKeyAction(cmd *cobra.Command, target, action, manifestPath, dataRoot string) error {
	minimumAge, err := secondsEnvironment("ROTATION_MIN_AGE_SECONDS", 86400)
	if err != nil {
		return err
	}
	disableGrace, err := secondsEnvironment("ROTATION_DISABLE_GRACE_SECONDS", 86400)
	if err != nil {
		return err
	}
	project := os.Getenv("GCP_PROJECT")
	options := hostruntime.KeyRotationOptions{
		ProjectID: project, MinimumAge: minimumAge, DisableGrace: disableGrace,
		Group:         os.Getenv("ROTATION_CREDENTIAL_GROUP"),
		FreshMarker:   firstEnvironment("CLOUD_COMPOSE_FRESH_FILESYSTEM_MARKER", "/mnt/disks/data/.cloud-compose/fresh-filesystem"),
		FreshIdentity: os.Getenv("CLOUD_COMPOSE_FRESH_FILESYSTEM_IDENTITY"), Stdout: cmd.OutOrStdout(),
	}
	if options.Group == "" {
		options.Group = "cloud-compose"
	}
	switch target {
	case "app":
		manifest, loadErr := hostruntime.LoadManifest(manifestPath, dataRoot)
		if loadErr != nil {
			return loadErr
		}
		options.ServiceAccount = os.Getenv("GCP_APP_SERVICE_ACCOUNT_EMAIL")
		options.CredentialsFile = firstEnvironment("APP_CREDENTIALS_FILE", "/mnt/disks/data/cloud-compose/app/GOOGLE_APPLICATION_CREDENTIALS")
		options.Owner = firstEnvironment("ROTATION_CENTRAL_CREDENTIAL_OWNER", "root")
		options.RestartUnit = "cloud-compose.service"
		options.AllowOrphanReconcile = truthy(os.Getenv("GCP_APP_SERVICE_ACCOUNT_MANAGED"))
		copyOwner := firstEnvironment("ROTATION_CREDENTIAL_OWNER", "100")
		for _, name := range manifest.Names() {
			options.Copies = append(options.Copies, hostruntime.CredentialCopy{Path: manifest[name].ProjectDir + "/secrets/GOOGLE_APPLICATION_CREDENTIALS", Owner: copyOwner, Group: options.Group})
		}
	case "internal":
		instance := os.Getenv("GCP_INSTANCE_NAME")
		if instance == "" {
			return fmt.Errorf("GCP_INSTANCE_NAME is required")
		}
		options.ServiceAccount = "internal-" + instance + "@" + project + ".iam.gserviceaccount.com"
		options.CredentialsFile = firstEnvironment("INTERNAL_CREDENTIALS_FILE", "/mnt/disks/data/libops-internal/GOOGLE_APPLICATION_CREDENTIALS")
		options.Owner = firstEnvironment("ROTATION_CREDENTIAL_OWNER", "100")
		options.RestartUnit = "cloud-compose-internal-services.service"
		options.AllowOrphanReconcile = true
	default:
		return fmt.Errorf("unknown key target %q", target)
	}
	switch action {
	case "rotate":
		return hostruntime.ConvergeKeyRotation(cmd.Context(), options)
	case "rollback":
		return hostruntime.RollbackKeyRotation(cmd.Context(), options)
	case "retire":
		return hostruntime.RetireKeyCredentials(cmd.Context(), options)
	default:
		return fmt.Errorf("unknown key action %q", action)
	}
}

func secondsEnvironment(name string, fallback int64) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return time.Duration(fallback) * time.Second, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return time.Duration(seconds) * time.Second, nil
}

func truthy(value string) bool {
	switch strings.ToLower(value) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func firstEnvironment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func hostEnvCommand() *cobra.Command {
	command := &cobra.Command{Use: "env", Short: "Manage host-owned environment files"}
	set := &cobra.Command{
		Use:   "set FILE NAME VALUE",
		Short: "Atomically set a host environment value",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			return hostruntime.SetRuntimeEnv(args[0], args[1], args[2])
		},
	}
	composeSet := &cobra.Command{
		Use:   "compose-set FILE NAME VALUE",
		Short: "Atomically set a managed Compose environment override",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			return hostruntime.SetComposeEnv(args[0], args[1], args[2], "# cloud-compose managed: ")
		},
	}
	sync := &cobra.Command{
		Use:   "compose-sync FILE JSON_FILE",
		Short: "Reconcile application environment data into a Compose dotenv file",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return hostruntime.SyncComposeEnv(args[0], args[1])
		},
	}
	command.AddCommand(set, composeSet, sync)
	return command
}

func hostAppsCommand(manifestPath, dataRoot *string) *cobra.Command {
	command := &cobra.Command{Use: "apps", Short: "Operate the host application manifest"}
	command.AddCommand(
		&cobra.Command{
			Use:   "validate",
			Short: "Validate the host application manifest",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "validated %d applications\n", len(manifest))
				return nil
			},
		},
		hostAppsPrepareCommand(manifestPath, dataRoot),
		hostAppsConvergeFilesystemsCommand(manifestPath, dataRoot),
		hostAppsLifecycleCommand(manifestPath, dataRoot),
	)
	return command
}

func hostAppsConvergeFilesystemsCommand(manifestPath, dataRoot *string) *cobra.Command {
	var account string
	command := &cobra.Command{
		Use:   "converge-filesystems",
		Short: "Converge managed project ownership and modes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
			if err != nil {
				return err
			}
			return (hostruntime.Apps{Manifest: manifest}).ConvergeFilesystems(account)
		},
	}
	command.Flags().StringVar(&account, "account", "cloud-compose", "Runtime account that owns managed application files")
	return command
}

func hostAppsPrepareCommand(manifestPath, dataRoot *string) *cobra.Command {
	var app string
	command := &cobra.Command{
		Use:   "prepare",
		Short: "Prepare verified application source checkouts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
			if err != nil {
				return err
			}
			names := manifest.Names()
			if app != "" {
				names = []string{app}
			}
			return (hostruntime.Apps{Manifest: manifest, Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}).PrepareSources(cmd.Context(), names)
		},
	}
	command.Flags().StringVar(&app, "app", "", "Prepare only the named manifest application")
	return command
}

func hostAppsLifecycleCommand(manifestPath, dataRoot *string) *cobra.Command {
	options := struct{ app string }{}
	var lockPath string
	command := &cobra.Command{
		Use:   "lifecycle init|up|down|rollout",
		Short: "Run a validated lifecycle for managed applications",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := hostruntime.AcquireLock(lockPath)
			if err != nil {
				return err
			}
			defer lock.Close()
			manifest, err := hostruntime.LoadManifest(*manifestPath, *dataRoot)
			if err != nil {
				return err
			}
			names := manifest.Names()
			if options.app == "" && args[0] == "rollout" {
				options.app = os.Getenv("CLOUD_COMPOSE_PRIMARY_APP")
			}
			if options.app != "" {
				names = []string{options.app}
			}
			runtime := hostruntime.Apps{Manifest: manifest, Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()}
			for _, name := range names {
				if err := runtime.RunLifecycle(cmd.Context(), name, args[0]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.app, "app", os.Getenv("CLOUD_COMPOSE_APP"), "Run only the named manifest application")
	command.Flags().StringVar(&lockPath, "lock", "/mnt/disks/data/.cloud-compose-lifecycle.lock", "Host lifecycle lock file")
	return command
}
