# internal/routes/

Routed-mode discovery: which AppProjects does this pull request touch, and what credentials does
each need? Consumed only by `process_event.ProcessCodeChangeRouted()`.

## Data sources (produced by the platform's convergence tooling, read-only here)

| Where | Layout | Contents |
| ----- | ------ | -------- |
| SSM Parameter Store | `<prefix>/<env>/routes/<repo>/<appproject>` | `{"prefixes": ["path", ...], "globs": ["pattern", ...]}` |
| Secrets Manager | `<prefix>/<env>/projects/<appproject>` | `{"token", "server", "ui_base_url"}` — a read-only project-role JWT |

`<prefix>` is `ARGO_DIFF_AWS_PREFIX` (default `/adrise/argo-diff`); the environments consulted are
`ARGO_DIFF_ENVS` (default `dev,staging,prod`).

## How it works

- **`Discover(ctx, repoName, changedFiles)`** fetches every environment's route parameters for the
  repository (`get-parameters-by-path --recursive`; the aws CLI auto-paginates) and matches changed
  files against each parameter's prefixes (a prefix of `.` or empty matches everything) and globs
  (`path.Match`). The AppProject name is the last segment of the parameter name. Returns sorted,
  deduplicated `(env, project)` targets **plus `UnroutedManifests`**: changed manifest-like files
  (`.yaml/.yml/.tpl/.gotmpl/.json`, `Chart.lock`) that matched no route in any environment. Those
  are surfaced as a PR warning — route maps are generated from live specs and can only go stale
  between convergence runs, and staleness must be visible, never silent.
- **`GetCredentials(ctx, target)`** reads and validates the target's secret. The `server` value is
  normalized to a scheme-less host for `argocd --server`; `ui_base_url` falls back to the server
  with `https://` prepended.

## Gotchas

- All AWS access shells out to the **aws CLI** with ambient credentials (`execAwsCli` is the
  package-level mocking seam, mirroring `execArgoCdCli`). argo-diff never holds AWS keys.
- `execAwsCli` never logs command output — `get-secret-value` responses contain tokens.
- A route-map miss is the one remaining way a change can go undiffed at the *project* level
  (application-level matching fails open, see `internal/argocd/spec_paths.go`). That is exactly
  what `UnroutedManifests` exists to expose.

## Tests

`routes_test.go` stubs `execAwsCli` keyed on the first two CLI args and covers prefix/glob
matching, unrouted-file reporting, credential normalization, and incomplete-secret rejection.
