# Workflows and Renovate

The shared workflows and the Renovate preset come from `github.com/spnngl/.github`; its `README.md` documents their inputs and permissions.

- Pin third-party actions to a full commit SHA with a `# vX.Y.Z` comment. `spnngl/*` references stay at `@main`: the preset disables their updates and `zizmor.yml` allows them.
- Annotate a tool version with `# renovate: datasource=<datasource> depName=<name>` on the line right above `key: "<version>"`, unless Renovate already tracks it (its github-actions manager reads `golangci-lint-action`'s `version`).
- `release.yml` publishes a `vX.Y.Z` tag on `main`; the packages come from `.goreleaser.yaml`, and the lint exceptions from `packaging/`. CI builds a snapshot, so test a packaging change with `goreleaser release --snapshot --clean`.
- Grant permissions per job, with `permissions: {}` at the top.
- Done when `actionlint` and `zizmor .` pass, and `renovate-config-validator --strict renovate.json5` for a Renovate change.
