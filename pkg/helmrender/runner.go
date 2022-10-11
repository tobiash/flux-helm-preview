package helmrender

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/kustomize/api/hasher"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
)

type Runner struct {
	settings *cli.EnvSettings
	logger   logr.Logger
	storage  repo.File
	lock     sync.Mutex
	repos    sync.Map
}

type RenderTask struct {
	values          chartutil.Values
	chart           string
	version         string
	repo            repo.Entry
	releaseName     string
	namespace       string
	createNamespace bool
	skipCRDs        bool
	replace         bool
	disableHooks    bool
	includeCRDs     bool
}

func NewRunner(settings *cli.EnvSettings, log logr.Logger) *Runner {
	return &Runner{
		settings: settings,
		logger:   log,
	}
}

func (r *Runner) RenderCharts(ctx context.Context, releases []RenderTask) (resmap.ResMap, error) {
	res := resmap.New()
	g, ctx := errgroup.WithContext(ctx)

	results := make([]resmap.ResMap, len(releases))
	for i, h := range releases {
		i := i
		h := h
		g.Go(func() error {
			r, err := r.renderChart(ctx, &h)
			if err != nil {
				return err
			}
			results[i] = r
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for _, r := range results {
		res.AbsorbAll(r)
	}
	return res, nil
}

func (r *Runner) renderChart(ctx context.Context, t *RenderTask) (resmap.ResMap, error) {
	cfg := new(action.Configuration)
	cfg.Init(r.settings.RESTClientGetter(), t.namespace, os.Getenv("HELM_DRIVER"), func(format string, args ...interface{}) {
		r.logger.Info(fmt.Sprintf(format, args...))
	})

	install := action.NewInstall(cfg)
	install.DryRun = true
	install.ClientOnly = true
	install.CreateNamespace = t.createNamespace
	install.ReleaseName = t.releaseName
	install.SkipCRDs = t.skipCRDs
	install.Replace = t.replace
	install.DisableHooks = t.disableHooks
	install.APIVersions = []string{}
	install.IncludeCRDs = t.includeCRDs

	if err := r.getAndUpdateRepo(&t.repo); err != nil {
		return nil, err
	}
	install.ChartPathOptions.Version = t.version
	install.ChartPathOptions.RepoURL = t.repo.URL
	install.ChartPathOptions.Username = t.repo.Username
	install.ChartPathOptions.Password = t.repo.Password
	install.ChartPathOptions.CaFile = t.repo.CAFile
	install.ChartPathOptions.CertFile = t.repo.CertFile
	install.ChartPathOptions.InsecureSkipTLSverify = t.repo.InsecureSkipTLSverify
	install.ChartPathOptions.PassCredentialsAll = t.repo.PassCredentialsAll
	install.ChartPathOptions.KeyFile = t.repo.KeyFile

	r.settings.Debug = true

	cp, err := install.ChartPathOptions.LocateChart(t.chart, r.settings)
	if err != nil {
		return nil, fmt.Errorf("error locating chart: %w", err)
	}
	r.logger.Info("Loaded chart from repo", "chart", t.chart, "repo", t.repo.Name, "repo", t.repo.URL, "path", cp)
	chart, err := loader.Load(cp)
	if err != nil {
		return nil, err
	}
	out := new(bytes.Buffer)
	rel, err := install.Run(chart, t.values)
	if err != nil {
		return nil, err
	}
	if rel != nil {
		var manifests bytes.Buffer
		fmt.Fprintln(&manifests, strings.TrimSpace(rel.Manifest))
		if !install.DisableHooks {
			for _, m := range rel.Hooks {
				fmt.Fprintf(&manifests, "---\n# Source: %s\n%s\n", m.Path, m.Manifest)
			}
		}
		fmt.Fprintf(out, "%s", manifests.String())
	}
	return resmap.NewFactory(resource.NewFactory(&hasher.Hasher{})).NewResMapFromBytes(out.Bytes())
}

func (r *Runner) getAndUpdateRepo(entry *repo.Entry) error {
	_, ok := r.repos.Load(entry.URL)
	if ok {
		return nil
	}

	r.lock.Lock()
	defer r.lock.Unlock()
	_, ok = r.repos.Load(entry.URL)
	if ok {
		return nil
	}

	chartRepo, err := repo.NewChartRepository(entry, getter.All(r.settings))
	if err != nil {
		return err
	}
	chartRepo.CachePath = r.settings.RepositoryCache
	_, err = chartRepo.DownloadIndexFile()
	if err != nil {
		return err
	}
	if r.storage.Has(entry.Name) {
		return nil
	}
	r.storage.Update(entry)
	err = r.storage.WriteFile(r.settings.RegistryConfig, 0o644)
	if err != nil {
		return err
	}
	r.repos.Store(entry.URL, true) // TODO
	return nil
}
