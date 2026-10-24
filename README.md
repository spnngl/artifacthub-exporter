# Artifact Hub Exporter for Prometheus [![ci](https://github.com/spnngl/artifacthub-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/spnngl/artifacthub-exporter/actions/workflows/ci.yml)

Prometheus exporter for [Artifact Hub](https://artifacthub.io/) metrics, written in Go: stars, versions, views, vulnerabilities and tracking status of the packages and repositories you publish or depend on.

The exporter polls the [Artifact Hub API](https://artifacthub.io/docs/api/) in the background and serves the last result on every scrape, so scraping never calls Artifact Hub.

### Installation

Each [release](https://github.com/spnngl/artifacthub-exporter/releases) has a deb and an rpm for amd64 and arm64, with the commands to verify them in its notes. Install one with:

    $ sudo apt install ./artifacthub-exporter_<version>_amd64.deb
    $ sudo dnf install ./artifacthub-exporter-<version>-1.x86_64.rpm

Or build it:

    $ go install github.com/spnngl/artifacthub-exporter/cmd/artifacthub-exporter@latest

### Configuration

Each variable has a flag too (`artifacthub-exporter --help`). Set at least one target. Lists are comma separated.

| ENV Variable | Description |
|--------------|-------------|
| ARTIFACTHUB_ORGS | Organizations to collect: their packages and repositories |
| ARTIFACTHUB_USERS | Users (by alias) to collect: their packages and repositories |
| ARTIFACTHUB_REPOSITORIES | Repositories to collect, by name, with their packages |
| ARTIFACTHUB_PACKAGES | Packages to collect, as `kind/repository/name`, e.g. `helm/bitnami/redis` |
| ARTIFACTHUB_HUB_STATS | Collect the totals of the hub, default: `false` |
| ARTIFACTHUB_URL | Artifact Hub address, default: `https://artifacthub.io` |
| ARTIFACTHUB_API_KEY_ID | API key id, no default; set with ARTIFACTHUB_API_KEY_SECRET or not at all |
| ARTIFACTHUB_API_KEY_SECRET | API key secret, no default |
| COLLECT_INTERVAL | Time between two collections, default: `15m`, at least `5m` (the API caches its answers for 5 minutes) |
| WEB_PATH | Path for metrics, default: `/metrics` |
| WEB_ADDR | Address for this exporter to run, default: `:8080` |

The kinds are the slugs of Artifact Hub: `helm`, `falco`, `opa`, `olm`, `tbaction`, `krew`, `helm-plugin`, `tekton-task`, `keda-scaler`, `coredns`, `keptn`, `tekton-pipeline`, `container`, `kubewarden`, `gatekeeper`, `kyverno`, `knative-client-plugin`, `backstage`, `argo-template`, `kubearmor`, `kcl`, `headlamp`, `inspektor-gadget`, `tekton-stepaction`, `meshery`, `opencost`, `radius`, `bootc` and `kagent`.

#### Rate limit

Artifact Hub rate limits its API without documenting the numbers: past roughly 100 requests a minute, it answers 429. The exporter sends one request a second, and retries a 429 (waiting 30 s, then 60 s) or a 5xx a few times. A collection costs about 2 requests per package (its details and its views) and 1 per 60 packages listed, so it takes about 2 seconds per package: an organization of 500 packages takes 17 minutes, longer than the interval. Prefer repositories or packages for large organizations.

A target that fails keeps its previous metrics. A package that fails keeps its last details and views, with fresh search fields; a package never collected is left out until a later collection succeeds. Each failure counts in `artifacthub_collection_errors_total` and holds back `artifacthub_collection_last_success_timestamp_seconds`.

### Metrics

Package metrics have the labels `kind`, `repository` and `package`; repository metrics `kind` and `repository`.

| Metric | Description |
|--------|-------------|
| `artifacthub_package_info{package_id, publisher, version, app_version, license}` | Always 1; the latest version and identity |
| `artifacthub_package_stars` | Stars |
| `artifacthub_package_last_release_timestamp_seconds` | Unix time of the latest release |
| `artifacthub_package_deprecated`, `_signed`, `_official` | 1 if so |
| `artifacthub_package_production_organizations` | Organizations using it in production |
| `artifacthub_package_vulnerabilities{severity}` | Vulnerabilities of the security report (`critical`, `high`, `medium`, `low`, `unknown`); absent without a report |
| `artifacthub_package_versions` | Versions |
| `artifacthub_package_subscriptions`, `_webhooks` | Subscriptions and webhooks |
| `artifacthub_package_views_last_7d` | Views in the last 7 complete UTC days |
| `artifacthub_package_views_yesterday` | Views in the last complete UTC day |
| `artifacthub_repository_info{repository_id, publisher, url, verified_publisher, official}` | Always 1 |
| `artifacthub_repository_last_tracking_timestamp_seconds`, `_errors` | Last tracking time, and 1 if it had errors |
| `artifacthub_repository_last_scanning_timestamp_seconds`, `_errors` | Last security scan time, and 1 if it had errors |
| `artifacthub_repository_disabled` | 1 if disabled |
| `artifacthub_repository_packages` | Packages collected in it; absent for a repository only collected through some of its packages |
| `artifacthub_hub_packages`, `_releases` | Totals of the hub (`ARTIFACTHUB_HUB_STATS`) |
| `artifacthub_collection_last_success_timestamp_seconds{target}` | Unix time of the target's last successful collection |
| `artifacthub_collection_errors_total{target}` | Failed collections of the target |
| `artifacthub_collection_duration_seconds{target}` | Duration of the target's last collection |
| `artifacthub_api_requests_total{endpoint, code}` | Requests to Artifact Hub; `code` is `error` without a response |

A package or repository found by several targets is exported once. `target` is `org:<name>`, `user:<alias>`, `repository:<name>`, `package:<kind>/<repository>/<name>` or `hub`.

#### Alerts

```yaml
groups:
  - name: artifacthub
    rules:
      - alert: ArtifactHubCollectionStale
        expr: time() - artifacthub_collection_last_success_timestamp_seconds > 3600
        annotations:
          summary: "{{ $labels.target }} has not been collected for an hour"
      - alert: ArtifactHubRepositoryTrackingErrors
        expr: artifacthub_repository_last_tracking_errors == 1
        for: 1h
      - alert: ArtifactHubCriticalVulnerabilities
        expr: artifacthub_package_vulnerabilities{severity="critical"} > 0
        for: 1d
```

### Container

The image is built in [spnngl/images](https://github.com/spnngl/images).

    docker pull ghcr.io/spnngl/images/artifacthub-exporter
    docker run -d -p 8080:8080 -e ARTIFACTHUB_ORGS=artifacthub ghcr.io/spnngl/images/artifacthub-exporter

### Development

    make check

Runs every CI check; needs [golangci-lint](https://golangci-lint.run/) on the `PATH` and a C compiler for the race detector. `make install` installs the binary.

Copy `.env.example` to `.env` and set your preferences: the exporter reads it at start.

### License

[Apache-2.0](LICENSE)
