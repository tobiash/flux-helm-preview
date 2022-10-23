package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/alecthomas/kingpin.v2"
	"gopkg.in/yaml.v2"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	helmcli "helm.sh/helm/v3/pkg/cli"

	"github.com/tobiash/flux-helm-preview/pkg/diff"
	"github.com/tobiash/flux-helm-preview/pkg/filter"
	"github.com/tobiash/flux-helm-preview/pkg/helmrender"
	"github.com/tobiash/flux-helm-preview/pkg/render"
)


var (
	app = kingpin.New("flux-helm-preview", "A tool to preview changes in Flux / Helm deployments.")

	helmRegistryConfig   = app.Flag("registry-config", "Helm Registry Config").String()
	helmRepositoryConfig = app.Flag("repository-config", "Helm Repository Config").String()
	helmRepositoryCache  = app.Flag("repository-cache", "Helm Repository Cache").String()

	kustomizations = app.Flag("kustomization", "Kustomize base to render (relative to path)").Short('k').Required().Strings()
	renderHelm     = app.Flag("render-helm", "Render HelmRelease objects").Short('H').Default("true").Bool()

	filtersFile = app.Flag("filter", "KIO filters definition file").File()

	renderCmd  = app.Command("render", "Render a single path.")
	renderPath = renderCmd.Arg("path", "Path to render.").Required().ExistingDir()

	diffCmd   = app.Command("diff", "Diff two paths.")
	diffPathA = diffCmd.Arg("a", "First path.").Required().ExistingDir()
	diffPathB = diffCmd.Arg("b", "Second path.").Required().ExistingDir()

	filters *filter.FilterConfig
)

func helmSettings() *helmcli.EnvSettings {
	settings := helmcli.New()
	if helmRepositoryConfig != nil && *helmRepositoryConfig != "" {
		settings.RepositoryConfig = *helmRepositoryConfig
	}
	if helmRegistryConfig != nil && *helmRegistryConfig != "" {
		settings.RegistryConfig = *helmRegistryConfig
	}
	if helmRepositoryCache != nil && *helmRepositoryCache != "" {
		settings.RepositoryCache = *helmRepositoryCache
	}
	return settings
}

func loadRepo(log logr.Logger, repoPath string, helmRunner *helmrender.Runner) (*render.Render, error) {
	r := render.NewDefaultRender(log)
	for _, k := range *kustomizations {
		err := r.AddKustomization(filesys.MakeFsOnDisk(), filepath.Join(repoPath, k))
		if err != nil {
			return nil, fmt.Errorf("failed to add kustomization: %w", err)
		}
	}
	if helmRunner != nil {
		helm, err := helmrender.ParseHelmRepo(r, helmRunner, log)
		if err != nil {
			return nil, fmt.Errorf("failed to parse helm repo: %w", err)
		}
		rc, err := helm.RenderAllCharts()
		if err != nil {
			return nil, fmt.Errorf("failed to render helm charts: %w", err)
		}
		if err = r.AppendAll(rc); err != nil {
			return nil, err
		}
	}


	if filters != nil {
		for _, f := range filters.Filters {
			if err := r.ApplyFilter(f.Filter); err != nil {
				return nil, err
			}
		}
	}

	return r, nil
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerologr.NameFieldName = "logger"
	zerologr.NameSeparator = "/"

	zl := zerolog.New(os.Stderr)
	zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
	var log logr.Logger = zerologr.New(&zl)

	var helmRunner *helmrender.Runner

	kingpin.CommandLine.HelpFlag.Short('h')

	cmd := kingpin.MustParse(app.Parse(os.Args[1:]))

	if renderHelm != nil && *renderHelm {
		helmRunner = helmrender.NewRunner(helmSettings(), log)
	}

	if *filtersFile != nil {
		m := &filter.FilterConfig{}
		d := yaml.NewDecoder(*filtersFile)
		err := d.Decode(m)
		app.FatalIfError(err, "error decoding modifier")
		filters = m
	}

	switch cmd {
	case renderCmd.FullCommand():
		r, err := loadRepo(log, *renderPath, helmRunner)
		app.FatalIfError(err, "error loading repo")
		yaml, err := r.AsYaml()
		app.FatalIfError(err, "error transforming to yaml")
		_, err = os.Stdout.Write(yaml)
		app.FatalIfError(err, "error writing output")

	case diffCmd.FullCommand():
		a, err := loadRepo(log, *diffPathA, helmRunner)
		app.FatalIfError(err, "error loading first path")
		b, err := loadRepo(log, *diffPathB, helmRunner)
		app.FatalIfError(err, "error loading second path")
		err = diff.Diff(a, b, os.Stdout)
		app.FatalIfError(err, "error creating diff")
	}
}
