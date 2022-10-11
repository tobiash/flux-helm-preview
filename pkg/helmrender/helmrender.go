package helmrender

import (
	"context"
	"fmt"
	"strings"

	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/runtime/transform"
	source "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/tobiash/flux-helm-preview/pkg/render"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/strvals"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

var HELM_RELEASE_GVK = resid.NewGvk("helm.toolkit.fluxcd.io", "v2beta1", "HelmRelease")
var HELM_REPO_V1BETA1_GVK = resid.NewGvk("source.toolkit.fluxcd.io", "v1beta1", "HelmRepository")
var HELM_REPO_V1BETA2_GVK = resid.NewGvk("source.toolkit.fluxcd.io", "v1beta2", "HelmRepository")
var SECRET_GVK = resid.NewGvk("", "v1", "Secret")
var CONFIGMAP_GVK = resid.NewGvk("", "v1", "ConfigMap")

type HelmRepo struct {
	render.Render
	runner       *Runner
	scheme       *runtime.Scheme
	releases     []v2.HelmRelease
	repositories []source.HelmRepository
	logger       logr.Logger
}

func ParseHelmRepo(r *render.Render, runner *Runner, log logr.Logger) (*HelmRepo, error) {
	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)

	if err := v2.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("error adding helm-controller scheme: %w", err)
	}
	if err := source.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("error adding source-controller scheme: %w", err)
	}

	var repo HelmRepo
	repo.Render = *r
	repo.scheme = sch
	repo.logger = log
	repo.runner = runner

	for _, res := range r.Resources() {
		log.Info("found manifest", "group", res.GetGvk().Group, "kind", res.GetGvk().Kind, "version", res.GetGvk().Version)
		switch res.GetGvk() {
		case HELM_RELEASE_GVK:
			var release v2.HelmRelease
			if err := repo.convertTyped(res, &release); err != nil {
				return nil, fmt.Errorf("error converting resource: %w", err)
			}
			log.Info("found helm release", "name", release.Name, "namespace", release.Namespace)
			repo.releases = append(repo.releases, release)
		case HELM_REPO_V1BETA1_GVK, HELM_REPO_V1BETA2_GVK:
			var hrepo source.HelmRepository
			res = res.DeepCopy()
			res.SetApiVersion(HELM_REPO_V1BETA2_GVK.ApiVersion())
			if err := repo.convertTyped(res, &hrepo); err != nil {
				return nil, fmt.Errorf("error converting resource: %w", err)
			}
			log.Info("found helm repository", "name", hrepo.Name, "namespace", hrepo.Namespace)
			repo.repositories = append(repo.repositories, hrepo)
		}
	}

	return &repo, nil
}

func (r *HelmRepo) RenderAllCharts() (resmap.ResMap, error) {
	tasks := make([]RenderTask, len(r.releases))
	for i, h := range r.releases {
		values, err := r.composeValues(h)
		if err != nil {
			return nil, fmt.Errorf("error composing values: %w", err)
		}
		url, err := r.findHelmChartUrl(&h)
		if err != nil {
			return nil, err
		}

		tasks[i] = RenderTask{
			values: values,
			chart:  h.Spec.Chart.Spec.Chart,
			repo: repo.Entry{
				URL:  url,
				Name: fmt.Sprintf("%s-%s", h.GetNamespace(), h.GetName()),
			},
			releaseName:     h.GetReleaseName(),
			skipCRDs:        h.Spec.GetInstall().SkipCRDs,
			replace:         h.Spec.GetInstall().Replace,
			disableHooks:    h.Spec.GetInstall().DisableHooks,
			createNamespace: h.Spec.GetInstall().CreateNamespace,
		}
	}
	return r.runner.RenderCharts(context.Background(), tasks)
}

