package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func stubAws(t *testing.T, responses map[string]string) {
	t.Helper()
	original := execAwsCli
	t.Cleanup(func() { execAwsCli = original })
	execAwsCli = func(_ context.Context, args []string) ([]byte, error) {
		key := strings.Join(args[:2], " ")
		if v, ok := responses[key]; ok {
			return []byte(v), nil
		}
		return nil, fmt.Errorf("unexpected aws call: %v", args)
	}
}

func ssmResponse(params map[string]routeMap) string {
	type parameter struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	var response struct {
		Parameters []parameter `json:"Parameters"`
	}
	for name, value := range params {
		raw, _ := json.Marshal(value)
		response.Parameters = append(response.Parameters, parameter{Name: name, Value: string(raw)})
	}
	out, _ := json.Marshal(response)
	return string(out)
}

func TestDiscoverMatchesPrefixesAndReportsUnrouted(t *testing.T) {
	t.Setenv("ARGO_DIFF_ENVS", "dev")
	t.Setenv("ARGO_DIFF_AWS_PREFIX", "/adrise/argo-diff")
	stubAws(t, map[string]string{
		"ssm get-parameters-by-path": ssmResponse(map[string]routeMap{
			"/adrise/argo-diff/dev/routes/deployments/rainbow-eks2": {Prefixes: []string{"back-office/rainbow/clusters/back-office/dev"}},
			"/adrise/argo-diff/dev/routes/deployments/apollo-eks2":  {Prefixes: []string{"back-office/apollo"}},
		}),
	})

	discovery, err := Discover(context.Background(), "deployments", []string{
		"back-office/rainbow/clusters/back-office/dev/apps/myapp/deployment.yaml",
		"docs/README.md",
		"orphaned/manifest.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Targets) != 1 {
		t.Fatalf("expected 1 target, got %+v", discovery.Targets)
	}
	if discovery.Targets[0] != (Target{Env: "dev", Project: "rainbow-eks2"}) {
		t.Fatalf("unexpected target: %+v", discovery.Targets[0])
	}
	// README.md is not manifest-like; orphaned/manifest.yaml is and matched nothing.
	if len(discovery.UnroutedManifests) != 1 || discovery.UnroutedManifests[0] != "orphaned/manifest.yaml" {
		t.Fatalf("unexpected unrouted manifests: %+v", discovery.UnroutedManifests)
	}
}

func TestMatchesRoute(t *testing.T) {
	routes := routeMap{Prefixes: []string{"argocd"}, Globs: []string{"apps/*/config.json"}}
	cases := map[string]bool{
		"argocd/bootstrap/wave-1/keda/appset.yaml": true,
		"argocd":                true,
		"argocd-other/x.yaml":   false,
		"apps/web/config.json":  true,
		"apps/web/other.json":   false,
		"unrelated/values.yaml": false,
	}
	for file, want := range cases {
		if got := matchesRoute(file, routes); got != want {
			t.Errorf("matchesRoute(%q) = %v, want %v", file, got, want)
		}
	}
	if !matchesRoute("anything/at/all.yaml", routeMap{Prefixes: []string{"."}}) {
		t.Error("prefix '.' must match everything")
	}
}

func TestGetCredentials(t *testing.T) {
	t.Setenv("ARGO_DIFF_AWS_PREFIX", "/adrise/argo-diff")
	payload := `{"token":"jwt-value","server":"https://hub.example.com/","ui_base_url":""}`
	envelope, _ := json.Marshal(map[string]string{"SecretString": payload})
	stubAws(t, map[string]string{"secretsmanager get-secret-value": string(envelope)})

	credentials, err := GetCredentials(context.Background(), Target{Env: "dev", Project: "rainbow-eks2"})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ServerAddr != "hub.example.com" {
		t.Errorf("server scheme/slash not stripped: %q", credentials.ServerAddr)
	}
	if credentials.AuthToken != "jwt-value" {
		t.Errorf("unexpected token: %q", credentials.AuthToken)
	}
	if credentials.UiBaseUrl != "https://hub.example.com" {
		t.Errorf("ui base url not defaulted from server: %q", credentials.UiBaseUrl)
	}
}

func TestGetCredentialsRejectsIncompleteSecret(t *testing.T) {
	envelope, _ := json.Marshal(map[string]string{"SecretString": `{"token":"","server":""}`})
	stubAws(t, map[string]string{"secretsmanager get-secret-value": string(envelope)})
	if _, err := GetCredentials(context.Background(), Target{Env: "dev", Project: "x"}); err == nil {
		t.Fatal("expected error for incomplete secret")
	}
}
