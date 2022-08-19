package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	helmcli "helm.sh/helm/v3/pkg/cli"

	"github.com/tobiash/flux-helm-preview/pkg/diff"
	"github.com/tobiash/flux-helm-preview/pkg/helmrender"
	"github.com/tobiash/flux-helm-preview/pkg/render"
)

var helmSettings = []cli.Flag{
	&cli.StringFlag{
		Name: "registry-config",
	},
	&cli.StringFlag{
		Name: "repository-config",
	},
	&cli.StringFlag{
		Name: "repository-cache",
	},
}

var renderFlags = []cli.Flag{
	&cli.StringSliceFlag{
		Name: "k",
	},
	&cli.BoolFlag{
		Name: "H",
	},
}

func helmSettingsFromContext(ctx *cli.Context) (*helmcli.EnvSettings) {
	settings := helmcli.New()
	// settings.RepositoryConfig = ctx.String("repository-config")
	// settings.RepositoryCache =  ctx.String("repository-cache")
	// settings.RegistryConfig = ctx.String("registry-config")
	return settings
}

func loadRepo(ctx *cli.Context, log logr.Logger, repoPath string) (*render.Render, error) {
	r := render.NewDefaultRender(log)
	for _, k := range ctx.StringSlice("k") {
		err := r.AddKustomization(filesys.MakeFsOnDisk(), filepath.Join(repoPath, k))
		if err != nil {
			return nil, fmt.Errorf("failed to add kustomization: %w", err)
		}
	}
	if ctx.Bool("H") {
		helm, err := helmrender.ParseHelmRepo(r, helmSettingsFromContext(ctx), log)
		if err != nil {
			return nil, err
		}
		rc, err := helm.RenderAllCharts()
		if err != nil {
			return nil, err
		}
		if err = r.AppendAll(rc); err != nil {
			return nil, err
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

	app := cli.App{
		Name: "flux-helm-preview",
		Commands: []*cli.Command{
			{
				Name:      "render",
				Flags:     append([]cli.Flag{}, renderFlags...),
				ArgsUsage: "<repo>",
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 1 {
						return cli.NewExitError("missing repo", 1)
					}
					repoPath := ctx.Args().First()
					r, err := loadRepo(ctx, log, repoPath)
					if err != nil {
						return err
					}

					yaml, err := r.AsYaml()
					if err != nil {
						return err
					}
					_, err = os.Stdout.Write(yaml)
					return err
				},
			},
			{
				Name:  "diff",
				Flags: append([]cli.Flag{}, renderFlags...),
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 2 {
						return cli.NewExitError("missing repos", 1)
					}
					a, err := loadRepo(ctx, log, ctx.Args().Get(0))
					if err != nil {
						return err
					}
					b, err := loadRepo(ctx, log, ctx.Args().Get(1))
					if err != nil {
						return err
					}
					err = diff.Diff(a, b, os.Stdout)
					if err != nil {
						return err
					}
					return nil
				},
			},
		},
		Action: func(ctx *cli.Context) error {
			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Error(err, "error running app")
		os.Exit(1)
	}
}
