package action

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	githubactions "github.com/sethvargo/go-githubactions"
	"github.com/tobiash/flux-helm-preview/pkg/diff"
	"github.com/tobiash/flux-helm-preview/pkg/helmrender"
	"github.com/tobiash/flux-helm-preview/pkg/render"
	"helm.sh/helm/v3/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type Config struct {
	Helm             bool
	Kustomizations   []string
	RepoA            string
	RepoB            string
	WriteMarkdown    string
	MarkdownTemplate string
}

type Action struct {
	ctx        context.Context
	cfg        Config
	log        logr.Logger
	action     *githubactions.Action
	helmRunner *helmrender.Runner
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
	return &action, nil
}

func (a *Action) Run() error {
	repoA, err := a.loadRepo(a.cfg.RepoA)
	if err != nil {
		return err
	}
	repoB, err := a.loadRepo(a.cfg.RepoB)
	if err != nil {
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

	return r, nil
}
