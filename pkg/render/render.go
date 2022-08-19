package render

import (
	"github.com/go-logr/logr"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Render is a set of rendered yaml
type Render struct {
	resmap.ResMap
	kustomizer *krusty.Kustomizer
	log logr.Logger
}

func NewDefaultRender(log logr.Logger) *Render {
	return &Render{
		ResMap:    resmap.New(),
		kustomizer: krusty.MakeKustomizer(krusty.MakeDefaultOptions()),
		log: log,
	}
}

func (r *Render) AddKustomization(fSys filesys.FileSystem, path string) error {
	resmap, err := r.kustomizer.Run(fSys, path)
	if err != nil {
		return err
	}
	return r.AppendAll(resmap)
}
