# Contributing

Write code, documentation, commit messages, Issues and pull requests in English. Discuss unresolved product, architecture, compatibility and scope decisions in Issues. Accepted design belongs in `spec/`; changes go through pull requests on descriptive short-lived branches from `main`.

Use Conventional Commit PR titles and draft PRs until ready for CI and review. Preserve unrelated work and resolve review threads before merging. Repository governance retains the parent's squash-only merge, protected main and immutable release-tag policies.

## Development

Use Go 1.25+, Python 3.13, uv and Make. Python is generator/test tooling, not a runtime dependency of the Go module. `make install` installs locked tooling and Go dependencies. `make format` formats handwritten tooling and Go sources; `make check` verifies formatting, types and vet; `make check-all` also checks generated drift, runs race-enabled tests and builds the module. No Service checkout, database or sibling SDK is required.

Keep the design direct, preserve typed unions and omitted/null/value distinctions, and never patch generated files manually. Change the local generator/configuration instead. Tests cover transport ownership, cancellation, redacted diagnostics, typed requests and pinned wire evidence. Reuse still-valid checks and report exact results, including limitations.

## Releases

The release channel remains `release/a13n/go/<version>`, using stable `X.Y.Z` or `X.Y.Z-rc.N` (positive N with no leading zeroes). The canonical Go module tag is now root `v<version>`, not `sdk/go/v<version>`. The module path is `github.com/converge-ai-labs/a13n-sdk-go`; consumers must update imports from the old parent-repository module.

Tag a main-line commit whose required CI passed. Use the `sdk-go-github` release environment. Release tags are immutable; do not rewrite or move them. Repository initialization does not authorize an actual release, and private repository access must be available to module consumers. Local checks do not prove registry or GitHub publication.

### Release operations

The release workflow verifies that the tagged commit is an ancestor of `origin/main` and that its latest push-triggered `ci.yml` run on `main` completed successfully. It fails rather than waiting for CI or silently using an older successful attempt. Rerun a blocked release only after CI succeeds. This read-only check uses `actions: read`; it does not replace branch protection or rerun the full test suite. Local tests for this boundary require Git, Bash and jq and use a fake GitHub CLI response, not production credentials.

Version preparation and artifact builds operate in ephemeral checkouts. Do not commit their modified manifests/lockfiles. Release tags are immutable, and a retry must retain the same tag and source commit. Publication is not transactional across registry and GitHub: an earlier job may have published before a later job failed. Inspect the registry, tags, artifacts and workflow result before rerunning; do not move tags or assume every publish step is idempotent.

Changelogs use first-parent history scoped to this repository and select only ancestor tags from the same release channel. RCs compare against an earlier RC for the same target, otherwise the preceding stable; stable releases compare against the preceding stable. PR labels classify and omit entries with a Conventional Commit fallback. Curated `.github/release-notes/COMPONENT/VERSION.md` notes are optional. Preview an existing, locally fetched tag without publishing (GitHub PR-label reads still require `gh` authentication):

```bash
GITHUB_REPOSITORY=converge-ai-labs/a13n-sdk-go \
  python3 scripts/create-github-release.py a13n-go 1.2.3 "a13n SDK 1.2.3" --dry-run
```

The first channel release uses initial or curated notes rather than attributing extracted monorepo history to this repository's PR numbers. RC GitHub Releases explicitly avoid `latest`. Repository privacy is separate from package visibility: registry publication can expose the package even when its source repository remains private.

Go publication creates a lightweight root `v<version>` tag at the verified release commit. An existing tag at another object is an error, never force-updated. Versions 2 and later require a separately reviewed matching `/vN` module path and imports; release preparation rejects a mismatched module instead of publishing an unusable tag. `GITHUB_TOKEN` needs contents write in `sdk-go-github`; no registry token is needed.

## Service contract updates

Contract tooling requires Git, Bash, jq and `shasum`. Ordinary builds need no Service checkout or credentials. Generation verifies the local manifest and hashes before reading the snapshot; `contract/README.md` is local guidance, not upstream evidence.

From a clean SDK branch, with the Service repository's full `origin/main` history fetched:

```bash
bash scripts/sync-contract.sh /path/to/agent-foundation FULL_40_CHARACTER_SERVICE_SHA
make generate
make check-all
```

Sync copies Git blobs, never working-tree files or executable Service code. It requires a complete main-line SHA, forward ancestry from the old pin, and byte-accurate old provenance. Missing/malformed inputs fail before writes; same-SHA retries do nothing. Inspect any interrupted local write and restore only the affected snapshot before retrying. The snapshot includes OpenAPI, both wire schemas, fixtures, API conventions, Native streaming and queue semantics. The source compare exposes runtime-only changes too. Follow recorded upstream paths for related specifications; accepted specs take precedence over inconsistent implementation.

`sync-service-contract.yml` receives Service dispatches or a manual full SHA and opens a **draft PR** containing the snapshot. Maintainers generate, adapt and test, then mark it ready for ordinary CI and review. It never merges, tags or releases. Each SHA has one branch; retries preserve existing open/closed PRs and reviewer edits. If push succeeded before PR creation failed, a retry creates the missing draft without rewriting the branch. Run `open-contract-pr.sh` only in an ephemeral CI checkout.

### Setup

Install both repositories' workflows on their default branches first. Use a dedicated GitHub App installed only on Service and the four SDK repos, with Contents and Pull requests read/write. Configure variable `SERVICE_CONTRACT_APP_CLIENT_ID` and secret `SERVICE_CONTRACT_APP_PRIVATE_KEY` in those repos (or restrict an organization secret to them). Never copy a developer's OAuth token. Workflows request separate Service-read and destination-write tokens and do not persist checkout credentials. Without a client ID the job is skipped; configuration enables it without another feature flag.

Verify one known main SHA end to end before relying on notifications: dispatch, draft, provenance, adaptation and ready-PR CI. Source import is not compatibility acceptance. Offline tests use temporary Git repos and fake GitHub responses; they do not prove App installation or delivery. Registry credentials and release authorization remain separate.
