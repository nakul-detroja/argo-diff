// Package routes resolves which AppProjects a pull request touches and the
// per-project credentials needed to diff them.
//
// Route maps and token secrets are produced by the platform's convergence
// tooling (one SSM parameter per AppProject with the repo path prefixes it
// consumes, one Secrets Manager secret per AppProject with a read-only
// project-role token). This package only reads them, via the aws CLI with the
// runner's ambient credentials - argo-diff never holds AWS keys of its own.
//
// Layout (prefix configurable via ARGO_DIFF_AWS_PREFIX, default
// /adrise/argo-diff):
//
//	<prefix>/<env>/routes/<repo>/<appproject>  SSM: {"prefixes": [...], "globs": [...]}
//	<prefix>/<env>/projects/<appproject>       Secrets Manager: {"token", "server", "ui_base_url"}
package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

const defaultAwsPrefix = "/adrise/argo-diff"
const defaultEnvironments = "dev,staging,prod"

// Target identifies one AppProject on one hub environment.
type Target struct {
	Env     string
	Project string
}

// Credentials hold what one target needs to run argocd CLI diffs.
type Credentials struct {
	ServerAddr string // host[:port] without scheme, for --server
	AuthToken  string
	UiBaseUrl  string
}

// Discovery is the result of matching changed files against route maps.
type Discovery struct {
	Targets []Target
	// UnroutedManifests are changed manifest-like files that matched no
	// route in any environment. They are surfaced on the PR comment
	// because a stale route map must be visible, never silent.
	UnroutedManifests []string
}

func awsPrefix() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("ARGO_DIFF_AWS_PREFIX")), "/"); v != "" {
		return v
	}
	return defaultAwsPrefix
}

func environments() []string {
	envList := os.Getenv("ARGO_DIFF_ENVS")
	if strings.TrimSpace(envList) == "" {
		envList = defaultEnvironments
	}
	var out []string
	for _, env := range strings.Split(envList, ",") {
		if env = strings.TrimSpace(env); env != "" {
			out = append(out, env)
		}
	}
	return out
}

// execAwsCli wraps the aws CLI; a variable so tests can stub it. Command
// output is never logged: get-secret-value responses contain tokens.
var execAwsCli = func(ctx context.Context, args []string) ([]byte, error) {
	log.Info().Msgf("Executing aws %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "aws", append(args, "--output", "json")...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("aws %s: %s: %s", strings.Join(args, " "), err.Error(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

type ssmParameter struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type routeMap struct {
	Prefixes []string `json:"prefixes"`
	Globs    []string `json:"globs"`
}

func matchesRoute(file string, routes routeMap) bool {
	for _, prefix := range routes.Prefixes {
		prefix = strings.Trim(strings.TrimSpace(prefix), "/")
		if prefix == "" || prefix == "." {
			return true
		}
		if file == prefix || strings.HasPrefix(file, prefix+"/") {
			return true
		}
	}
	for _, glob := range routes.Globs {
		if ok, err := path.Match(glob, file); err == nil && ok {
			return true
		}
	}
	return false
}

// manifestLike reports whether a changed file plausibly feeds rendered
// manifests. Used only to decide which unrouted files are worth warning
// about; matching itself considers every changed file.
func manifestLike(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".yaml", ".yml", ".tpl", ".gotmpl", ".json":
		return true
	}
	return path.Base(file) == "Chart.lock"
}

// Discover matches the pull request's changed files against every
// environment's route maps for the repository and returns the affected
// (env, AppProject) targets, plus any manifest-like changed files that
// matched no route at all.
func Discover(ctx context.Context, repoName string, changedFiles []string) (Discovery, error) {
	var discovery Discovery
	routed := map[string]bool{}
	seen := map[Target]bool{}
	for _, env := range environments() {
		parameterPath := fmt.Sprintf("%s/%s/routes/%s", awsPrefix(), env, repoName)
		out, err := execAwsCli(ctx, []string{
			"ssm", "get-parameters-by-path", "--path", parameterPath, "--recursive",
		})
		if err != nil {
			return discovery, fmt.Errorf("route lookup under %s failed: %w", parameterPath, err)
		}
		var response struct {
			Parameters []ssmParameter `json:"Parameters"`
		}
		if err := json.Unmarshal(out, &response); err != nil {
			return discovery, fmt.Errorf("decoding route parameters under %s: %w", parameterPath, err)
		}
		for _, parameter := range response.Parameters {
			var routes routeMap
			if err := json.Unmarshal([]byte(parameter.Value), &routes); err != nil {
				log.Warn().Err(err).Msgf("Skipping unparseable route map %s", parameter.Name)
				continue
			}
			nameParts := strings.Split(parameter.Name, "/")
			project := nameParts[len(nameParts)-1]
			for _, file := range changedFiles {
				if matchesRoute(file, routes) {
					routed[file] = true
					target := Target{Env: env, Project: project}
					if !seen[target] {
						seen[target] = true
						discovery.Targets = append(discovery.Targets, target)
					}
				}
			}
		}
	}
	sort.Slice(discovery.Targets, func(i, j int) bool {
		if discovery.Targets[i].Env != discovery.Targets[j].Env {
			return discovery.Targets[i].Env < discovery.Targets[j].Env
		}
		return discovery.Targets[i].Project < discovery.Targets[j].Project
	})
	for _, file := range changedFiles {
		if manifestLike(file) && !routed[file] {
			discovery.UnroutedManifests = append(discovery.UnroutedManifests, file)
		}
	}
	return discovery, nil
}

// GetCredentials fetches and validates the token secret for a target.
func GetCredentials(ctx context.Context, target Target) (Credentials, error) {
	var credentials Credentials
	secretId := fmt.Sprintf("%s/%s/projects/%s", awsPrefix(), target.Env, target.Project)
	out, err := execAwsCli(ctx, []string{
		"secretsmanager", "get-secret-value", "--secret-id", secretId,
	})
	if err != nil {
		return credentials, fmt.Errorf("reading secret %s: %w", secretId, err)
	}
	var response struct {
		SecretString string `json:"SecretString"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return credentials, fmt.Errorf("decoding secret envelope %s: %w", secretId, err)
	}
	var payload struct {
		Token     string `json:"token"`
		Server    string `json:"server"`
		UiBaseUrl string `json:"ui_base_url"`
	}
	if err := json.Unmarshal([]byte(response.SecretString), &payload); err != nil {
		return credentials, fmt.Errorf("decoding secret payload %s: %w", secretId, err)
	}
	server := strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(payload.Server, "https://"), "http://"), "/")
	if payload.Token == "" || server == "" {
		return credentials, fmt.Errorf("secret %s is missing token or server", secretId)
	}
	uiBaseUrl := strings.TrimRight(payload.UiBaseUrl, "/")
	if uiBaseUrl == "" {
		uiBaseUrl = server
	}
	if !strings.HasPrefix(uiBaseUrl, "http://") && !strings.HasPrefix(uiBaseUrl, "https://") {
		uiBaseUrl = "https://" + uiBaseUrl
	}
	credentials = Credentials{ServerAddr: server, AuthToken: payload.Token, UiBaseUrl: uiBaseUrl}
	return credentials, nil
}
