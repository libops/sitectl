package cmd

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type siteBackupManifest struct {
	CreatedAt string            `yaml:"createdAt"`
	Database  string            `yaml:"database"`
	Volumes   map[string]string `yaml:"volumes"`
}

func init() { RootCmd.AddCommand(backupCommand()) }

func backupCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create or restore a full-site database and file-volume backup", GroupID: "ops"}
	cmd.AddCommand(backupCreateCommand(), backupRestoreCommand())
	return cmd
}

func backupCreateCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "create", Short: "Back up MariaDB and application file volumes", Args: cobra.NoArgs,
		Long: "Create a compressed logical MariaDB dump plus tar archives for named Compose volumes not mounted by MariaDB. Cache and index volumes are included so the artifact is complete.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			if output == "" {
				output = filepath.Join("backups", time.Now().UTC().Format("20060102T150405Z"))
			}
			backupDir, err := safeProjectRelativePath(ctx, output)
			if err != nil {
				return err
			}
			if _, err := ctx.RunCommandContext(cmd.Context(), exec.Command("mkdir", "-p", backupDir)); err != nil { // #nosec G204 -- confined project path is a separate argument and remote execution shell-quotes it.
				return fmt.Errorf("create backup directory: %w", err)
			}
			databasePath := filepath.Join(backupDir, "mariadb.sql.gz")
			if err := runMariaDBBackup(cmd, ctx, mariaDBBackupOptions{service: firstNonBlank(ctx.DatabaseService, defaultMariaDBService), output: databasePath, allDatabases: true, compress: true}); err != nil {
				return err
			}
			compose, err := inspectComposeSecrets(cmd, ctx)
			if err != nil {
				return err
			}
			image := compose.Services["init"].Image
			if image == "" {
				return fmt.Errorf("compose init service must declare an image for volume backup tooling")
			}
			excluded := databaseVolumeSources(compose, firstNonBlank(ctx.DatabaseService, defaultMariaDBService))
			manifest := siteBackupManifest{CreatedAt: time.Now().UTC().Format(time.RFC3339), Database: "mariadb.sql.gz", Volumes: map[string]string{}}
			names := make([]string, 0, len(compose.Volumes))
			for name := range compose.Volumes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, logical := range names {
				if excluded[logical] {
					continue
				}
				actual := compose.Volumes[logical].Name
				if actual == "" {
					actual = compose.Name + "_" + logical
				}
				archive := "volume-" + sanitizeArtifactPart(logical) + ".tar.gz"
				if err := runBackupContainer(cmd, ctx, image, actual, backupDir, archive, false); err != nil {
					return err
				}
				manifest.Volumes[logical] = archive
			}
			encoded, err := yaml.Marshal(manifest)
			if err != nil {
				return err
			}
			if err := ctx.WriteFile(filepath.Join(backupDir, "manifest.yaml"), encoded); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Full-site backup created at %s\n", backupDir)
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Project-relative backup directory; defaults to backups/YYYY-MM-DDTHHMMSSZ.")
	return cmd
}

func backupRestoreCommand() *cobra.Command {
	var yolo bool
	cmd := &cobra.Command{Use: "restore DIRECTORY", Short: "Replace the site's database and file volumes from a full-site backup", Args: cobra.ExactArgs(1),
		Long: "Stop the Compose stack, replace file-volume contents, restore the MariaDB dump, and start the stack. This destroys current site data and requires --yolo.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yolo {
				return fmt.Errorf("backup restore replaces current site data; rerun with --yolo after verifying the backup")
			}
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			backupDir, err := safeProjectRelativePath(ctx, args[0])
			if err != nil {
				return err
			}
			data, err := ctx.ReadFile(filepath.Join(backupDir, "manifest.yaml"))
			if err != nil {
				return err
			}
			var manifest siteBackupManifest
			if err := yaml.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("parse backup manifest: %w", err)
			}
			if filepath.Base(manifest.Database) != manifest.Database || manifest.Database == "." || strings.HasPrefix(manifest.Database, "-") {
				return fmt.Errorf("backup manifest contains an unsafe database archive path")
			}
			compose, err := inspectComposeSecrets(cmd, ctx)
			if err != nil {
				return err
			}
			image := compose.Services["init"].Image
			if image == "" {
				return fmt.Errorf("compose init service must declare an image for restore tooling")
			}
			if err := runContextCompose(cmd, *ctx, []string{"down"}); err != nil {
				return err
			}
			for logical, archive := range manifest.Volumes {
				if filepath.Base(archive) != archive || archive == "." || strings.HasPrefix(archive, "-") {
					return fmt.Errorf("backup manifest contains unsafe archive path for volume %s", logical)
				}
				volume, ok := compose.Volumes[logical]
				if !ok {
					return fmt.Errorf("backup volume %s is not declared by current Compose config", logical)
				}
				actual := volume.Name
				if actual == "" {
					actual = compose.Name + "_" + logical
				}
				if err := runBackupContainer(cmd, ctx, image, actual, backupDir, archive, true); err != nil {
					return err
				}
			}
			if err := runContextCompose(cmd, *ctx, []string{"up", "-d", firstNonBlank(ctx.DatabaseService, defaultMariaDBService)}); err != nil {
				return err
			}
			if err := runMariaDBImport(cmd, ctx, mariaDBImportOptions{service: firstNonBlank(ctx.DatabaseService, defaultMariaDBService), input: filepath.Join(backupDir, manifest.Database), yolo: true}); err != nil {
				return err
			}
			return runContextCompose(cmd, *ctx, []string{"up", "--remove-orphans", "--wait", "--wait-timeout", "600", "-d"})
		},
	}
	cmd.Flags().BoolVar(&yolo, "yolo", false, "Skip the destructive restore safeguard and replace current database and volume contents.")
	return cmd
}

func safeProjectRelativePath(ctx *config.Context, value string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup path must be a project-relative directory")
	}
	return filepath.Join(ctx.ProjectDir, clean), nil
}
func databaseVolumeSources(compose composeSecretConfig, service string) map[string]bool {
	out := map[string]bool{}
	for _, volume := range compose.Services[service].Volumes {
		if volume.Type == "volume" {
			out[volume.Source] = true
		}
	}
	return out
}
func runBackupContainer(cmd *cobra.Command, ctx *config.Context, image, volume, backupDir, archive string, restore bool) error {
	if strings.TrimSpace(image) == "" || strings.HasPrefix(image, "-") || strings.TrimSpace(volume) == "" || strings.HasPrefix(volume, "-") {
		return fmt.Errorf("backup image and volume must be non-empty safe values")
	}
	script := "tar -C /source -czf /backup/$1 ."
	mount := volume + ":/source:ro"
	if restore {
		script = "find /source -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -C /source -xzf /backup/$1"
		mount = volume + ":/source:rw"
	}
	command := exec.Command("docker", "run", "--rm", "-v", mount, "-v", backupDir+":/backup:rw", image, "sh", "-euc", script, "sitectl-backup", archive) // #nosec G204 -- validated values are argv entries; the fixed shell script only reads positional $1.
	if _, err := ctx.RunCommandContext(cmd.Context(), command); err != nil {
		return fmt.Errorf("%s volume %s: %w", map[bool]string{true: "restore", false: "backup"}[restore], volume, err)
	}
	return nil
}
