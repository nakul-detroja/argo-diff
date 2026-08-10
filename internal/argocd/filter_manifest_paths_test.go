package argocd

import (
	"encoding/json"
	"testing"
)

func TestFilterApplicationsByPath(t *testing.T) {
	var a []Application
	annotationStr := "argocd.argoproj.io/manifest-generate-paths"
	payload, _, err := readFileToByteArray(payloadAppList)
	if err != nil {
		t.Errorf("Failed to read %s: %v", payloadAppList, err)
	}
	var appList ApplicationList
	if err := json.Unmarshal(payload, &appList); err != nil {
		t.Errorf("Error decoding ApplicationList payload: %v", err)
	}
	a = appList.Items
	if err != nil {
		t.Errorf("decodeApplicationListPayload() failed: %s", err)
	}

	// no annotations set, should get the same apps back
	passThru, _ := FilterApplicationsByPath(a, []string{"doesnt", "matter"})
	if len(passThru) != len(a) {
		t.Error("passthrough failed for FilterApplicationByPath()")
	}

	a1 := []Application{a[0]}
	a1[0].SetAnnotations(map[string]string{
		annotationStr: ".",
	})

	relativeNoMatch, _ := FilterApplicationsByPath(a1, []string{"not_apps/manifest.yaml", "something/else.yaml"})
	if len(relativeNoMatch) != 0 {
		t.Error("relativeNoMatch failed")
	}

	relativeMatch, _ := FilterApplicationsByPath(a1, []string{"apps/somepath/manifest.yaml", "something/else.yaml"})
	if len(relativeMatch) != 1 {
		t.Error("relativeMatch failed")
	}

	a1[0].SetAnnotations(map[string]string{
		annotationStr: "/apps",
	})

	absoluteNoMatch, _ := FilterApplicationsByPath(a1, []string{"not_apps/manifest.yaml", "something/else.yaml"})
	if len(absoluteNoMatch) != 0 {
		t.Error("absoluteNoMatch failed")
	}

	absoluteMatch, _ := FilterApplicationsByPath(a1, []string{"apps/somepath/manifest.yaml", "something/else.yaml"})
	if len(absoluteMatch) != 1 {
		t.Error("absoluteMatch failed")
	}

	a1[0].SetAnnotations(map[string]string{
		annotationStr: "/shared/application-*.yaml",
	})

	globNoMatch, _ := FilterApplicationsByPath(a1, []string{"somepath/application-testing.yaml", "something/else.yaml"})
	if len(globNoMatch) != 0 {
		t.Error("globNoMatch failed")
	}

	globMatch, _ := FilterApplicationsByPath(a1, []string{"shared/application-testing_123.yaml", "something/else.yaml"})
	if len(globMatch) != 1 {
		t.Error("globMatch failed")
	}

	a1[0].SetAnnotations(map[string]string{
		annotationStr: ".;/shared/application-*.yaml;/more/apps/",
	})

	mixedNoMatch, _ := FilterApplicationsByPath(a1, []string{"somepath/application-testing.yaml", "something/else.yaml", "more/notapps/manifest.yaml"})
	if len(mixedNoMatch) != 0 {
		t.Error("mixedNoMatch failed")
	}

	mixedMatch1, _ := FilterApplicationsByPath(a1, []string{"shared/application-testing.yaml", "something/else.yaml", "more/notapps/manifest.yaml"})
	if len(mixedMatch1) != 1 {
		t.Error("mixedMatch1 failed")
	}

	mixedMatch2, _ := FilterApplicationsByPath(a1, []string{"somepath/application-testing.yaml", "apps/manifest.yaml", "more/notapps/manifest.yaml"})
	if len(mixedMatch2) != 1 {
		t.Error("mixedMatch2 failed")
	}

	mixedMatch3, _ := FilterApplicationsByPath(a1, []string{"somepath/application-testing.yaml", "something/else/dot.yaml", "more/apps/manifest.yaml"})
	if len(mixedMatch3) != 1 {
		t.Error("mixedMatch3 failed")
	}

	// Test "/" annotation - should match all files (same as no annotation)
	a1[0].SetAnnotations(map[string]string{
		annotationStr: "/",
	})

	rootMatch, _ := FilterApplicationsByPath(a1, []string{"any/path/file.yaml", "another/file.txt"})
	if len(rootMatch) != 1 {
		t.Error("rootMatch failed - '/' annotation should match all files")
	}

	rootMatchEmpty, _ := FilterApplicationsByPath(a1, []string{})
	if len(rootMatchEmpty) != 1 {
		t.Error("rootMatchEmpty failed - '/' annotation should include app even with no changed files")
	}
}

func TestFilterApplicationsByPathRequireAnnotation(t *testing.T) {
	payload, _, err := readFileToByteArray(payloadAppList)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", payloadAppList, err)
	}
	var appList ApplicationList
	if err := json.Unmarshal(payload, &appList); err != nil {
		t.Fatalf("Error decoding ApplicationList payload: %v", err)
	}
	unannotated := []Application{appList.Items[0]}
	unannotated[0].SetAnnotations(map[string]string{})

	// default behavior: an app without the annotation matches any change
	matched, skipped := FilterApplicationsByPath(unannotated, []string{"anywhere/file.yaml"})
	if len(matched) != 1 || len(skipped) != 0 {
		t.Errorf("without ARGO_DIFF_REQUIRE_MANIFEST_PATHS: got %d matched / %d skipped, want 1 / 0", len(matched), len(skipped))
	}

	t.Setenv("ARGO_DIFF_REQUIRE_MANIFEST_PATHS", "true")

	matched, skipped = FilterApplicationsByPath(unannotated, []string{"anywhere/file.yaml"})
	if len(matched) != 0 || len(skipped) != 1 {
		t.Errorf("with ARGO_DIFF_REQUIRE_MANIFEST_PATHS: got %d matched / %d skipped, want 0 / 1", len(matched), len(skipped))
	}
	if len(skipped) == 1 && skipped[0] != unannotated[0].ObjectMeta.Name {
		t.Errorf("skipped app name = %q, want %q", skipped[0], unannotated[0].ObjectMeta.Name)
	}

	// an annotated app is still filtered on its paths, not skipped
	annotated := []Application{appList.Items[0]}
	annotated[0].SetAnnotations(map[string]string{
		"argocd.argoproj.io/manifest-generate-paths": "/apps",
	})
	matched, skipped = FilterApplicationsByPath(annotated, []string{"apps/manifest.yaml"})
	if len(matched) != 1 || len(skipped) != 0 {
		t.Errorf("annotated match: got %d matched / %d skipped, want 1 / 0", len(matched), len(skipped))
	}
	matched, skipped = FilterApplicationsByPath(annotated, []string{"elsewhere/manifest.yaml"})
	if len(matched) != 0 || len(skipped) != 0 {
		t.Errorf("annotated non-match: got %d matched / %d skipped, want 0 / 0 (not reported as skipped)", len(matched), len(skipped))
	}

	// "/" is an explicit opt-in to every change, so it survives strict mode
	rootAnnotated := []Application{appList.Items[0]}
	rootAnnotated[0].SetAnnotations(map[string]string{
		"argocd.argoproj.io/manifest-generate-paths": "/",
	})
	matched, skipped = FilterApplicationsByPath(rootAnnotated, []string{"anywhere/file.yaml"})
	if len(matched) != 1 || len(skipped) != 0 {
		t.Errorf("'/' annotation under strict mode: got %d matched / %d skipped, want 1 / 0", len(matched), len(skipped))
	}
}