func (r *HelmRepo) composeValues(hr v2.HelmRelease) (chartutil.Values, error) {
	var result chartutil.Values
	logger := r.logger.WithValues("release", hr.Name, "namespace", hr.Namespace)

	for _, v := range hr.Spec.ValuesFrom {
		namespacedName := types.NamespacedName{Namespace: hr.Namespace, Name: v.Name}
		var valuesData []byte

		switch v.Kind {
		case "ConfigMap":
			var cm corev1.ConfigMap
			found, err := r.findResource(CONFIGMAP_GVK, namespacedName, &cm)
			if err != nil {
				return nil, fmt.Errorf("error loading configmap %s: %w", namespacedName, err)
			}
			if !found {
				logger.Info("configmap not found, ignoring values", "configmap", namespacedName)
				continue
			}
			if data, ok := cm.Data[v.GetValuesKey()]; !ok {
				return nil, fmt.Errorf("missing key '%s' in %s '%s'", v.Kind, v.GetValuesKey(), namespacedName)
			} else {
				valuesData = []byte(data)
			}
		case "Secret":
			var secret corev1.Secret
			found, err := r.findResource(SECRET_GVK, namespacedName, &secret)
			if err != nil {
				return nil, fmt.Errorf("error loading secret %s: %w", namespacedName, err)
			}
			if !found {
				logger.Info("secret not found, ignoring values", "secret", namespacedName)
				continue
			}
			if data, ok := secret.Data[v.GetValuesKey()]; !ok {
				return nil, fmt.Errorf("missing key '%s' in %s '%s'", v.Kind, v.GetValuesKey(), namespacedName)
			} else {
				valuesData = []byte(data)
			}
		default:
			return nil, fmt.Errorf("unsupported ValuesReference kind '%s'", v.Kind)
		}
		switch v.TargetPath {
		case "":
			values, err := chartutil.ReadValues(valuesData)
			if err != nil {
				return nil, fmt.Errorf("error reading values from %s '%s': %w", v.Kind, namespacedName, err)
			}
			result = transform.MergeMaps(result, values)
		default:
			stringValuesData := string(valuesData)
			const singleQuote = "'"
			const doubleQuote = "\""
			var err error
			if (strings.HasPrefix(stringValuesData, singleQuote) && strings.HasSuffix(stringValuesData, singleQuote)) || (strings.HasPrefix(stringValuesData, doubleQuote) && strings.HasSuffix(stringValuesData, doubleQuote)) {
				stringValuesData = strings.Trim(stringValuesData, singleQuote+doubleQuote)
				singleValue := v.TargetPath + "=" + stringValuesData
				err = strvals.ParseIntoString(singleValue, result)
			} else {
				singleValue := v.TargetPath + "=" + stringValuesData
				err = strvals.ParseInto(singleValue, result)
			}
			if err != nil {
				return nil, fmt.Errorf("unable to merge value from key '%s' in %s '%s' into target path '%s': %w", v.GetValuesKey(), v.Kind, namespacedName, v.TargetPath, err)
			}
		}
	}
	return transform.MergeMaps(result, hr.GetValues()), nil
}

func (r *HelmRepo) findHelmChartUrl(source *v2.HelmRelease) (string, error) {
	namespace := source.Spec.Chart.Spec.SourceRef.Namespace
	if namespace == "" {
		namespace = source.GetNamespace()
	}
	switch source.Spec.Chart.Spec.SourceRef.Kind {
	case "HelmRepository":
		for _, hr := range r.repositories {
			if hr.GetNamespace() == namespace && hr.GetName() == source.Spec.Chart.Spec.SourceRef.Name {
				return hr.Spec.URL, nil
			}
		}
	default:
		return "", fmt.Errorf("unsupported source kind '%s'", source.Spec.Chart.Spec.SourceRef.Kind)
	}
	return "", fmt.Errorf("unable to find source '%s'", source.Spec.Chart.Spec.SourceRef.Name)
}

func (r *HelmRepo) findResource(gvk resid.Gvk, namespacedName types.NamespacedName, to interface{}) (bool, error) {
	res, err := r.GetById(resid.NewResIdWithNamespace(gvk, namespacedName.Name, namespacedName.Namespace))
	if err != nil {
		return false, nil
	}
	if err := r.convertTyped(res, to); err != nil {
		return false, err
	}
	return true, nil
}

func (r *HelmRepo) convertTyped(from *resource.Resource, to interface{}) error {
	var u unstructured.Unstructured
	m, err := from.Map()
	if err != nil {
		return err
	}
	u.SetUnstructuredContent(m)
	return r.scheme.Convert(&u, to, nil)
}
