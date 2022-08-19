package helmrender

import (
	"bytes"

	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"helm.sh/helm/v3/pkg/postrender"
)

type combinedPostRenderer struct {
	renderers []postrender.PostRenderer
}
func newCombinedPostRenderer() combinedPostRenderer {
	return combinedPostRenderer{
		renderers: make([]postrender.PostRenderer, 0),
	}
}

func (c *combinedPostRenderer) addRenderer(renderer postrender.PostRenderer) {
	c.renderers = append(c.renderers, renderer)
}

func (c *combinedPostRenderer) Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error) {
	var result *bytes.Buffer = renderedManifests
	for _, renderer := range c.renderers {
		result, err = renderer.Run(result)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func postRenderers(hr v2.HelmRelease) (postrender.PostRenderer, error) {
	var combinedRenderer = newCombinedPostRenderer()
	for _, r := range hr.Spec.PostRenderers {
		if r.Kustomize != nil {
			combinedRenderer.addRenderer(newPostRendererKustomize(r.Kustomize))
		}
	}
	combinedRenderer.addRenderer(newPostRendererOriginLabels(&hr))
	if len(combinedRenderer.renderers) == 0 {
		return nil, nil
	}
	return &combinedRenderer, nil
}