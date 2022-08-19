package runner

import (
	"bytes"
	"fmt"
	"io/ioutil"

	hr "github.com/fluxcd/helm-controller/api/v2beta1"
	source "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/mattn/go-zglob"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"k8s.io/client-go/kubernetes/scheme"

	"github.com/go-logr/logr"
	"helm.sh/helm/v3/pkg/action"
)

type Runner struct {
	config *action.Configuration
	logger logr.Logger
}

type FluxRepo struct {
	releases     []hr.HelmRelease
	repositories []source.HelmRepository
	decode       func([]byte, *schema.GroupVersionKind, runtime.Object) (runtime.Object, *schema.GroupVersionKind, error)
}

func (f *FluxRepo) tryDecode(b []byte, logger logr.Logger) error {
	obj, groupVersionKind, err := f.decode(b, nil, nil)
	if err != nil {
		return err
	}
	logger.Info("found manifest", "group", groupVersionKind.Group, "kind", groupVersionKind.Kind)
	if groupVersionKind.Group == "helm.toolkit.fluxcd.io" &&
		groupVersionKind.Kind == "HelmRelease" {
		release := obj.(*hr.HelmRelease)
		f.releases = append(f.releases, *release)
		logger.Info("found helm release", "name", release.Name, "namespace", release.Namespace)
	} else if groupVersionKind.Group == "source.toolkit.fluxcd.io" &&
		groupVersionKind.Kind == "HelmRepository" {
		repo := obj.(*source.HelmRepository)
		f.repositories = append(f.repositories, *repo)
		logger.Info("found helm repository", "name", repo.Name, "namespace", repo.Namespace)
	}
	return nil
}

func NewFluxRepo() (*FluxRepo, error) {
	releases := []hr.HelmRelease{}
	repositories := []source.HelmRepository{}
	decode, err := prepareDecode()
	if err != nil {
		return nil, err
	}

	return &FluxRepo{
		releases:     releases,
		repositories: repositories,
		decode:       decode,
	}, nil
}

func prepareDecode() (func([]byte, *schema.GroupVersionKind, runtime.Object) (runtime.Object, *schema.GroupVersionKind, error), error) {
	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)
	if err := hr.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("error adding helm-controller scheme: %w", err)
	}
	if err := source.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("error adding source-controller scheme: %w", err)
	}
	
	return serializer.NewCodecFactory(sch).UniversalDeserializer().Decode, nil
}

func (repo *FluxRepo) ParseKustomize(path string, logger logr.Logger) (error) {

	var b bytes.Buffer
	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resmap, err := kustomizer.Run(filesys.MakeFsOnDisk(), path)

	resmap.Resources()[0].

	if err != nil {
		return err
	}
	docBytes := bytes.Split(b.Bytes(), []byte("---\n"))
	for di, d := range docBytes {
		repo.tryDecode(d, logger.WithValues("doc", di))
	}
	return nil
}

func (repo *FluxRepo) ParseGlob(pathGlob string, logger logr.Logger) (error) {
	matches, err := zglob.Glob(pathGlob)
	if err != nil {
		return fmt.Errorf("error executing glob: %w", err)
	}

	for _, m := range matches {
		logger := logger.WithValues("file", m)
		logger.Info("reading file")
		fileBytes, err := ioutil.ReadFile(m)
		if err != nil {
			continue
		}
		docBytes := bytes.Split(fileBytes, []byte("---\n"))
		for di, d := range docBytes {
			repo.tryDecode(d, logger.WithValues("doc", di))
		}
	}
	return nil
}

func NewRunner() *Runner {
	return &Runner{}
}
