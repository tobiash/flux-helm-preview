package preview

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-helm-preview/pkg/diff"
	"github.com/tobiash/flux-helm-preview/pkg/filter"
	"github.com/tobiash/flux-helm-preview/pkg/helmrender"
	"github.com/tobiash/flux-helm-preview/pkg/render"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
	helmcli "helm.sh/helm/v3/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type Preview struct {
	kustomizations []string
	filters        *filter.FilterConfig
	helmsettings   *helmcli.EnvSettings
	helmrunner     *helmrender.Runner
	log            logr.Logger
	ctx            context.Context
}

func (p *Preview) loadRepo(path string) (*render.Render, error) {
	r := render.NewDefaultRender(p.log.WithValues("renderPath", path))
	for _, k := range p.kustomizations {
		err := r.AddKustomization(filesys.MakeFsOnDisk(), filepath.Join(path, k))
		if err != nil {
			return nil, fmt.Errorf("failed to add kustomization: %w", err)
		}
	}

	if p.helmrunner != nil {
		helm, err := helmrender.ParseHelmRepo(r, p.helmrunner, p.log)
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

	if p.filters != nil {
		for _, f := range p.filters.Filters {
			if err := r.ApplyFilter(f.Filter); err != nil {
				return nil, err
			}
		}
	}

	return r, nil
}

func (p *Preview) Render(path string, out io.Writer) error {
	r, err := p.loadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	yaml, err := r.AsYaml()
	if err != nil {
		return fmt.Errorf("error transforming to yaml: %w", err)
	}
	_, err = out.Write(yaml)
	if err != nil {
		fmt.Errorf("error writing output: %w", err)
	}
	return nil
}

func (a *Preview) renderFn(repo string, out **render.Render) func () error {
	return func() error {
		var err error
		*out, err = a.loadRepo(repo)
		if err != nil {
			return err
		}
		return nil
	}
}


func (p *Preview) Diff(a, b string, out io.Writer) error {
	g, _ := errgroup.WithContext(p.ctx)
	var ar, br *render.Render
	g.Go(p.renderFn(a, &ar))
	g.Go(p.renderFn(b, &br))
	if err := g.Wait(); err != nil {
		return fmt.Errorf("render error: %w", err)
	}
	if err := diff.Diff(ar, br, out); err != nil {
		return fmt.Errorf("diff error: %w", err)
	}
	return nil
}

type Opt func(p *Preview) error

func New(opts ...Opt) (*Preview, error) {
	var p Preview
	for _, opt := range opts {
		if err := opt(&p); err != nil {
			return nil, err
		}
	}
	if p.helmsettings != nil {
		p.helmrunner = helmrender.NewRunner(p.helmsettings, p.log)
	}
	if p.ctx == nil {
		p.ctx = context.TODO()
	}
	return &p, nil
}

func WithLogger(log logr.Logger) Opt {
	return func(p *Preview) error {
		p.log = log
		return nil
	}
}

func WithFilterFile(f *os.File) Opt {
	return func(p *Preview) error {
		m := &filter.FilterConfig{}
		d := yaml.NewDecoder(f)
		if err := d.Decode(m); err != nil {
			return err
		}
		p.filters = m
		return nil
	}
}

func WithFilterYAML(f string) Opt {
	return func (p *Preview) error {
		m := &filter.FilterConfig{}
		if err := yaml.Unmarshal([]byte(f), m); err != nil {
			return err
		}
		p.filters = m
		return nil
	}
}

func WithHelm(helmsettings *helmcli.EnvSettings) Opt {
	return func(p *Preview) error {
		p.helmsettings = helmsettings
		return nil
	}
}

func WithKustomizations(kustomizations []string) Opt {
	return func(p *Preview) error {
		p.kustomizations = append(p.kustomizations, kustomizations...)
		return nil
	}
}
