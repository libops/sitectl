package hostruntime

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// BackupOptions controls local and off-host managed backups.
type BackupOptions struct {
	Root          string
	RetentionDays int
	StateRoot     string
	Driver        string
	DataRoot      string
	VolumesRoot   string
	Provider      string
	Instance      string
	Now           func() time.Time
	Sitectl       string
	LockPath      string
	Stdout        io.Writer
	Stderr        io.Writer
}

type composeConfig struct {
	Services map[string]struct {
		Volumes []composeMount `json:"volumes"`
	} `json:"services"`
	Volumes map[string]json.RawMessage `json:"volumes"`
}

type composeMount struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type backupApplication struct {
	Name             string           `json:"name"`
	Databases        []backupDatabase `json:"databases"`
	ApplicationFiles struct {
		Roots      []string        `json:"roots"`
		BindMounts []manifestMount `json:"bind_mounts"`
	} `json:"application_files"`
	VolumeTopology struct {
		DeclaredNamedVolumes []string        `json:"declared_named_volumes"`
		ServiceMounts        []manifestMount `json:"service_mounts"`
	} `json:"volume_topology"`
}

type backupDatabase struct {
	Engine string `json:"engine"`
	Format string `json:"format"`
	Path   string `json:"local_recovery_artifact"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type manifestMount struct {
	Service  string `json:"service"`
	Type     string `json:"type,omitempty"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type backupManifest struct {
	SchemaVersion    int                 `json:"schema_version"`
	Kind             string              `json:"kind"`
	OperationID      string              `json:"operation_id"`
	BackupDate       string              `json:"backup_date"`
	Provider         string              `json:"provider"`
	Instance         string              `json:"instance"`
	RequiredCoverage []string            `json:"required_coverage"`
	Applications     []backupApplication `json:"applications"`
}

// RunMariaDBBackups creates one validated daily dump per manifest application.
func RunMariaDBBackups(ctx context.Context, manifest Manifest, options BackupOptions) error {
	options = backupDefaults(options)
	if options.RetentionDays < 1 {
		return fmt.Errorf("backup retention must be positive")
	}
	lock, err := AcquireLock(ctx, options.LockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "cloud-compose.service").Run() != nil {
		_, _ = fmt.Fprintln(options.Stdout, "Cloud Compose application service is inactive; skipping MariaDB backup")
		return nil
	}
	date := options.Now().UTC().Format("20060102")
	var failures []error
	for _, name := range manifest.Names() {
		app := manifest[name]
		directory := filepath.Join(options.Root, name)
		if err := ensureSafeDirectory(directory, 0o750); err != nil {
			failures = append(failures, err)
			continue
		}
		output := filepath.Join(directory, date+"-"+name+".sql.gz")
		if validGzip(output) {
			continue
		}
		if _, err := os.Lstat(output); err == nil {
			failures = append(failures, fmt.Errorf("existing backup is invalid: %s", output))
			continue
		}
		staging, err := os.MkdirTemp(directory, ".backup-*")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		staged := filepath.Join(staging, "backup.sql.gz")
		executable := options.Sitectl
		if executable == "" {
			executable = "sitectl"
		}
		command := exec.CommandContext(ctx, executable, "mariadb", "backup", "--context", app.SitectlContextName, "--gzip", "--output", staged)
		command.Stdout, command.Stderr = options.Stdout, options.Stderr
		if err := command.Run(); err != nil {
			_ = os.RemoveAll(staging)
			failures = append(failures, fmt.Errorf("backup %q: %w", name, err))
			continue
		}
		if !validGzip(staged) {
			_ = os.RemoveAll(staging)
			failures = append(failures, fmt.Errorf("backup %q did not produce a valid gzip stream", name))
			continue
		}
		if err := os.Chmod(staged, 0o640); err != nil {
			_ = os.RemoveAll(staging)
			failures = append(failures, fmt.Errorf("secure backup %q: %w", name, err))
			continue
		}
		err = os.Rename(staged, output)
		_ = os.RemoveAll(staging)
		if err != nil {
			failures = append(failures, fmt.Errorf("publish backup %q: %w", name, err))
		}
	}
	cutoff := options.Now().Add(-time.Duration(options.RetentionDays) * 24 * time.Hour)
	for _, name := range manifest.Names() {
		entries, _ := os.ReadDir(filepath.Join(options.Root, name))
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".sql.gz") {
				info, err := entry.Info()
				if err == nil && info.ModTime().Before(cutoff) {
					_ = os.Remove(filepath.Join(options.Root, name, entry.Name()))
				}
			}
		}
	}
	return errors.Join(failures...)
}

