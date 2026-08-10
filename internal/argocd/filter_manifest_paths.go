package argocd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

const manifestGeneratePathsAnnotation = "argocd.argoproj.io/manifest-generate-paths"

// requireManifestGeneratePaths reports whether applications without a
// manifest-generate-paths annotation should be skipped instead of diffed.
//
// The annotation is opt-in narrowing, so by default an application without it
// matches every change to its repo. In a monorepo whose applications mostly
// lack the annotation that means nearly every application is diffed on every
// pull request, which costs one argocd round trip each and commonly exhausts
// ARGO_DIFF_TIMEOUT before the applications that did change are reached.
// Setting this to true makes the annotation the contract: only annotated
// applications are considered, and the rest are reported as skipped so the
// missing coverage is visible rather than silent.
func requireManifestGeneratePaths() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("ARGO_DIFF_REQUIRE_MANIFEST_PATHS"))) == "true"
}

// containsGlob returns true if the pattern contains glob meta characters.
func containsGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// matchChangedFiles checks if any of the changed files match one of the given patterns.
func matchChangedFiles(changedFiles []string, patterns []string) bool {
	for _, file := range changedFiles {
		// changed files shouldn't have absolute paths, but we'll trim / to be safe
		if filepath.IsAbs(file) {
			file = strings.TrimPrefix(file, "/")
		}
		for _, pattern := range patterns {
			log.Trace().Msgf("matchChangedFiles(): matching files %s to pattern %s", file, pattern)
			if containsGlob(pattern) {
				// filepath.Match expects the pattern to match the entire name.
				if ok, err := filepath.Match(pattern, file); err == nil && ok {
					return true
				} else if err != nil {
					log.Warn().Err(err).Msgf("failed to call filepath.Match(%s, %s)", pattern, file)
				}
			} else {
				// For a non-glob pattern, treat it as a directory prefix.
				dirPrefix := pattern
				if !strings.HasSuffix(dirPrefix, string(filepath.Separator)) {
					dirPrefix += string(filepath.Separator)
				}
				// Clean paths to avoid mismatches.
				cleanFile := filepath.Clean(file)
				cleanPrefix := filepath.Clean(dirPrefix)
				// Check if the changed file is under the directory.
				if strings.HasPrefix(cleanFile, cleanPrefix) {
					return true
				}
			}
		}
	}
	return false
}

// FilterApplications returns a list of Application objects whose annotation-based manifest-generate-paths
// or default source path (if the annotation is absent) match one or more of the changed files.
// It iterates through each source returned by the built-in GetSources() method.
//
// The second return value names the applications skipped because they carry no
// manifest-generate-paths annotation while ARGO_DIFF_REQUIRE_MANIFEST_PATHS is
// set; it is always empty otherwise. These are not failures: the caller reports
// them so that an application whose diff was never attempted can't be mistaken
// for one that had no changes.
func FilterApplicationsByPath(apps []Application, changedFiles []string) ([]Application, []string) {
	var matchedApps []Application
	var skippedApps []string
	requireAnnotation := requireManifestGeneratePaths()

	for _, app := range apps {
		annotations := app.GetAnnotations()
		var manifestPaths string
		var ok bool
		if manifestPaths, ok = annotations[manifestGeneratePathsAnnotation]; !ok {
			if requireAnnotation {
				log.Debug().Msgf("Skipping application %s: no %s annotation and ARGO_DIFF_REQUIRE_MANIFEST_PATHS is set", app.ObjectMeta.Name, manifestGeneratePathsAnnotation)
				skippedApps = append(skippedApps, app.ObjectMeta.Name)
				continue
			}
			// if the app does not have this annotation, include it in the results
			matchedApps = append(matchedApps, app)
			continue
		}
		// Treat "/" as "no annotation" - include the app without path filtering.
		// This stays an unconditional include under
		// ARGO_DIFF_REQUIRE_MANIFEST_PATHS: the annotation is present, and "/"
		// deliberately opts the application in to every change.
		if strings.TrimSpace(manifestPaths) == "/" {
			matchedApps = append(matchedApps, app)
			continue
		}

		// Get all sources from the Application.
		sources := app.Spec.GetSources()
		matched := false

		for _, source := range sources {
			var patterns []string

			if manifestPaths != "" {
				// Split the annotation on semicolons and build full patterns.
				parts := strings.Split(manifestPaths, ";")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					var fullPattern string
					if !filepath.IsAbs(p) {
						// If p is not an absolute path, join it with the source's path.
						if p == "." {
							fullPattern = source.Path
						} else if strings.HasPrefix(p, "./") {
							fullPattern = filepath.Join(source.Path, strings.TrimPrefix(p, "./"))
						} else {
							fullPattern = filepath.Join(source.Path, p)
						}
					} else {
						fullPattern = strings.TrimPrefix(p, "/")
					}
					patterns = append(patterns, fullPattern)
				}
			} else {
				// Empty annotation, include it in the results
				matched = true
			}

			if matchChangedFiles(changedFiles, patterns) {
				matched = true
				break // No need to check further sources for this Application.
			}
		}

		if matched {
			matchedApps = append(matchedApps, app)
		}
	}

	return matchedApps, skippedApps
}
