package github

import (
	"strings"
	"testing"
)

func TestAddResourceDiffIsCollapsedByDefault(t *testing.T) {
	app := ArgoAppMarkdown{}

	app.AddResourceDiff("apps", "Deployment", "api", "default", "+ replicas: 2\n")

	if len(app.Resources) != 1 {
		t.Fatalf("expected one resource diff, got %d", len(app.Resources))
	}
	if !strings.Contains(app.Resources[0], "<details>\n") {
		t.Errorf("resource diff is not collapsed: %q", app.Resources[0])
	}
	if strings.Contains(app.Resources[0], "<details open>") {
		t.Errorf("resource diff is expanded by default: %q", app.Resources[0])
	}
}
