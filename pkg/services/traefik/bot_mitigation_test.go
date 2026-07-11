package traefik

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
)

func TestBotMitigationBuildsReusableComponentDefinition(t *testing.T) {
	def := BotMitigation(BotMitigationOptions{RouterName: "ojs"})

	if def.Name != BotMitigationName {
		t.Fatalf("expected component name %q, got %q", BotMitigationName, def.Name)
	}
	if def.DefaultState != corecomponent.StateOff {
		t.Fatalf("expected default off state, got %q", def.DefaultState)
	}
	if !def.PromptOnCreate {
		t.Fatal("expected bot mitigation to prompt on create")
	}
	if !strings.Contains(def.Guidance.EnabledHelp, "ojs router") {
		t.Fatalf("expected router-specific guidance, got %q", def.Guidance.EnabledHelp)
	}
	if len(def.On.Compose.Rules) != 0 || len(def.Off.Compose.Rules) != 0 {
		t.Fatalf("expected metadata-only definition to avoid partial apply rules, got on=%#v off=%#v", def.On.Compose.Rules, def.Off.Compose.Rules)
	}
}

func TestNormalizeBotMitigationOptionsAllowsAppMiddlewareOverrides(t *testing.T) {
	opts := NormalizeBotMitigationOptions(BotMitigationOptions{
		RouterName: "ojs",
		Middleware: CaptchaProtectMiddlewareOptions{
			ProtectRoutes: "^/(issues|articles)",
			ExcludeRoutes: []string{
				`\/api\/v1`,
			},
		},
	})

	if opts.RouterConfigPath != "conf/traefik/ojs.yml" {
		t.Fatalf("expected router config path derived from router, got %q", opts.RouterConfigPath)
	}
	if opts.Middleware.ProtectRoutes != "^/(issues|articles)" {
		t.Fatalf("expected protectRoutes override preserved, got %q", opts.Middleware.ProtectRoutes)
	}
	if len(opts.Middleware.ExcludeRoutes) != 1 || opts.Middleware.ExcludeRoutes[0] != `\/api\/v1` {
		t.Fatalf("expected excludeRoutes override preserved, got %#v", opts.Middleware.ExcludeRoutes)
	}
	if opts.Middleware.CaptchaProvider != "turnstile" {
		t.Fatalf("expected default captcha provider, got %q", opts.Middleware.CaptchaProvider)
	}
	if opts.Middleware.EnableUptimeRobotBypass != "true" {
		t.Fatalf("expected UptimeRobot bypass enabled, got %q", opts.Middleware.EnableUptimeRobotBypass)
	}
}

