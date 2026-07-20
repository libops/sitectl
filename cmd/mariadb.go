package cmd

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	corejob "github.com/libops/sitectl/pkg/job"
	"github.com/spf13/cobra"
)

const defaultMariaDBService = "mariadb"

type mariaDBBackupOptions struct {
	service      string
	output       string
	database     string
	allDatabases bool
	compress     bool
}

type mariaDBImportOptions struct {
	service  string
	input    string
	database string
	yolo     bool
}

type mariaDBSyncOptions struct {
	service   string
	source    string
	target    string
	database  string
	backupDir string
	fresh     bool
	yolo      bool
}

type mariaDBUpgradeResult struct {
	exitCode int
	stdout   string
	stderr   string
}

type mariaDBUpgradeExecutor func(context.Context, *docker.DockerClient, string, []string) (mariaDBUpgradeResult, error)

func mariaDBCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mariadb",
		Short:   "Operate on the MariaDB service in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(
		serviceStatusCommand(defaultMariaDBService),
		mariaDBBackupCommand(),
		mariaDBRestoreCommand(),
		mariaDBSyncCommand(),
		mariaDBUpgradeCommand(),
	)
	return cmd
}

func mariaDBUpgradeCommand() *cobra.Command {
	opts := struct {
		service string
	}{service: defaultMariaDBService}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade MariaDB system tables when required",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServiceContainer(cmd, opts.service)
			if err != nil {
				return err
			}
			defer func() {
				_ = target.cli.Close()
			}()

			return runMariaDBUpgrade(cmd, target)
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", defaultMariaDBService, "MariaDB compose service name")
	return cmd
}

func runMariaDBUpgrade(cmd *cobra.Command, target *serviceContainer) error {
	binary := resolveContainerExecutable(cmd, target.cli, target.containerName, "mariadb-upgrade", "mysql_upgrade")
	return runMariaDBUpgradeWithExecutor(cmd, target, binary, executeMariaDBUpgradeCommand)
}

func runMariaDBUpgradeWithExecutor(cmd *cobra.Command, target *serviceContainer, binary string, execute mariaDBUpgradeExecutor) error {
	helpResult, err := execute(cmd.Context(), target.cli, target.containerName, []string{binary, "--help"})
	if err != nil {
		return mariaDBUpgradeError("inspect MariaDB upgrade capabilities", helpResult, err)
	}

	supportsProbe := helpResult.exitCode == 0 && strings.Contains(
		helpResult.stdout+"\n"+helpResult.stderr,
		"--check-if-upgrade-is-needed",
	)
	if supportsProbe {
		probeResult, err := execute(cmd.Context(), target.cli, target.containerName, []string{binary, "--check-if-upgrade-is-needed"})
		if err != nil {
			return mariaDBUpgradeError("check whether MariaDB needs an upgrade", probeResult, err)
		}
		switch probeResult.exitCode {
		case 0:
			if err := writeMariaDBUpgradeResult(cmd, probeResult); err != nil {
				return err
			}
		case 1:
			if err := writeMariaDBUpgradeResult(cmd, probeResult); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "MariaDB is already up to date; no upgrade needed."); err != nil {
				return fmt.Errorf("write MariaDB upgrade status: %w", err)
			}
			return nil
		default:
			return mariaDBUpgradeError("check whether MariaDB needs an upgrade", probeResult, nil)
		}
	}

	upgradeResult, err := execute(cmd.Context(), target.cli, target.containerName, []string{binary})
	if err != nil {
		return mariaDBUpgradeError("run MariaDB upgrade", upgradeResult, err)
	}
	if upgradeResult.exitCode != 0 {
		return mariaDBUpgradeError("run MariaDB upgrade", upgradeResult, nil)
	}
	if err := writeMariaDBUpgradeResult(cmd, upgradeResult); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "MariaDB upgrade complete."); err != nil {
		return fmt.Errorf("write MariaDB upgrade status: %w", err)
	}
	return nil
}

