package process_event

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/vince-riv/argo-diff/internal/argocd"
	"github.com/vince-riv/argo-diff/internal/github"
	"github.com/vince-riv/argo-diff/internal/routes"
	"github.com/vince-riv/argo-diff/internal/webhook"
)

// RoutedEnabled reports whether route-map orchestration is enabled via
// ARGO_DIFF_ROUTED. In routed mode argo-diff resolves the AppProjects a pull
// request touches from SSM route maps, fetches each project's read-only token
// from Secrets Manager, and diffs each affected project with its own scoped
// credentials - no global ARGOCD_SERVER_ADDR / ARGOCD_AUTH_TOKEN required.
func RoutedEnabled() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("ARGO_DIFF_ROUTED"))) == "true"
}

// targetOutcome collects the diff results for one (env, AppProject) target.
type targetOutcome struct {
	target    routes.Target
	uiBaseUrl string
	apps      []argocd.ApplicationResourcesWithChanges
	notDiffed []string
	err       error
}

func (o targetOutcome) changedApps() []string {
	var names []string
	for _, a := range o.apps {
		if a.WarnStr == "" && len(a.ChangedResources) > 0 {
			names = append(names, a.ArgoApp.ObjectMeta.Name)
		}
	}
	return names
}

// summaryTable renders one row per (AppProject, env) so a reviewer can see at
// a glance which projects the PR touches and which applications changed.
func summaryTable(outcomes []targetOutcome) string {
	md := "\n| AppProject | Env | Changed apps | Applications |\n"
	md += "| --- | --- | ---: | --- |\n"
	for _, outcome := range outcomes {
		names := outcome.changedApps()
		detail := cappedNames(names)
		if outcome.err != nil {
			detail = "ERROR: " + outcome.err.Error()
		} else if len(names) == 0 {
			detail = "-"
		}
		md += fmt.Sprintf("| %s | %s | %d | %s |\n", outcome.target.Project, outcome.target.Env, len(names), detail)
	}
	return md
}

// unroutedMarkdown warns about changed manifest files that matched no route:
// a stale route map must be visible on the PR, never silent.
func unroutedMarkdown(files []string) string {
	md := "\n> [!WARNING]\n"
	md += fmt.Sprintf("> %d changed manifest file(s) matched no Argo CD route, so applications rendering them (if any) were **not** diffed: %s\n", len(files), cappedNames(files))
	md += ">\n> If these files feed an Argo CD application, refresh the route maps (token/route convergence script) and re-run this check.\n"
	return md
}