func TestApplyBotMitigationRoundTripManagesAllArtifacts(t *testing.T) {
	projectDir := t.TempDir()
	writeBotMitigationProjectFixture(t, projectDir)

	archive := testCaptchaProtectArchive(t)
	oldFetch := fetchCaptchaProtectArchive
	oldExpectedTreeSHA := captchaProtectExpectedTreeSHA256
	captchaProtectExpectedTreeSHA256 = testCaptchaProtectArchiveTreeSHA(t, archive)
	fetchCaptchaProtectArchive = func(context.Context) ([]byte, error) {
		return archive, nil
	}
	t.Cleanup(func() {
		fetchCaptchaProtectArchive = oldFetch
		captchaProtectExpectedTreeSHA256 = oldExpectedTreeSHA
	})

	opts := BotMitigationOptions{
		RouterName:       "ojs",
		RouterConfigPath: "conf/traefik/ojs.yml",
		Middleware: CaptchaProtectMiddlewareOptions{
			ProtectRoutes: "^/(issues|articles)",
			ExcludeRoutes: []string{`\/api\/v1`},
		},
	}
	if err := ApplyBotMitigation(projectDir, BotMitigationStateOn, opts); err != nil {
		t.Fatalf("ApplyBotMitigation(on) error = %v", err)
	}

	compose := readText(t, filepath.Join(projectDir, "docker-compose.yml"))
	for _, want := range []string{
		captchaProtectCommand,
		captchaProtectPluginVolume,
		captchaProtectTemplateMount,
		"TURNSTILE_SITE_KEY:",
		turnstileSiteKeyDefault,
		"TURNSTILE_SECRET_KEY:",
		turnstileSecretKeyDefault,
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("expected compose to contain %q, got:\n%s", want, compose)
		}
	}

	router := readText(t, filepath.Join(projectDir, "conf", "traefik", "ojs.yml"))
	for _, want := range []string{
		"      middlewares:\n        - captcha-protect",
		"          protectRoutes: ^/(issues|articles)",
		"          excludeRoutes:\n            - \\/api\\/v1",
		"          enableUptimeRobotBypass: \"true\"",
	} {
		if !strings.Contains(router, want) {
			t.Fatalf("expected router config to contain %q, got:\n%s", want, router)
		}
	}
	template := readText(t, filepath.Join(projectDir, "conf", "traefik", "challenge.tmpl.html"))
	if !strings.Contains(template, "{{ .FrontendJS }}") {
		t.Fatalf("expected challenge template installed, got:\n%s", template)
	}
	pluginFile := filepath.Join(projectDir, "conf", "traefik", "plugins", "captcha-protect", "go.mod")
	info, err := os.Stat(pluginFile)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", pluginFile, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected normalized plugin file mode 0644, got %04o", got)
	}

	if err := ApplyBotMitigation(projectDir, BotMitigationStateOff, opts); err != nil {
		t.Fatalf("ApplyBotMitigation(off) error = %v", err)
	}
	compose = readText(t, filepath.Join(projectDir, "docker-compose.yml"))
	for _, removed := range []string{
		"captcha-protect",
		"TURNSTILE_SITE_KEY",
		"TURNSTILE_SECRET_KEY",
	} {
		if strings.Contains(compose, removed) {
			t.Fatalf("expected compose to remove %q, got:\n%s", removed, compose)
		}
	}
	router = readText(t, filepath.Join(projectDir, "conf", "traefik", "ojs.yml"))
	if strings.Contains(router, "captcha-protect") {
		t.Fatalf("expected router config to remove captcha-protect, got:\n%s", router)
	}
	for _, removedPath := range []string{
		filepath.Join(projectDir, "conf", "traefik", "plugins", "captcha-protect"),
		filepath.Join(projectDir, "conf", "traefik", "challenge.tmpl.html"),
	} {
		if _, err := os.Stat(removedPath); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat error = %v", removedPath, err)
		}
	}
}

func TestUpdateComposeForBotMitigationPreservesFoldedTraefikCommand(t *testing.T) {
	projectDir := t.TempDir()
	copyTestFixture(t, filepath.Join(projectDir, "docker-compose.yml"), "docker-compose-isle.yml")

	if err := updateComposeForBotMitigation(projectDir, true); err != nil {
		t.Fatalf("updateComposeForBotMitigation(on) error = %v", err)
	}
	compose := readText(t, filepath.Join(projectDir, "docker-compose.yml"))
	for _, want := range []string{
		"<<: *common",
		"--entryPoints.http.address=:80\n      --entryPoints.http.forwardedHeaders.trustedIPs=${FRONTEND_IP_1},${FRONTEND_IP_2},${FRONTEND_IP_3}",
		"--api.debug=${DEVELOPMENT_ENVIRONMENT:-false}\n      " + captchaProtectCommand,
		"      - ./certs/cert.pem:/certs/cert.pem:ro,z",
		"      - ./certs/privkey.pem:/certs/privkey.pem:ro,z",
		"      - ./conf/traefik:/etc/traefik/dynamic:ro,z",
		"      - " + captchaProtectPluginVolume,
		"      - " + captchaProtectTemplateMount,
		`TURNSTILE_SITE_KEY: "` + turnstileSiteKeyDefault + `"`,
		`TURNSTILE_SECRET_KEY: "` + turnstileSecretKeyDefault + `"`,
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("expected compose to contain %q, got:\n%s", want, compose)
		}
	}
	if strings.Count(compose, captchaProtectCommand) != 1 {
		t.Fatalf("expected captcha command once, got:\n%s", compose)
	}
	if strings.Contains(compose, "--ping=true --log.level=INFO") {
		t.Fatalf("expected folded command lines not to collapse, got:\n%s", compose)
	}

	if err := updateComposeForBotMitigation(projectDir, false); err != nil {
		t.Fatalf("updateComposeForBotMitigation(off) error = %v", err)
	}
	compose = readText(t, filepath.Join(projectDir, "docker-compose.yml"))
	for _, removed := range []string{captchaProtectCommand, captchaProtectPluginVolume, captchaProtectTemplateMount, "TURNSTILE_SITE_KEY", "TURNSTILE_SECRET_KEY"} {
		if strings.Contains(compose, removed) {
			t.Fatalf("expected compose to remove %q, got:\n%s", removed, compose)
		}
	}
	for _, want := range []string{
		"command: >-\n      --ping=true\n      --log.level=INFO",
		"      --entryPoints.https.transport.respondingTimeouts.readTimeout=60",
		"      --providers.file.directory=/etc/traefik/dynamic",
		"      --api.debug=${DEVELOPMENT_ENVIRONMENT:-false}",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("expected original folded command line %q preserved after disable, got:\n%s", want, compose)
		}
	}
}

