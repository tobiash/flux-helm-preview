package main

import (
	"os"

	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	helmcli "helm.sh/helm/v3/pkg/cli"

	"github.com/tobiash/flux-helm-preview/pkg/preview"
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

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerologr.NameFieldName = "logger"
	zerologr.NameSeparator = "/"

	zl := zerolog.New(os.Stderr)
	zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
	var log logr.Logger = zerologr.New(&zl)

	kingpin.CommandLine.HelpFlag.Short('h')
	cmd := kingpin.MustParse(app.Parse(os.Args[1:]))

	opts := []preview.Opt{
		preview.WithLogger(log),
		preview.WithKustomizations(*kustomizations),
	}

	if renderHelm != nil && *renderHelm {
		opts = append(opts, preview.WithHelm(helmSettings()))
	}

	if *filtersFile != nil {
		opts = append(opts, preview.WithFilterFile(*filtersFile))
	}

	p, err := preview.New(opts...)
	app.FatalIfError(err, "error creating preview")

	switch cmd {
	case renderCmd.FullCommand():
		err := p.Render(*renderPath, os.Stdout)
		app.FatalIfError(err, "error rendering")

	case diffCmd.FullCommand():
		err := p.Diff(*diffPathA, *diffPathB, os.Stdout)
		app.FatalIfError(err, "error creating diff")
	}
}