func executeMariaDBUpgradeCommand(ctx context.Context, cli *docker.DockerClient, containerName string, command []string) (mariaDBUpgradeResult, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode, err := cli.Exec(ctx, docker.ExecOptions{
		Container:    containerName,
		Cmd:          command,
		AttachStdout: true,
		AttachStderr: true,
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	return mariaDBUpgradeResult{
		exitCode: exitCode,
		stdout:   stdout.String(),
		stderr:   stderr.String(),
	}, err
}

func mariaDBUpgradeError(action string, result mariaDBUpgradeResult, err error) error {
	detail := strings.TrimSpace(result.stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.stdout)
	}
	if err != nil {
		if detail != "" {
			return fmt.Errorf("%s: %w: %s", action, err, detail)
		}
		return fmt.Errorf("%s: %w", action, err)
	}
	if detail != "" {
		return fmt.Errorf("%s failed with exit code %d: %s", action, result.exitCode, detail)
	}
	return fmt.Errorf("%s failed with exit code %d", action, result.exitCode)
}

func writeMariaDBUpgradeResult(cmd *cobra.Command, result mariaDBUpgradeResult) error {
	if err := writeMariaDBUpgradeStream(cmd.OutOrStdout(), result.stdout); err != nil {
		return fmt.Errorf("write MariaDB upgrade stdout: %w", err)
	}
	if err := writeMariaDBUpgradeStream(cmd.ErrOrStderr(), result.stderr); err != nil {
		return fmt.Errorf("write MariaDB upgrade stderr: %w", err)
	}
	return nil
}

func writeMariaDBUpgradeStream(writer io.Writer, output string) error {
	if output == "" {
		return nil
	}
	if _, err := io.WriteString(writer, output); err != nil {
		return err
	}
	if !strings.HasSuffix(output, "\n") {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func mariaDBBackupCommand() *cobra.Command {
	opts := mariaDBBackupOptions{service: defaultMariaDBService}
	cmd := &cobra.Command{
		Use:   "backup [DATABASE]",
		Short: "Export a MariaDB SQL backup from the active compose stack",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.database = strings.TrimSpace(args[0])
			}
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			return runMariaDBBackup(cmd, ctx, opts)
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", defaultMariaDBService, "MariaDB compose service name")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "", "Write SQL backup to a host path instead of stdout")
	cmd.Flags().BoolVar(&opts.allDatabases, "all-databases", false, "Export all databases explicitly")
	cmd.Flags().BoolVar(&opts.compress, "gzip", false, "Compress the backup with gzip")
	return cmd
}

func mariaDBRestoreCommand() *cobra.Command {
	opts := mariaDBImportOptions{service: defaultMariaDBService}
	cmd := &cobra.Command{
		Use:     "restore INPUT [DATABASE]",
		Aliases: []string{"import"},
		Short:   "Import a MariaDB SQL backup into the active compose stack",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.input = strings.TrimSpace(args[0])
			if len(args) == 2 {
				opts.database = strings.TrimSpace(args[1])
			}
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			return runMariaDBImport(cmd, ctx, opts)
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", defaultMariaDBService, "MariaDB compose service name")
	cmd.Flags().StringVar(&opts.database, "database", "", "Database to replace before import")
	cmd.Flags().BoolVar(&opts.yolo, "yolo", false, "Apply destructive database changes without confirmation")
	return cmd
}

func mariaDBSyncCommand() *cobra.Command {
	opts := mariaDBSyncOptions{
		service:   defaultMariaDBService,
		backupDir: "/tmp/sitectl-mariadb-jobs/db-backup",
	}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync a MariaDB database artifact between contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMariaDBSync(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.source, "source", "", "Source sitectl context")
	cmd.Flags().StringVar(&opts.target, "target", "", "Target sitectl context")
	cmd.Flags().StringVar(&opts.service, "service", defaultMariaDBService, "MariaDB compose service name")
	cmd.Flags().StringVar(&opts.database, "database", "", "Database to sync instead of all databases")
	cmd.Flags().StringVar(&opts.backupDir, "backup-dir", opts.backupDir, "Directory on the source host used to cache backup artifacts")
	cmd.Flags().BoolVar(&opts.fresh, "fresh", false, "Always take a fresh backup instead of reusing one from today")
	cmd.Flags().BoolVar(&opts.yolo, "yolo", false, "Skip the confirmation prompt before importing")
	markRequired(cmd, "source")
	markRequired(cmd, "target")
	return cmd
}

func runMariaDBBackup(cmd *cobra.Command, ctx *config.Context, opts mariaDBBackupOptions) error {
	if err := validateMariaDBBackupOptions(opts); err != nil {
		return err
	}
	if strings.TrimSpace(opts.output) == "" {
		return writeMariaDBDump(cmd, ctx, opts, cmd.OutOrStdout())
	}
	if err := corejob.EnsurePathAbsentOnContext(ctx, opts.output); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp("", "sitectl-mariadb-backup-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if err := writeMariaDBDump(cmd, ctx, opts, tempFile); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return ctx.UploadFile(tempPath, opts.output)
}

func runMariaDBImport(cmd *cobra.Command, ctx *config.Context, opts mariaDBImportOptions) error {
	if strings.TrimSpace(opts.input) == "" {
		return fmt.Errorf("input path is required")
	}
	database := strings.TrimSpace(opts.database)
	if err := validateMariaDBDatabaseName(database); err != nil {
		return err
	}
	databaseLabel := "MariaDB"
	if database != "" {
		databaseLabel = "MariaDB " + database
	}
	ok, err := corejob.ConfirmDatabaseReplacement(ctx.Name, databaseLabel, opts.input, opts.yolo)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("database import cancelled")
	}

	cli, containerName, password, err := mariaDBContainer(cmd, ctx, opts.service)
	if err != nil {
		return err
	}
	defer func() {
		_ = cli.Close()
	}()

	tempFile, err := os.CreateTemp("", "sitectl-mariadb-import-*.sql")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if err := corejob.DownloadContextFile(ctx, opts.input, tempPath); err != nil {
		return err
	}
	inputFile, err := os.Open(tempPath) // #nosec G304 -- tempPath is created by this process and populated before import.
	if err != nil {
		return err
	}
	defer func() {
		_ = inputFile.Close()
	}()
	reader, cleanupReader, err := maybeGzipReader(inputFile)
	if err != nil {
		return err
	}
	defer func() {
		_ = cleanupReader()
	}()

	client := resolveContainerExecutable(cmd, cli, containerName, "mariadb", "mysql")
	if database != "" {
		if err := resetMariaDBDatabase(cmd, cli, containerName, client, password, database); err != nil {
			return err
		}
	}

	args := []string{client, "--user=root"}
	if database != "" {
		args = append(args, database)
	}
	var stderr bytes.Buffer
	exitCode, err := cli.Exec(cmd.Context(), docker.ExecOptions{
		Container:    containerName,
		Cmd:          args,
		Env:          []string{"MYSQL_PWD=" + strings.TrimSpace(password)},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Stdin:        reader,
		Stdout:       io.Discard,
		Stderr:       &stderr,
	})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("mariadb import failed with exit code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runMariaDBSync(cmd *cobra.Command, opts mariaDBSyncOptions) error {
	sourceCtx, targetCtx, err := corejob.ResolveContextPair(opts.source, opts.target)
	if err != nil {
		return err
	}
	workDir, cleanupWorkDir, err := corejob.MakeTempWorkDir("sitectl-mariadb-sync-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer cleanupWorkDir()

	artifact := mariaDBArtifactName(opts.database)
	sourceArtifactPath, err := corejob.ResolveRecentArtifact(sourceCtx, opts.backupDir, artifact, opts.fresh, time.Now().UTC(), func(path string) error {
		return runMariaDBBackup(cmd, sourceCtx, mariaDBBackupOptions{
			service:  opts.service,
			output:   path,
			database: opts.database,
			compress: true,
		})
	})
	if err != nil {
		return fmt.Errorf("resolve source backup artifact from %q: %w", sourceCtx.Name, err)
	}

	targetHostPath, cleanupTarget, err := corejob.StageArtifactBetweenContexts(
		cmd.Context(),
		sourceCtx,
		targetCtx,
		sourceArtifactPath,
		workDir,
		artifact,
		"sitectl-mariadb-sync",
	)
	if err != nil {
		return fmt.Errorf("stage database artifact from %q to %q: %w", sourceCtx.Name, targetCtx.Name, err)
	}
	defer cleanupTarget()

	if err := runMariaDBImport(cmd, targetCtx, mariaDBImportOptions{
		service:  opts.service,
		input:    targetHostPath,
		database: opts.database,
		yolo:     opts.yolo,
	}); err != nil {
		return fmt.Errorf("import database into %q: %w", targetCtx.Name, err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MariaDB database synced from %s to %s\n", sourceCtx.Name, targetCtx.Name)
	return nil
}

func writeMariaDBDump(cmd *cobra.Command, ctx *config.Context, opts mariaDBBackupOptions, output io.Writer) error {
	cli, containerName, password, err := mariaDBContainer(cmd, ctx, opts.service)
	if err != nil {
		return err
	}
	defer func() {
		_ = cli.Close()
	}()

	writer := output
	var gzipWriter *gzip.Writer
	if opts.compress {
		gzipWriter = gzip.NewWriter(output)
		writer = gzipWriter
	}

	dump := resolveContainerExecutable(cmd, cli, containerName, "mariadb-dump", "mysqldump")
	args := mariaDBDumpArgs(dump, opts)
	var stderr bytes.Buffer
	exitCode, err := cli.Exec(cmd.Context(), docker.ExecOptions{
		Container:    containerName,
		Cmd:          args,
		Env:          []string{"MYSQL_PWD=" + strings.TrimSpace(password)},
		AttachStdout: true,
		AttachStderr: true,
		Stdout:       writer,
		Stderr:       &stderr,
	})
	if err != nil {
		if gzipWriter != nil {
			_ = gzipWriter.Close()
		}
		return err
	}
	if gzipWriter != nil {
		if closeErr := gzipWriter.Close(); closeErr != nil {
			return closeErr
		}
	}
	if exitCode != 0 {
		return fmt.Errorf("mariadb dump failed with exit code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func mariaDBContainer(cmd *cobra.Command, ctx *config.Context, service string) (*docker.DockerClient, string, string, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, "", "", fmt.Errorf("service name cannot be empty")
	}
	cli, err := docker.GetDockerCli(ctx)
	if err != nil {
		return nil, "", "", err
	}
	containerName, err := cli.GetContainerNameContext(cmd.Context(), ctx, service)
	if err != nil {
		_ = cli.Close()
		return nil, "", "", fmt.Errorf("find %s container: %w", service, err)
	}
	if strings.TrimSpace(containerName) == "" {
		_ = cli.Close()
		return nil, "", "", fmt.Errorf("unable to find %s container for context %q", service, ctx.Name)
	}
	password, err := docker.GetFirstSecretOrEnv(cmd.Context(), cli.CLI, ctx, containerName, "DB_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD")
	if err != nil {
		_ = cli.Close()
		return nil, "", "", err
	}
	return cli, containerName, password, nil
}

func mariaDBDumpArgs(binary string, opts mariaDBBackupOptions) []string {
	args := []string{binary, "--single-transaction", "--quick", "--routines", "--triggers", "--user=root"}
	if strings.TrimSpace(opts.database) != "" {
		return append(args, strings.TrimSpace(opts.database))
	}
	return append(args, "--all-databases")
}

func resetMariaDBDatabase(cmd *cobra.Command, cli *docker.DockerClient, containerName, client, password, database string) error {
	stmt := fmt.Sprintf("DROP DATABASE IF EXISTS %s; CREATE DATABASE %s;", mariaDBIdentifier(database), mariaDBIdentifier(database))
	var stderr bytes.Buffer
	exitCode, err := cli.Exec(cmd.Context(), docker.ExecOptions{
		Container:    containerName,
		Cmd:          []string{client, "--user=root", "-e", stmt},
		Env:          []string{"MYSQL_PWD=" + strings.TrimSpace(password)},
		AttachStdout: true,
		AttachStderr: true,
		Stdout:       io.Discard,
		Stderr:       &stderr,
	})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("reset mariadb database %q failed with exit code %d: %s", database, exitCode, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func validateMariaDBBackupOptions(opts mariaDBBackupOptions) error {
	if strings.TrimSpace(opts.service) == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if strings.TrimSpace(opts.database) != "" && opts.allDatabases {
		return fmt.Errorf("--all-databases cannot be combined with a database name")
	}
	return validateMariaDBDatabaseName(opts.database)
}

func validateMariaDBDatabaseName(database string) error {
	database = strings.TrimSpace(database)
	if database == "" {
		return nil
	}
	if strings.HasPrefix(database, "-") {
		return fmt.Errorf("database name cannot start with '-'")
	}
	if strings.Contains(database, "\x00") {
		return fmt.Errorf("database name cannot contain NUL")
	}
	return nil
}

func mariaDBIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func maybeGzipReader(file *os.File) (io.Reader, func() error, error) {
	reader := bufio.NewReader(file)
	header, err := reader.Peek(2)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	if len(header) == 2 && header[0] == 0x1f && header[1] == 0x8b {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			return nil, nil, err
		}
		return gzipReader, gzipReader.Close, nil
	}
	return reader, func() error { return nil }, nil
}

func mariaDBArtifactName(database string) string {
	database = strings.TrimSpace(database)
	if database == "" {
		return "mariadb.sql.gz"
	}
	return "mariadb-" + sanitizeArtifactPart(database) + ".sql.gz"
}

func sanitizeArtifactPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "database"
	}
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r)
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '-' || r == '_':
			out.WriteRune(r)
		default:
			out.WriteRune('-')
		}
	}
	return strings.Trim(out.String(), "-")
}

func markRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(err)
	}
}
