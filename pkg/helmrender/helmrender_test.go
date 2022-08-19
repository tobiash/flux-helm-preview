package helmrender_test

import (
	"fmt"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
)

func TestHelmRender(t *testing.T) {
	cpo := &action.ChartPathOptions{
		RepoURL: "https://charts.bitnami.com/bitnami",
		Version: "9.0.2",
	}

	settings := cli.New()
	settings.RepositoryCache = t.TempDir()

	entry := &repo.Entry{
		Name: "foo",
		URL: cpo.RepoURL,
	}

	r, err := repo.NewChartRepository(entry, getter.All(settings))

	if err != nil {
		t.Fatal(err)
	}
	storage := &repo.File{}

	r.CachePath = settings.RepositoryCache
	_, err = r.DownloadIndexFile()
	if err != nil {
		t.Fatal(err)
	}

	storage.Update(entry)
	err = storage.WriteFile(settings.RepositoryConfig, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	path, err := cpo.LocateChart("contour", settings)
	if err != nil {
		t.Fatal(err)
	}

	_, err = loader.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	

	fmt.Println(path)
}