// ProcessCodeChangeRouted handles one pull-request event in routed mode:
// discover affected AppProjects from route maps, diff each with its own
// project-scoped token, and post a single summary comment. Designed to run in
// a goroutine, mirroring ProcessCodeChange.
func ProcessCodeChangeRouted(eventInfo webhook.EventInfo, devMode bool, wg *sync.WaitGroup, callerErr *error) {
	defer wg.Done()
	timeout := processTimeout()
	log.Debug().Msgf("Processing event (routed mode) with a %s timeout", timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if eventInfo.PrNum <= 0 {
		log.Error().Msg("ProcessCodeChangeRouted called with non-PR event - this is not supported")
		*callerErr = fmt.Errorf("only pull request events are supported")
		return
	}

	if eventInfo.Refresh {
		pull, err := github.GetPullRequest(ctx, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
		if err != nil {
			log.Error().Err(err).Msgf("github.GetPullRequest(%s, %s, %d) failed", eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
			*callerErr = err
			return
		}
		base := pull.GetBase()
		head := pull.GetHead()
		if base == nil || head == nil {
			log.Error().Msgf("Empty branch information when refreshing %s/%s#%d", eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
			*callerErr = fmt.Errorf("empty branch information when refreshing %s/%s#%d", eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
			return
		}
		eventInfo.Sha = *head.SHA
		eventInfo.ChangeRef = *head.Ref
		eventInfo.BaseRef = *base.Ref
	}

	changedFiles, err := github.ListPullRequestFiles(ctx, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
	if err != nil {
		// Changed files drive both route discovery and application matching;
		// routed mode cannot proceed without them.
		log.Error().Err(err).Msgf("Failed to list pull request files for %s/%s#%d", eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum)
		*callerErr = err
		return
	}
	eventInfo.ChangedFiles = changedFiles

	err = github.Status(ctx, github.StatusPending, "", eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.Sha, devMode)
	if err != nil {
		log.Warn().Err(err).Msgf("Failed to set commit status %s for %s/%s@%s", github.StatusPending, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.Sha)
	}

	reserve := reportReserve(timeout)

	discovery, err := routes.Discover(ctx, eventInfo.RepoName, eventInfo.ChangedFiles)
	if err != nil {
		log.Error().Err(err).Msg("routes.Discover() failed")
		reportCtx, reportCancel := context.WithTimeout(context.Background(), reserve)
		defer reportCancel()
		_ = github.Status(reportCtx, github.StatusError, err.Error(), eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.Sha, devMode)
		*callerErr = err
		return
	}
	log.Info().Msgf("Route discovery matched %d target(s); %d unrouted manifest file(s)", len(discovery.Targets), len(discovery.UnroutedManifests))

	// Diff every target within a shared budget, holding back a reserve so
	// reporting always happens (mirrors ProcessCodeChange).
	diffCtx, diffCancel := context.WithTimeout(ctx, timeout-reserve)
	defer diffCancel()

	var outcomes []targetOutcome
	for _, target := range discovery.Targets {
		outcome := targetOutcome{target: target}
		credentials, err := routes.GetCredentials(diffCtx, target)
		if err != nil {
			outcome.err = err
			outcomes = append(outcomes, outcome)
			continue
		}
		outcome.uiBaseUrl = credentials.UiBaseUrl
		argocd.SetTarget(credentials.ServerAddr, credentials.AuthToken)
		apps, notDiffed, _, err := argocd.GetApplicationChanges(diffCtx, eventInfo)
		argocd.ClearTarget()
		outcome.apps = apps
		outcome.notDiffed = notDiffed
		outcome.err = err
		outcomes = append(outcomes, outcome)
	}

	reportCtx, reportCancel := context.WithTimeout(context.Background(), reserve)
	defer reportCancel()

	changeCount := 0
	errorCount := 0
	firstError := ""
	var notDiffedAll []string
	cMarkdown := github.CommentMarkdown{}
	for _, outcome := range outcomes {
		if outcome.err != nil {
			errorCount++
			if firstError == "" {
				firstError = fmt.Sprintf("%s (%s): %s", outcome.target.Project, outcome.target.Env, outcome.err.Error())
			}
			continue
		}
		for _, name := range outcome.notDiffed {
			notDiffedAll = append(notDiffedAll, fmt.Sprintf("%s [%s/%s]", name, outcome.target.Project, outcome.target.Env))
		}
		for _, a := range outcome.apps {
			appName := a.ArgoApp.ObjectMeta.Name
			appSyncStatus := a.ArgoApp.Status.Sync.Status
			appHealthStatus := a.ArgoApp.Status.Health.Status
			appHealthMsg := a.ArgoApp.Status.Health.Message
			if a.WarnStr != "" {
				errorCount++
				appMarkdown := cMarkdown.AppMarkdown(appName, "Error: "+a.WarnStr, appSyncStatus, appHealthStatus, appHealthMsg)
				appMarkdown.UiBaseUrl = outcome.uiBaseUrl
				if firstError == "" {
					firstError = a.WarnStr
				}
			} else if len(a.ChangedResources) > 0 {
				changeCount++
				appMarkdown := cMarkdown.AppMarkdown(appName, "", appSyncStatus, appHealthStatus, appHealthMsg)
				appMarkdown.UiBaseUrl = outcome.uiBaseUrl
				for _, resource := range a.ChangedResources {
					appMarkdown.AddResourceDiff(resource.Group, resource.Kind, resource.Name, resource.Namespace, resource.DiffStr)
				}
			}
		}
	}

	changeCountStr := fmt.Sprintf("%d application(s) with changes across %d AppProject route(s)", changeCount, len(outcomes))
	if len(notDiffedAll) > 0 {
		changeCountStr += fmt.Sprintf(" [%d apps not diffed]", len(notDiffedAll))
	}

	newStatus := github.StatusSuccess
	statusDescription := changeCountStr + " - no errors"
	if errorCount > 0 {
		newStatus = github.StatusFailure
		statusDescription = fmt.Sprintf("%s; %d error(s); first error: %s", changeCountStr, errorCount, firstError)
		*callerErr = fmt.Errorf("%d target(s)/application(s) failed to diff; first error: %s", errorCount, firstError)
	}
	if len(notDiffedAll) > 0 {
		newStatus = github.StatusFailure
		statusDescription = fmt.Sprintf("%d app(s) not diffed (timed out); %s", len(notDiffedAll), statusDescription)
		if *callerErr == nil {
			*callerErr = fmt.Errorf("timed out (ARGO_DIFF_TIMEOUT is %s); %d application(s) were not diffed", timeout, len(notDiffedAll))
		}
	}
	_ = github.Status(reportCtx, newStatus, statusDescription, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.Sha, devMode)

	tStr := time.Now().Format("3:04PM MST, 2 Jan 2006")
	preamble := changeCountStr + " compared to live state\n"
	preamble += "\n" + tStr + "\n"
	if len(outcomes) > 0 {
		preamble += summaryTable(outcomes)
	}
	if len(discovery.UnroutedManifests) > 0 {
		preamble += unroutedMarkdown(discovery.UnroutedManifests)
	}
	if len(notDiffedAll) > 0 {
		preamble += timeoutMarkdown(timeout, notDiffedAll)
	}
	cMarkdown.Preamble = preamble

	if len(outcomes) == 0 && len(discovery.UnroutedManifests) == 0 {
		// Nothing routed and nothing suspicious: clear any previous comments.
		_, _ = github.Comment(reportCtx, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum, eventInfo.Sha, []string{})
		return
	}
	_, _ = github.Comment(reportCtx, eventInfo.RepoOwner, eventInfo.RepoName, eventInfo.PrNum, eventInfo.Sha, cMarkdown.String())
}
