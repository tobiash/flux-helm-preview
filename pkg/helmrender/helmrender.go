package helmrender

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/runtime/transform"
	source "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/tobiash/flux-helm-preview/pkg/render"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/strvals"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/kustomize/api/hasher"
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
	settings     *cli.EnvSettings
	httpClient   *retryablehttp.Client
	scheme       *runtime.Scheme
	releases     []v2.HelmRelease
	repositories []source.HelmRepository
	logger       logr.Logger
	storage      repo.File
}

func ParseHelmRepo(r *render.Render, settings *cli.EnvSettings, log logr.Logger) (*HelmRepo, error) {
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
	repo.settings = settings
	repo.httpClient = retryablehttp.NewClient()

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
	res := resmap.New()
	for _, h := range r.releases {
		rendered, err := r.RenderChart(h)
		if err != nil {
			return nil, err
		}
		if err = res.AppendAll(rendered); err != nil {
			return nil, err
		}
	}
	return res, nil
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

func (r *HelmRepo) RenderChart(hr v2.HelmRelease) (resmap.ResMap, error) {
	values, err := r.composeValues(hr)
	if err != nil {
		return nil, fmt.Errorf("error composing values: %w", err)
	}

	hc := r.buildHelmChartFromTemplate(&hr)
	if err != nil {
		return nil, fmt.Errorf("error loading chart: %w", err)
	}

	cfg := new(action.Configuration)
	cfg.Init(r.settings.RESTClientGetter(), hr.GetReleaseNamespace(), os.Getenv("HELM_DRIVER"), func(format string, args ...interface{}) {
		r.logger.Info(fmt.Sprintf(format, args...))
	})

	install := action.NewInstall(cfg)
	install.DryRun = true
	install.ClientOnly = true
	install.CreateNamespace = hr.Spec.GetInstall().CreateNamespace
	install.ReleaseName = hr.GetReleaseName()
	install.SkipCRDs = hr.Spec.GetInstall().SkipCRDs
	install.Replace = hr.Spec.GetInstall().Replace
	install.DisableHooks = hr.Spec.GetInstall().DisableHooks
	install.APIVersions = []string{}
	install.IncludeCRDs = true

	chart, err := r.loadHelmChart(hc, install)

	if err != nil {
		return nil, err
	}

	out := new(bytes.Buffer)
	rel, err := install.Run(chart, values)

	if rel != nil {
		var manifests bytes.Buffer
		fmt.Fprintln(&manifests, strings.TrimSpace(rel.Manifest))
		if !install.DisableHooks {
			for _, m := range rel.Hooks {
				fmt.Fprintf(&manifests, "---\n# Source: %s\n%s\n", m.Path, m.Manifest)
			}
		}
		fmt.Fprintf(out, "%s", manifests.String())
	}
	return resmap.NewFactory(resource.NewFactory(&hasher.Hasher{})).NewResMapFromBytes(out.Bytes())
}

func (r *HelmRepo) buildHelmChartFromTemplate(hr *v2.HelmRelease) *source.HelmChart {
	template := hr.Spec.Chart
	return &source.HelmChart{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hr.GetHelmChartName(),
			Namespace: hr.Spec.Chart.GetNamespace(hr.Namespace),
		},
		Spec: source.HelmChartSpec{
			Chart:   template.Spec.Chart,
			Version: template.Spec.Version,
			SourceRef: source.LocalHelmChartSourceReference{
				Name: template.Spec.SourceRef.Name,
				Kind: template.Spec.SourceRef.Kind,
			},
			Interval:          template.GetInterval(hr.Spec.Interval),
			ReconcileStrategy: template.Spec.ReconcileStrategy,
			ValuesFiles:       template.Spec.ValuesFiles,
			ValuesFile:        template.Spec.ValuesFile,
		},
	}
}

func (r *HelmRepo) updateRepo(hr *source.HelmRepository) error {
	entry := repo.Entry{
		Name: fmt.Sprintf("%s-%s", hr.GetNamespace(), hr.GetName()),
		URL:  hr.Spec.URL,
	}
	chartRepo, err := repo.NewChartRepository(&entry, getter.All(r.settings))
	if err != nil {
		return err
	}
	chartRepo.CachePath = r.settings.RepositoryCache
	_, err = chartRepo.DownloadIndexFile()
	if err != nil {
		return err
	}
	if r.storage.Has(entry.Name) {
		return nil
	}
	r.storage.Update(&entry)
	err = r.storage.WriteFile(r.settings.RepositoryConfig, 0o644)
	if err != nil {
	 	return err
	}
	return nil
}

func (r *HelmRepo) loadHelmChart(source *source.HelmChart, client *action.Install) (*chart.Chart, error) {
	switch source.Spec.SourceRef.Kind {
	case "HelmRepository":
		for _, hr := range r.repositories {
			if hr.GetNamespace() == source.GetNamespace() && hr.GetName() == source.Spec.SourceRef.Name {
				if err := r.updateRepo(&hr); err != nil {
					return nil, err
				}
				client.ChartPathOptions.RepoURL = hr.Spec.URL
				r.settings.Debug = true
				cp, err := client.ChartPathOptions.LocateChart(source.Spec.Chart, r.settings)
				if err != nil {
					return nil, fmt.Errorf("error locating chart: %w", err)
				}
				r.logger.Info("Loaded chart from repo", "chart", source.Spec.Chart, "repo", hr.Spec.URL, "path", cp)
				return loader.Load(cp)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported source kind '%s'", source.Spec.SourceRef.Kind)
	}
	return nil, fmt.Errorf("unable to find source '%s'", source.Spec.SourceRef.Name)
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
