package action

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	githubactions "github.com/sethvargo/go-githubactions"
	"github.com/tobiash/flux-helm-preview/pkg/preview"
	"helm.sh/helm/v3/pkg/cli"
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
	ctx     context.Context
	cfg Config
	action  *githubactions.Action
	preview *preview.Preview
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
	opts := []preview.Opt{
		preview.WithLogger(log),
		preview.WithKustomizations(cfg.Kustomizations),
	}
	if cfg.Helm {
		opts = append(opts, preview.WithHelm(cli.New()))
	}
	if cfg.Filter != "" {
		opts = append(opts, preview.WithFilterYAML(cfg.Filter))
	}
	p, err := preview.New(opts...)

	if err != nil {
		return nil, err
	}

	action := Action{
		ctx:    ctx,
		cfg: *cfg,
		action: ghaction,
		preview: p,
	}

	return &action, nil
}

func (a *Action) Run() error {
	var buf bytes.Buffer
	if err := a.preview.Diff(a.cfg.RepoA, a.cfg.RepoB, &buf); err != nil {
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
