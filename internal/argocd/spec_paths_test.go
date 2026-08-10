package argocd

import (
	"slices"
	"testing"
)

func kustomizeApp(name, path string) Application {
	app := Application{}
	app.ObjectMeta.Name = name
	app.Spec.Source = &ApplicationSource{
		RepoURL: "https://github.com/acme/deployments.git",
		Path:    path,
	}
	return app
}

func multiSourceHelmApp(name string, valueFiles ...string) Application {
	app := Application{}
	app.ObjectMeta.Name = name
	app.Spec.Sources = []ApplicationSource{
		{
			RepoURL: "https://github.com/acme/deployments.git",
			Ref:     "values",
		},
		{
			RepoURL: "https://charts.example.com",
			Chart:   "widget",
			Helm:    &ApplicationSourceHelm{ValueFiles: valueFiles},
		},
	}
	return app
}

func TestSpecDerivedPatternsKustomize(t *testing.T) {
	app := kustomizeApp("web", "apps/web/overlays/dev")
	patterns, matchAll := specDerivedPatterns(&app)
	if matchAll {
		t.Fatal("kustomize app with a source path must not match all")
	}
	if !slices.Contains(patterns, "apps/web/overlays/dev") {
		t.Fatalf("expected source path pattern, got %v", patterns)
	}
}

func TestSpecDerivedPatternsRepoRoot(t *testing.T) {
	app := kustomizeApp("root", ".")
	if _, matchAll := specDerivedPatterns(&app); !matchAll {
		t.Fatal("source path '.' must match every changed file")
	}
	app = kustomizeApp("empty", "")
	if _, matchAll := specDerivedPatterns(&app); !matchAll {
		t.Fatal("empty source path must match every changed file")
	}
}

func TestSpecDerivedPatternsHelmValueFiles(t *testing.T) {
	app := kustomizeApp("api", "apps/api")
	app.Spec.Source.Helm = &ApplicationSourceHelm{
		ValueFiles:     []string{"values.yaml", "../shared/values.yaml"},
		FileParameters: []HelmFileParameter{{Name: "cfg", Path: "config/extra.yaml"}},
	}
	patterns, matchAll := specDerivedPatterns(&app)
	if matchAll {
		t.Fatal("unexpected matchAll")
	}
	for _, want := range []string{"apps/api", "apps/api/values.yaml", "apps/shared/values.yaml", "apps/api/config/extra.yaml"} {
		if !slices.Contains(patterns, want) {
			t.Fatalf("missing pattern %q in %v", want, patterns)
		}
	}
}

func TestSpecDerivedPatternsValuesRef(t *testing.T) {
	app := multiSourceHelmApp("platform-widget", "$values/environments/dev/platform/widget/values.yaml")
	patterns, matchAll := specDerivedPatterns(&app)
	if matchAll {
		t.Fatal("unexpected matchAll")
	}
	if !slices.Contains(patterns, "environments/dev/platform/widget/values.yaml") {
		t.Fatalf("expected $values-resolved pattern, got %v", patterns)
	}
}

func TestFilterApplicationsBySpecPathsMatching(t *testing.T) {
	apps := []Application{
		kustomizeApp("web", "apps/web"),
		kustomizeApp("api", "apps/api"),
		multiSourceHelmApp("platform-widget", "$values/environments/dev/platform/widget/values.yaml"),
	}
	matched := FilterApplicationsBySpecPaths(apps, []string{"apps/web/deployment.yaml"})
	if len(matched) != 1 || matched[0].ObjectMeta.Name != "web" {
		t.Fatalf("expected only web to match, got %+v", names(matched))
	}
	matched = FilterApplicationsBySpecPaths(apps, []string{"environments/dev/platform/widget/values.yaml"})
	if len(matched) != 1 || matched[0].ObjectMeta.Name != "platform-widget" {
		t.Fatalf("expected only platform-widget to match, got %+v", names(matched))
	}
}

func TestFilterApplicationsBySpecPathsFailsOpen(t *testing.T) {
	// A chart-only application derives no git patterns: it must be included
	// rather than silently skipped.
	chartOnly := Application{}
	chartOnly.ObjectMeta.Name = "chart-only"
	chartOnly.Spec.Source = &ApplicationSource{
		RepoURL: "https://charts.example.com",
		Chart:   "widget",
	}
	matched := FilterApplicationsBySpecPaths([]Application{chartOnly}, []string{"README.md"})
	if len(matched) != 1 {
		t.Fatal("application with no derivable patterns must be included (fail open)")
	}
}

func names(apps []Application) []string {
	var out []string
	for _, app := range apps {
		out = append(out, app.ObjectMeta.Name)
	}
	return out
}