func writeBotMitigationProjectFixture(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, "conf", "traefik"), 0o755); err != nil {
		t.Fatalf("MkdirAll(conf/traefik) error = %v", err)
	}
	copyTestFixture(t, filepath.Join(projectDir, "docker-compose.yml"), "docker-compose-sequence.yml")
	copyTestFixture(t, filepath.Join(projectDir, "conf", "traefik", "ojs.yml"), "ojs-router.yml")
}

func testCaptchaProtectArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	writeZipFile(t, writer, "captcha-protect-1.12.3/challenge.tmpl.html", 0o666, "{{ .FrontendJS }}")
	writeZipFile(t, writer, "captcha-protect-1.12.3/go.mod", 0o777, "module github.com/libops/captcha-protect\n")
	if err := writer.Close(); err != nil {
		t.Fatalf("Close zip writer error = %v", err)
	}
	return buf.Bytes()
}

func testCaptchaProtectArchiveTreeSHA(t *testing.T, archive []byte) string {
	t.Helper()
	targetDir := t.TempDir()
	if err := extractCaptchaProtectArchive(archive, targetDir); err != nil {
		t.Fatalf("extractCaptchaProtectArchive() error = %v", err)
	}
	treeSHA, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		t.Fatalf("hashCaptchaProtectSourceTree() error = %v", err)
	}
	return treeSHA
}

func TestCaptchaProtectArchiveTreeHashMatchesInstallMarker(t *testing.T) {
	targetDir := t.TempDir()

	if err := extractCaptchaProtectArchive(testCaptchaProtectArchiveWithSkippedPaths(t), targetDir); err != nil {
		t.Fatalf("extractCaptchaProtectArchive() error = %v", err)
	}
	for _, skipped := range []string{
		"ci/build.sh",
		".github/workflows/test.yml",
		"renovate.json5",
		"internal/plugin_test.go",
	} {
		if _, err := os.Stat(filepath.Join(targetDir, filepath.FromSlash(skipped))); !os.IsNotExist(err) {
			t.Fatalf("expected archive path %q to be skipped, stat error = %v", skipped, err)
		}
	}

	treeBeforeMarker, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		t.Fatalf("hashCaptchaProtectSourceTree(before marker) error = %v", err)
	}
	if err := writeCaptchaProtectInstallMarker(targetDir); err != nil {
		t.Fatalf("writeCaptchaProtectInstallMarker() error = %v", err)
	}
	treeAfterMarker, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		t.Fatalf("hashCaptchaProtectSourceTree(after marker) error = %v", err)
	}
	if treeAfterMarker != treeBeforeMarker {
		t.Fatalf("expected install marker to be excluded from source tree hash, before=%s after=%s", treeBeforeMarker, treeAfterMarker)
	}

	marker, err := readCaptchaProtectInstallMarker(targetDir)
	if err != nil {
		t.Fatalf("readCaptchaProtectInstallMarker() error = %v", err)
	}
	if marker["source_url"] != captchaProtectSourceURL || marker["archive_sha256"] != captchaProtectSourceSHA256 {
		t.Fatalf("unexpected marker source metadata: %#v", marker)
	}
	if marker["tree_sha256"] != treeAfterMarker {
		t.Fatalf("marker tree_sha256 = %s, want %s", marker["tree_sha256"], treeAfterMarker)
	}
	current, err := captchaProtectPluginCurrentForTreeSHA(targetDir, treeAfterMarker)
	if err != nil {
		t.Fatalf("captchaProtectPluginCurrentForTreeSHA(marker) error = %v", err)
	}
	if !current {
		t.Fatal("expected plugin tree to be current when its pinned tree hash matches")
	}

	if err := os.Remove(filepath.Join(targetDir, captchaProtectInstallMarker)); err != nil {
		t.Fatalf("Remove(%s) error = %v", captchaProtectInstallMarker, err)
	}
	current, err = captchaProtectPluginCurrentForTreeSHA(targetDir, treeAfterMarker)
	if err != nil {
		t.Fatalf("captchaProtectPluginCurrentForTreeSHA(markerless) error = %v", err)
	}
	if !current {
		t.Fatal("expected markerless plugin tree to be current when its tree hash matches")
	}
}

