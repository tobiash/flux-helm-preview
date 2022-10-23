package action

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	githubactions "github.com/sethvargo/go-githubactions"
	"github.com/tobiash/flux-helm-preview/pkg/diff"
	"github.com/tobiash/flux-helm-preview/pkg/filter"
	"github.com/tobiash/flux-helm-preview/pkg/helmrender"
	"github.com/tobiash/flux-helm-preview/pkg/render"
	"golang.org/x/sync/errgroup"
	"helm.sh/helm/v3/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

type Config struct {
	Helm             bool
	Kustomizations   []string
	RepoA            string
	RepoB            string
	WriteMarkdown    string
	MarkdownTemplate string
	Filter           string
}

type Action struct {
	ctx        context.Context
	cfg        Config
	log        logr.Logger
	action     *githubactions.Action
	helmRunner *helmrender.Runner
	filter     *filter.FilterConfig
}

type MarkdownContext struct {
	Diff           string
	RepoA          string
	RepoB          string
	Kustomizations []string
}

func NewFromInputs(action *githubactions.Action) (*Config, error) {
	kustomizations := strings.Split(action.GetInput("kustomizations"), "\n")
	cfg := &Config{
		RepoA: action.GetInput("repo-a"),
		RepoB: action.GetInput("repo-b"),
	}
	if cfg.RepoA == "" || cfg.RepoB == "" {
		return nil, fmt.Errorf("must configure both repo-a and repo-b")
	}
	if action.GetInput("helm") == "true" {
		cfg.Helm = true
	}
	for _, k := range kustomizations {
		ks := strings.TrimSpace(k)
		if ks != "" {
			cfg.Kustomizations = append(cfg.Kustomizations, ks)
		}
	}
	cfg.WriteMarkdown = action.GetInput("write-markdown")
	cfg.MarkdownTemplate = action.GetInput("markdown-template")
	cfg.Filter = action.GetInput("filter")
	return cfg, nil
}

func NewAction(ctx context.Context, cfg *Config, ghaction *githubactions.Action) (*Action, error) {
	log := logr.FromContextOrDiscard(ctx)
	action := Action{
		ctx:    ctx,
		cfg:    *cfg,
		log:    log,
		action: ghaction,
	}
	if cfg.Helm {
		action.helmRunner = helmrender.NewRunner(cli.New(), log)
	}
	if cfg.Filter != "" {
		m := &filter.FilterConfig{}
		if err := yaml.Unmarshal([]byte(cfg.Filter), m); err != nil {
			return nil, err
		}
		action.filter = m
	}
	return &action, nil
}

func (a *Action) renderFn(repo string, out **render.Render) func () error {
	return func() error {
		var err error
		*out, err = a.loadRepo(repo)
		if err != nil {
			return err
		}
		return nil
	}
}

func (a *Action) Run() error {
	g, _ := errgroup.WithContext(context.Background())
	var repoA, repoB *render.Render
	g.Go(a.renderFn(a.cfg.RepoA, &repoA))
	g.Go(a.renderFn(a.cfg.RepoB, &repoB))
	if err := g.Wait(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := diff.Diff(repoA, repoB, &buf); err != nil {
		return err
	}
	// a.action.AddStepSummary(fmt.Sprintf("```\n%s\n```", string(buf.Bytes())))
	a.action.SetOutput("diff", string(buf.Bytes()))
	if a.cfg.WriteMarkdown != "" {
		return a.writeMarkdown(string(buf.Bytes()))
	}
	return nil
}

func (a *Action) writeMarkdown(diff string) error {
	mdCtx := MarkdownContext{
		Diff:           diff,
		RepoA:          a.cfg.RepoA,
		RepoB:          a.cfg.RepoB,
		Kustomizations: a.cfg.Kustomizations,
	}
	tpl, err := template.New("markdown").Parse(a.cfg.MarkdownTemplate)
	if err != nil {
		return fmt.Errorf("error parsing markdown template: %w", err)
	}
	f, err := os.Create(a.cfg.WriteMarkdown)
	if err != nil {
		return err
	}
	defer f.Close()
	return tpl.Execute(f, &mdCtx)
}

func (a *Action) loadRepo(repo string) (*render.Render, error) {
	r := render.NewDefaultRender(a.log.WithValues("repo", repo))
	for _, k := range a.cfg.Kustomizations {
		err := r.AddKustomization(filesys.MakeFsOnDisk(), filepath.Join(repo, k))
		if err != nil {
			return nil, fmt.Errorf("failed to add kustomization: %w", err)
		}
	}

	if a.helmRunner == nil {
		return r, nil
	}
	helm, err := helmrender.ParseHelmRepo(r, a.helmRunner, a.log)
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
	if a.filter != nil {
		for _, f := range a.filter.Filters {
			if err := r.ApplyFilter(f.Filter); err != nil {
				return nil, err
			}
		}
	}

	return r, nil
}
