# artifacthub-exporter

Prometheus exporter for Artifact Hub. The root package is the Artifact Hub API client; `cmd/artifacthub-exporter` is the exporter binary.

## Rules

- Edit what agents read (every `AGENTS.md`, code comments) with the `writing-for-agents` skill. Each `AGENTS.md` has a `CLAUDE.md` symlink beside it.
- Finish a change when `make check` passes.
- Commit with the `git-commit` skill. Pull requests fail CI unless every commit subject is a Conventional Commit (`type(scope)!: description`; types feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert).
- Budget the requests: CloudFront answers 429 past roughly 100 a minute (undocumented), so the client sends one a second and a collection takes about 2 s per package. Count the requests a change adds per package.
- The container image is built in `github.com/spnngl/images` and published as `ghcr.io/spnngl/images/artifacthub-exporter`; container changes go there.

## Read before you work

- Before adding or changing a metric, read `cmd/artifacthub-exporter/AGENTS.md`.
- Before editing `.github/` or `renovate.json5`, read `.github/AGENTS.md`.