func TestCaptchaProtectPluginCurrentRejectsTamperedTreeWithForgedMarker(t *testing.T) {
	targetDir := t.TempDir()
	pluginPath := filepath.Join(targetDir, "plugin.go")
	if err := os.WriteFile(pluginPath, []byte("package plugin\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	expectedTreeSHA, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		t.Fatalf("hashCaptchaProtectSourceTree(expected) error = %v", err)
	}

	if err := os.WriteFile(pluginPath, []byte("package compromised\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tampered plugin) error = %v", err)
	}
	tamperedTreeSHA, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		t.Fatalf("hashCaptchaProtectSourceTree(tampered) error = %v", err)
	}
	forgedMarker := "source_url=" + captchaProtectSourceURL + "\n" +
		"archive_sha256=" + captchaProtectSourceSHA256 + "\n" +
		"tree_sha256=" + tamperedTreeSHA + "\n"
	if err := os.WriteFile(filepath.Join(targetDir, captchaProtectInstallMarker), []byte(forgedMarker), 0o644); err != nil {
		t.Fatalf("WriteFile(forged marker) error = %v", err)
	}

	current, err := captchaProtectPluginCurrentForTreeSHA(targetDir, expectedTreeSHA)
	if err != nil {
		t.Fatalf("captchaProtectPluginCurrentForTreeSHA() error = %v", err)
	}
	if current {
		t.Fatal("expected pinned tree hash to reject a tampered plugin with a forged marker")
	}
}

func TestVerifyCaptchaProtectSourceTreeRejectsMismatch(t *testing.T) {
	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "plugin.go"), []byte("package plugin\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}

	err := verifyCaptchaProtectSourceTree(targetDir, strings.Repeat("0", sha256.Size*2))
	if err == nil || !strings.Contains(err.Error(), "extracted tree sha256 mismatch") {
		t.Fatalf("verifyCaptchaProtectSourceTree() error = %v, want tree hash mismatch", err)
	}
}

func testCaptchaProtectArchiveWithSkippedPaths(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	writeZipFile(t, writer, "captcha-protect-1.12.3/challenge.tmpl.html", 0o666, "{{ .FrontendJS }}")
	writeZipFile(t, writer, "captcha-protect-1.12.3/go.mod", 0o777, "module github.com/libops/captcha-protect\n")
	writeZipFile(t, writer, "captcha-protect-1.12.3/internal/plugin.go", 0o644, "package internal\n")
	writeZipFile(t, writer, "captcha-protect-1.12.3/ci/build.sh", 0o755, "go test ./...\n")
	writeZipFile(t, writer, "captcha-protect-1.12.3/.github/workflows/test.yml", 0o644, "name: test\n")
	writeZipFile(t, writer, "captcha-protect-1.12.3/renovate.json5", 0o644, "{}\n")
	writeZipFile(t, writer, "captcha-protect-1.12.3/internal/plugin_test.go", 0o644, "package internal\n")
	if err := writer.Close(); err != nil {
		t.Fatalf("Close zip writer error = %v", err)
	}
	return buf.Bytes()
}

func writeZipFile(t *testing.T, writer *zip.Writer, name string, mode os.FileMode, content string) {
	t.Helper()
	header := &zip.FileHeader{Name: name}
	header.SetMode(mode)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("CreateHeader(%s) error = %v", name, err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatalf("Write zip file %s error = %v", name, err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func copyTestFixture(t *testing.T, targetPath, fixtureName string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "bot_mitigation", fixtureName))
	if err != nil {
		t.Fatalf("ReadFile(test fixture %s) error = %v", fixtureName, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(targetPath), err)
	}
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", targetPath, err)
	}
}

