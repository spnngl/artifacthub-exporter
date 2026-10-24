# Exporter

- A `poller` collects each `Target` on an interval into an immutable `snapshot` in the `store`; `collector.Collect` reads the store, so a scrape never waits on the API.
- Name a metric `artifacthub_<name>` from the `namespace` constant; a timestamp ends in `_timestamp_seconds`.
- Metric names and labels are public API: users' alerts and dashboards query them. A rename is a breaking change (`feat!:`).
- Keep data stale rather than missing. A failed target keeps its previous snapshot; a failed package keeps its last details and views (`packageData`). Each counts in `artifacthub_collection_errors_total{target}` and leaves `artifacthub_collection_last_success_timestamp_seconds` behind: that pair is the alert.
- `Collect` always succeeds: a `prometheus.NewInvalidMetric` would fail the scrape of every target.
- Targets overlap: `Collect` sends a package once per `package_id` and a repository once per `repository_id`, as a duplicate series fails `/metrics`.
- A package's own endpoint lacks its stars and whether it is official, so a `PackageTarget` finds it in package search by repository, then fetches details and views.
- `views_last_7d` and `views_yesterday` count complete UTC days: today's views are partial.
- A metric that may not exist (`vulnerabilities`, `repository_packages`) stays absent rather than 0.
- Add a descriptor through `collector.desc`, which lists it in `Describe`: `/metrics` fails with HTTP 500 when `Collect` sends a metric `Describe` lacks.
