package argocd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// specPathsEnabled reports whether application matching derives path patterns
// from each application's own spec (source path, Helm value files) instead of
// the manifest-generate-paths annotation.
//
// The annotation is hand-maintained metadata that can drift from what an
// application actually renders, and drift fails silent-closed: an application
// whose annotation misses a path is skipped without a trace. Spec-derived
// matching computes the paths from the same fields ArgoCD renders from, so it
// cannot drift, and every ambiguous case fails open (the application is
// diffed). When enabled, applications carrying the annotation are still
// matched by their spec - the annotation is ignored entirely.
func specPathsEnabled() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("ARGO_DIFF_SPEC_PATHS"))) == "true"
}

// specDerivedPatterns computes the git paths that contribute to an
// application's rendered manifests, straight from its spec:
//
//   - source.path for git sources (kustomize, helm, or plain YAML)
//   - helm.valueFiles and helm.fileParameters, joined to the source path
//   - "$ref/..." value files, resolved against the source whose ref matches
//     (the remainder is relative to that repository's root)
//
// matchAll is true when any git source renders from the repository root, in
// which case every changed file potentially affects the application.
//
// Chart (registry) sources contribute no git paths of their own; only their
// "$ref" value files count, via the git source that carries them.
func specDerivedPatterns(app *Application) (patterns []string, matchAll bool) {
	sources := app.Spec.GetSources()
	for _, src := range sources {
		if src.Chart == "" && src.RepoURL != "" {
			if src.Path == "" && src.Ref != "" {
				// A bare $values anchor renders nothing on its own; value
				// files referencing it are collected below.
			} else {
				p := filepath.Clean(strings.TrimPrefix(src.Path, "/"))
				if p == "." || p == "" {
					return nil, true
				}
				patterns = append(patterns, p)
			}
		}
		if src.Helm == nil {
			continue
		}
		valueFiles := append([]string{}, src.Helm.ValueFiles...)
		for _, fileParameter := range src.Helm.FileParameters {
			if fileParameter.Path != "" {
				valueFiles = append(valueFiles, fileParameter.Path)
			}
		}
		for _, valueFile := range valueFiles {
			valueFile = strings.TrimSpace(valueFile)
			if valueFile == "" {
				continue
			}
			if strings.HasPrefix(valueFile, "$") {
				refName, rel, _ := strings.Cut(strings.TrimPrefix(valueFile, "$"), "/")
				for _, ref := range sources {
					if ref.Ref == refName && ref.Chart == "" && ref.RepoURL != "" {
						if rel == "" {
							return nil, true
						}
						patterns = append(patterns, filepath.Clean(rel))
						break
					}
				}
				continue
			}
			if src.Chart != "" {
				// Value files inside a registry-pulled chart are not git paths.
				continue
			}
			if filepath.IsAbs(valueFile) {
				patterns = append(patterns, filepath.Clean(strings.TrimPrefix(valueFile, "/")))
			} else {
				patterns = append(patterns, filepath.Clean(filepath.Join(src.Path, valueFile)))
			}
		}
	}
	return patterns, false
}

// FilterApplicationsBySpecPaths returns the applications whose spec-derived
// paths match one or more changed files.
//
// It fails open by design: an application that renders from the repository
// root, or whose spec yields no usable patterns, is included rather than
// skipped. Imperfect derivation can only cost an extra diff - never a
// silently missing one.
func FilterApplicationsBySpecPaths(apps []Application, changedFiles []string) []Application {
	var matched []Application
	for i := range apps {
		app := &apps[i]
		patterns, matchAll := specDerivedPatterns(app)
		switch {
		case matchAll:
			log.Debug().Msgf("FilterApplicationsBySpecPaths: %s renders from the repo root - including", app.ObjectMeta.Name)
			matched = append(matched, *app)
		case len(patterns) == 0:
			log.Debug().Msgf("FilterApplicationsBySpecPaths: no patterns derived for %s - including (fail open)", app.ObjectMeta.Name)
			matched = append(matched, *app)
		case matchChangedFiles(changedFiles, patterns):
			matched = append(matched, *app)
		default:
			log.Debug().Msgf("FilterApplicationsBySpecPaths: no changed file matches %s (patterns: %s)", app.ObjectMeta.Name, strings.Join(patterns, ", "))
		}
	}
	return matched
}