func TestUpdateRouterConfigForBotMitigationAppliesCustomMiddleware(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ojs.yml")
	copyTestFixture(t, path, "ojs-router.yml")

	opts := NormalizeBotMitigationOptions(BotMitigationOptions{
		RouterName: "ojs",
		Middleware: CaptchaProtectMiddlewareOptions{
			ProtectRoutes: "^/(issues|articles)",
			ExcludeRoutes: []string{
				`\/api\/v1`,
			},
		},
	})
	if err := updateRouterConfigForBotMitigation(path, opts, true); err != nil {
		t.Fatalf("updateRouterConfigForBotMitigation() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(data)
	for _, want := range []string{
		"      middlewares:\n        - captcha-protect",
		"    captcha-protect:\n      plugin:\n        captcha-protect:",
		"          protectRoutes: ^/(issues|articles)",
		"          excludeRoutes:\n            - \\/api\\/v1",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected router config to contain %q, got:\n%s", want, rendered)
		}
	}

	if err := updateRouterConfigForBotMitigation(path, opts, false); err != nil {
		t.Fatalf("updateRouterConfigForBotMitigation(disabled) error = %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(data), "captcha-protect") {
		t.Fatalf("expected disabled router config to remove captcha-protect, got:\n%s", string(data))
	}
}

func TestUpdateRouterConfigForBotMitigationHandlesNonCanonicalYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ojs.yml")
	copyTestFixture(t, path, "ojs-router-noncanonical.yml")

	opts := NormalizeBotMitigationOptions(BotMitigationOptions{RouterName: "ojs"})
	if err := updateRouterConfigForBotMitigation(path, opts, true); err != nil {
		t.Fatalf("updateRouterConfigForBotMitigation() error = %v", err)
	}

	rendered := readText(t, path)
	for _, want := range []string{
		"middlewares:\n        - captcha-protect",
		"captcha-protect:\n      plugin:",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected router config to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestUpdateRouterConfigForBotMitigationDisableOnlyRemovesMiddlewareBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ojs.yml")
	copyTestFixture(t, path, "ojs-router-captcha-service.yml")

	opts := NormalizeBotMitigationOptions(BotMitigationOptions{RouterName: "ojs"})
	if err := updateRouterConfigForBotMitigation(path, opts, false); err != nil {
		t.Fatalf("updateRouterConfigForBotMitigation(disabled) error = %v", err)
	}

	rendered := readText(t, path)
	if !strings.Contains(rendered, "captcha-protect:\n      loadBalancer:") {
		t.Fatalf("expected service named captcha-protect to remain, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "- captcha-protect") || strings.Contains(rendered, "plugin:\n        captcha-protect:") {
		t.Fatalf("expected middleware reference and block removed, got:\n%s", rendered)
	}
}

func TestEnsureBotMitigationFilesSkipsCurrentPluginInstall(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "conf", "traefik"), 0o755); err != nil {
		t.Fatalf("MkdirAll(conf/traefik) error = %v", err)
	}

	archive := testCaptchaProtectArchive(t)
	oldFetch := fetchCaptchaProtectArchive
	oldExpectedTreeSHA := captchaProtectExpectedTreeSHA256
	captchaProtectExpectedTreeSHA256 = testCaptchaProtectArchiveTreeSHA(t, archive)
	fetches := 0
	fetchCaptchaProtectArchive = func(context.Context) ([]byte, error) {
		fetches++
		return archive, nil
	}
	t.Cleanup(func() {
		fetchCaptchaProtectArchive = oldFetch
		captchaProtectExpectedTreeSHA256 = oldExpectedTreeSHA
	})

	if err := ensureBotMitigationFiles(context.Background(), projectDir); err != nil {
		t.Fatalf("ensureBotMitigationFiles() error = %v", err)
	}
	if err := ensureBotMitigationFiles(context.Background(), projectDir); err != nil {
		t.Fatalf("ensureBotMitigationFiles(second) error = %v", err)
	}
	if fetches != 1 {
		t.Fatalf("expected one archive fetch for current install, got %d", fetches)
	}
}

func TestDownloadCaptchaProtectArchiveHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := downloadCaptchaProtectArchive(ctx, captchaProtectSourceURL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func TestDownloadCaptchaProtectArchiveRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxCaptchaProtectArchiveBytes+1))
	}))
	t.Cleanup(server.Close)

	_, err := downloadCaptchaProtectArchive(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected oversized archive error")
	}
	if !strings.Contains(err.Error(), "response exceeds") || !strings.Contains(err.Error(), "8388608") {
		t.Fatalf("unexpected oversized archive error: %v", err)
	}
}