// RunOffhostBackup proves complete recovery coverage through an operator-owned driver.
func RunOffhostBackup(ctx context.Context, manifest Manifest, options BackupOptions) error {
	options = backupDefaults(options)
	if err := validateDriver(options.Driver); err != nil {
		return err
	}
	lock, err := AcquireLock(ctx, options.LockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	date := options.Now().UTC().Format("20060102")
	operationID := date + "-" + options.Instance
	manifestDir := filepath.Join(options.StateRoot, "manifests")
	receiptDir := filepath.Join(options.StateRoot, "backup-receipts")
	stagingRoot := filepath.Join(options.StateRoot, "staging")
	for _, directory := range []string{options.StateRoot, manifestDir, receiptDir, stagingRoot} {
		if err := ensureSafeDirectory(directory, 0o700); err != nil {
			return err
		}
	}
	staging, err := os.MkdirTemp(stagingRoot, "."+operationID+"-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	backupManifest := backupManifest{SchemaVersion: 1, Kind: "cloud-compose.offhost-backup-manifest", OperationID: operationID, BackupDate: date, Provider: options.Provider, Instance: options.Instance, RequiredCoverage: []string{"database", "application_files", "volume_topology"}}
	for _, name := range manifest.Names() {
		app := manifest[name]
		dump := filepath.Join(options.Root, name, date+"-"+name+".sql.gz")
		if !validGzip(dump) {
			return fmt.Errorf("required MariaDB recovery artifact is invalid for %q", name)
		}
		stagedDump := filepath.Join(staging, name+".sql.gz")
		if err := copySingleLinkFile(dump, stagedDump, 0o400); err != nil {
			return err
		}
		config, err := renderComposeConfig(ctx, app)
		if err != nil {
			return err
		}
		application, err := buildCoverage(app, config, stagedDump, options)
		if err != nil {
			return err
		}
		backupManifest.Applications = append(backupManifest.Applications, application)
	}
	manifestBytes, err := json.Marshal(backupManifest)
	if err != nil {
		return err
	}
	stagedManifest := filepath.Join(staging, "manifest.json")
	if err := os.WriteFile(stagedManifest, append(manifestBytes, '\n'), 0o400); err != nil {
		return err
	}
	manifestDigest, _, err := digestFile(stagedManifest)
	if err != nil {
		return err
	}
	stagedReceipt := filepath.Join(staging, "receipt.json")
	if err := runDriver(ctx, options.Driver, "backup", "--manifest", stagedManifest, "--manifest-sha256", manifestDigest, "--operation-id", operationID, "--receipt", stagedReceipt); err != nil {
		return err
	}
	if err := validateBackupReceipt(stagedReceipt, operationID, manifestDigest); err != nil {
		return err
	}
	for _, pair := range [][2]string{{stagedManifest, filepath.Join(manifestDir, operationID+".json")}, {stagedReceipt, filepath.Join(receiptDir, operationID+".json")}} {
		if err := os.Chmod(pair[0], 0o640); err != nil {
			return err
		}
		if err := os.Rename(pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

// RunRestoreTest asks the off-host driver to prove the latest backup in a disposable recovery.
func RunRestoreTest(ctx context.Context, options BackupOptions) error {
	options = backupDefaults(options)
	if err := validateDriver(options.Driver); err != nil {
		return err
	}
	manifestDir := filepath.Join(options.StateRoot, "manifests")
	receiptDir := filepath.Join(options.StateRoot, "backup-receipts")
	proofDir := filepath.Join(options.StateRoot, "restore-proofs")
	stagingRoot := filepath.Join(options.StateRoot, "staging")
	for _, directory := range []string{options.StateRoot, manifestDir, receiptDir, proofDir, stagingRoot} {
		if err := ensureSafeDirectory(directory, 0o700); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(receiptDir)
	if err != nil {
		return err
	}
	var receipts []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			receipts = append(receipts, entry.Name())
		}
	}
	if len(receipts) == 0 {
		return fmt.Errorf("restore test requires a backup receipt")
	}
	sort.Strings(receipts)
	receiptName := receipts[len(receipts)-1]
	operationID := strings.TrimSuffix(receiptName, ".json")
	receiptPath := filepath.Join(receiptDir, receiptName)
	manifestPath := filepath.Join(manifestDir, receiptName)
	manifestDigest, _, err := digestFile(manifestPath)
	if err != nil {
		return err
	}
	if err := validateBackupReceipt(receiptPath, operationID, manifestDigest); err != nil {
		return err
	}
	receiptDigest, _, err := digestFile(receiptPath)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return err
	}
	testID := options.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random)
	staging, err := os.MkdirTemp(stagingRoot, ".restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	proof := filepath.Join(staging, "proof.json")
	if err := runDriver(ctx, options.Driver, "restore-test", "--manifest", manifestPath, "--backup-receipt", receiptPath, "--source-manifest-sha256", manifestDigest, "--source-receipt-sha256", receiptDigest, "--test-id", testID, "--proof", proof); err != nil {
		return err
	}
	if err := validateRestoreProof(proof, testID, manifestDigest, receiptDigest); err != nil {
		return err
	}
	if err := os.Chmod(proof, 0o640); err != nil {
		return err
	}
	return os.Rename(proof, filepath.Join(proofDir, testID+".json"))
}

func backupDefaults(options BackupOptions) BackupOptions {
	if options.Root == "" {
		options.Root = "/mnt/disks/data/backups/mariadb"
	}
	if options.RetentionDays == 0 {
		options.RetentionDays = 14
	}
	if options.StateRoot == "" {
		options.StateRoot = "/mnt/disks/data/.cloud-compose-disaster-recovery"
	}
	if options.Driver == "" {
		options.Driver = "/etc/cloud-compose/libexec/offhost-backup-driver"
	}
	if options.DataRoot == "" {
		options.DataRoot = "/mnt/disks/data"
	}
	if options.VolumesRoot == "" {
		options.VolumesRoot = "/mnt/disks/volumes"
	}
	if options.Instance == "" {
		options.Instance = "cloud-compose"
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.LockPath == "" {
		options.LockPath = "/run/lock/cloud-compose/lifecycle.lock"
	}
	return options
}

func renderComposeConfig(ctx context.Context, app Application) (composeConfig, error) {
	command := exec.CommandContext(ctx, "docker", "compose", "config", "--format", "json")
	command.Dir = app.ProjectDir
	command.Env = append(os.Environ(), app.Environment()...)
	output, err := command.Output()
	if err != nil {
		return composeConfig{}, fmt.Errorf("render Compose configuration for %q: %w", app.Name, err)
	}
	var config composeConfig
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	if err := decoder.Decode(&config); err != nil || len(config.Services) == 0 {
		return composeConfig{}, fmt.Errorf("decode Compose configuration for %q: %w", app.Name, err)
	}
	return config, nil
}

func buildCoverage(app Application, config composeConfig, dump string, options BackupOptions) (backupApplication, error) {
	digest, bytes, err := digestFile(dump)
	if err != nil {
		return backupApplication{}, err
	}
	application := backupApplication{Name: app.Name, Databases: []backupDatabase{{Engine: "mariadb", Format: "sql.gz", Path: dump, SHA256: digest, Bytes: bytes}}}
	application.ApplicationFiles.Roots = []string{app.ProjectDir}
	for service, settings := range config.Services {
		for _, mount := range settings.Volumes {
			if mount.Target == "" || (mount.Type != "bind" && mount.Type != "volume" && mount.Type != "tmpfs") {
				return backupApplication{}, fmt.Errorf("unsupported Compose volume for %q", app.Name)
			}
			entry := manifestMount{Service: service, Type: mount.Type, Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly}
			application.VolumeTopology.ServiceMounts = append(application.VolumeTopology.ServiceMounts, entry)
			if mount.Type == "bind" {
				if !withinRoot(mount.Source, options.DataRoot) && !withinRoot(mount.Source, options.VolumesRoot) {
					return backupApplication{}, fmt.Errorf("bind mount escapes managed roots for %q: %s", app.Name, mount.Source)
				}
				entry.Type = ""
				application.ApplicationFiles.BindMounts = append(application.ApplicationFiles.BindMounts, entry)
			}
		}
	}
	for name := range config.Volumes {
		application.VolumeTopology.DeclaredNamedVolumes = append(application.VolumeTopology.DeclaredNamedVolumes, name)
	}
	sort.Strings(application.VolumeTopology.DeclaredNamedVolumes)
	return application, nil
}

func validateBackupReceipt(path, operationID, manifestDigest string) error {
	var receipt struct {
		SchemaVersion  int             `json:"schema_version"`
		Kind           string          `json:"kind"`
		OperationID    string          `json:"operation_id"`
		ManifestSHA256 string          `json:"manifest_sha256"`
		Status         string          `json:"status"`
		Encrypted      bool            `json:"encrypted"`
		OffHost        bool            `json:"off_host"`
		CompletedAt    string          `json:"completed_at"`
		RemoteID       string          `json:"remote_id"`
		Coverage       map[string]bool `json:"coverage"`
	}
	if err := decodeJSONFile(path, &receipt); err != nil {
		return err
	}
	if receipt.SchemaVersion != 1 || receipt.Kind != "cloud-compose.offhost-backup-receipt" || receipt.OperationID != operationID || receipt.ManifestSHA256 != manifestDigest || receipt.Status != "succeeded" || !receipt.Encrypted || !receipt.OffHost || receipt.RemoteID == "" || len(receipt.RemoteID) > 512 || !validTimestamp(receipt.CompletedAt) || !coverageComplete(receipt.Coverage) {
		return fmt.Errorf("off-host backup receipt is incomplete")
	}
	return nil
}

func validateRestoreProof(path, testID, manifestDigest, receiptDigest string) error {
	var proof struct {
		SchemaVersion   int             `json:"schema_version"`
		Kind            string          `json:"kind"`
		TestID          string          `json:"test_id"`
		ManifestSHA256  string          `json:"source_manifest_sha256"`
		ReceiptSHA256   string          `json:"source_receipt_sha256"`
		Status          string          `json:"status"`
		Disposable      bool            `json:"disposable_recovery"`
		Destroyed       bool            `json:"recovery_destroyed"`
		Integrity       bool            `json:"integrity_verified"`
		CompletedAt     string          `json:"completed_at"`
		RecoveryID      string          `json:"recovery_id"`
		Coverage        map[string]bool `json:"coverage"`
		SourceEncrypted bool            `json:"source_encrypted"`
	}
	if err := decodeJSONFile(path, &proof); err != nil {
		return err
	}
	if proof.SchemaVersion != 1 || proof.Kind != "cloud-compose.restore-test-proof" || proof.TestID != testID || proof.ManifestSHA256 != manifestDigest || proof.ReceiptSHA256 != receiptDigest || proof.Status != "succeeded" || !proof.Disposable || !proof.Destroyed || !proof.Integrity || !proof.SourceEncrypted || proof.RecoveryID == "" || len(proof.RecoveryID) > 512 || !validTimestamp(proof.CompletedAt) || !coverageComplete(proof.Coverage) {
		return fmt.Errorf("restore-test proof is incomplete")
	}
	return nil
}

func decodeJSONFile(path string, target any) error {
	contents, err := readSingleLinkFile(path, 65536)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func validateDriver(path string) error {
	if err := validateAbsoluteFile(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o022 != 0 || stat.Nlink != 1 || stat.Uid != 0 || info.Mode()&0o111 == 0 {
		return fmt.Errorf("off-host backup driver is unsafe: %s", path)
	}
	return nil
}

func runDriver(ctx context.Context, driver string, args ...string) error {
	command := exec.CommandContext(ctx, driver, args...)
	command.Env = []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	if err := command.Run(); err != nil {
		return fmt.Errorf("off-host disaster-recovery driver failed: %w", err)
	}
	return nil
}

func digestFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	bytes, err := io.Copy(digest, file)
	return fmt.Sprintf("%x", digest.Sum(nil)), bytes, err
}

func copySingleLinkFile(source, destination string, mode os.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Nlink != 1 {
		return fmt.Errorf("unsafe source file: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func validGzip(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return false
	}
	_, err = io.Copy(io.Discard, reader)
	closeErr := reader.Close()
	return err == nil && closeErr == nil
}

func ensureSafeDirectory(path string, mode os.FileMode) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("unsafe directory: %s", path)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("directory contains a symbolic link: %s", path)
	}
	return os.Chmod(path, mode)
}

func withinRoot(path, root string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validTimestamp(value string) bool {
	_, err := time.Parse("2006-01-02T15:04:05Z", value)
	return err == nil
}

func coverageComplete(coverage map[string]bool) bool {
	return len(coverage) == 3 && coverage["database"] && coverage["application_files"] && coverage["volume_topology"]
}
