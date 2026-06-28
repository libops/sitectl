package plugin

import (
	"strings"

	"github.com/libops/sitectl/pkg/config"
	corehealthcheck "github.com/libops/sitectl/pkg/healthcheck"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

// HealthcheckRunner implements plugin-specific runtime health checks.
// Run returns a list of healthcheck results for the active context. Diagnostics
// that should be visible to users should be written to cmd.ErrOrStderr();
// stdout is captured by the RPC envelope and may not be displayed by callers.
type HealthcheckRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error)
}

type StandardComposeWebHealthcheckOptions struct {
	AppService              string
	HTTPName                string
	DefaultScheme           string
	DefaultDomain           string
	DatabaseService         string
	CheckDatabaseDependency bool
	SolrService             string
	SolrCore                string
}

type standardComposeWebHealthcheckRunner struct {
	opts StandardComposeWebHealthcheckOptions
}

// RegisterHealthcheckRunner registers a healthcheck runner for the plugin. The
// SDK stores the handler that is invoked through the plugin RPC entrypoint. The
// handler output is encoded into the RPC result and merged with core health
// results before writing the final report. If BindFlags uses a plugin-specific
// flag for CodebaseRootfs params, mark it with MarkCodebaseRootfsFlag.
func (s *SDK) RegisterHealthcheckRunner(runner HealthcheckRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "healthcheck",
		Short:        "Internal healthcheck hook",
		Hidden:       true,
		SilenceUsage: true,
	}
	runner.BindFlags(cmd)
	s.registerHealthcheckCommand(cmd)
	s.healthcheckRunner = runner
	s.hasHealthcheck = true
}

// StandardComposeWebHealthcheck returns a reusable healthcheck runner for
// MariaDB-backed Compose web applications. Core healthcheck already verifies
// service containers; this runner adds the app route, MariaDB ping, optional
// app->MariaDB service_healthy policy check, and optional Solr core check.
func StandardComposeWebHealthcheck(opts StandardComposeWebHealthcheckOptions) HealthcheckRunner {
	return standardComposeWebHealthcheckRunner{opts: opts}
}

func (r standardComposeWebHealthcheckRunner) BindFlags(cmd *cobra.Command) {}

func (r standardComposeWebHealthcheckRunner) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	checker, err := corehealthcheck.NewDockerChecker(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = checker.Close() }()

	appService := firstHealthcheckValue(r.opts.AppService, "app")
	databaseService := firstHealthcheckValue(r.opts.DatabaseService, "mariadb")
	results := []sitevalidate.Result{
		checker.CheckHTTPRoute(
			cmd.Context(),
			firstHealthcheckValue(r.opts.HTTPName, "http:"+appService),
			appService,
			corehealthcheck.PublicURLFromEnv(ctx, firstHealthcheckValue(r.opts.DefaultScheme, "http"), firstHealthcheckValue(r.opts.DefaultDomain, "localhost")),
		),
		checker.CheckMariaDB(cmd.Context(), databaseService),
	}
	if r.opts.CheckDatabaseDependency {
		results = append(results, checker.CheckComposeServiceDependsOnHealthy(cmd.Context(), appService, databaseService))
	}
	if r.opts.SolrService != "" || r.opts.SolrCore != "" {
		results = append(results, checker.CheckSolrCore(cmd.Context(), firstHealthcheckValue(r.opts.SolrService, "solr"), firstHealthcheckValue(r.opts.SolrCore, "default")))
	}
	return results, nil
}

func firstHealthcheckValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *SDK) runHealthcheckRunner(cmd *cobra.Command, runner HealthcheckRunner) ([]sitevalidate.Result, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	return runner.Run(cmd, ctx)
}
