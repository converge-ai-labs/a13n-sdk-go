# Contributing

Write code, documentation, commit messages, Issues and pull requests in English. Discuss unresolved product, architecture, compatibility and scope decisions in Issues. Accepted design belongs in `spec/`; changes go through pull requests on descriptive short-lived branches from `main`.

Use Conventional Commit PR titles. Draft PRs run the full CI gate; mark ready after compatibility review and resolving failures. Preserve unrelated work and resolve review threads before merging. Repository governance retains the parent's squash-only merge, protected main and immutable release-tag policies.

## Development

Use Go 1.25+, Python 3.13, uv and Make. Python is generator/test tooling, not a runtime dependency of the Go module. `make install` installs locked tooling and Go dependencies. `make format` formats handwritten tooling and Go sources; `make check` verifies formatting, types and vet; `make check-all` also runs race-enabled tests and builds the module. No Service checkout, database or sibling SDK is required.

### Quality gates

Run `make hooks-install` once after `make install` to install pre-commit in this checkout. Commit hooks run file hygiene, Markdown/Ruff formatting, and gofmt on changed Go files. Vet, race tests, generation, and builds remain explicit Make/CI checks.

- `make format` applies Go, Python, and owned Markdown formatting.
- `make lint` checks formatting, `go vet`, and Python style without rewriting files.
- `make typecheck` runs Pyright `standard` on Python tooling; Go compilation/type checking is already covered by vet, tests, and build.
- `make check` combines the fast static gates.
- `make hooks-check` runs all hooks. Formatter changes fail the run for review and restaging; do not bypass hooks.
- `make test` includes an isolated consumer that resolves the SDK from a local module ZIP proxy, without a source-tree `replace` directive or Service access.
- `make check-all` runs static checks, race-enabled tests, installed-module acceptance, and builds. CI uses the same entry point without installing hooks.

Vendored contract evidence is excluded from hooks and Markdown formatting, except the locally owned `contract/README.md`. The generated client has a narrow large-file exception; gofmt, vet, compilation, and tests still cover generated Go. Fix generated behavior in the generator. Existing vet/Ruff/Pyright checks provide a moderate baseline without introducing another aggregate linter.

Keep the design direct, preserve typed unions and omitted/null/value distinctions, and never patch generated files manually. Change the local generator/configuration instead. Tests cover transport ownership, cancellation, redacted diagnostics, typed requests and pinned wire evidence. Reuse still-valid checks and report exact results, including limitations.

Generation replaces generator-owned output directly. Validation exercises the committed bindings through language-native checks and behavior tests, rather than regenerating them for byte comparison or rechecking snapshot hashes. Run `make generate` after changing inputs, templates, or generator code, review the diff, then run `make check-all`. Commit hooks remain a local convenience and are not rerun by the full gate.

### Disposable Service acceptance

`uv run --locked python scripts/accept-installed.py` exercises the installed module against an explicitly configured HTTPS Service. Provide `A13N_SERVICE_URL`, `A13N_API_TOKEN`, `A13N_AGENT`, `A13N_CLIENT_TOOL_AGENT`, `A13N_CA_BUNDLE` and `A13N_FAILURE_PROMPT`; the script neither starts a Service nor bypasses TLS verification. Use disposable state because these journeys create and update resources. Pass `--offline` for the local HTTP fixtures used by `make test`. The failure prompt requires a disposable deterministic model that fails only its latest user input, not every later continuation containing that marker in history. The journey checks failed/cancelled last-Run history with normal explicit Send, default baseline/continuation and historical ordinal windows; a short sealed Run may retain its whole tail, so it explicitly reports when exclusion of earlier pages was not exercised live. Whole-tail-beyond-limit and sealed-recent-window distinctions have isolated mock dispatch coverage. Use `--save-zip tmp/module.zip` to retain the exact isolated-consumer module artifact. This optional integration check does not prove external model or memory-provider connectivity.

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

Contract synchronization requires Git, Bash and jq. Ordinary generation and builds read the committed local inputs directly and need no Service checkout or credentials. `contract/source.json` records the source repository, full commit SHA and original paths; it is attribution, not a checksum manifest. `contract/README.md` is local guidance, not upstream evidence.

From a clean SDK branch, with the Service repository's full `origin/main` history fetched:

```bash
bash scripts/sync-contract.sh /path/to/agent-foundation FULL_40_CHARACTER_SERVICE_SHA
make generate
make check-all
```

Sync copies Git blobs, never working-tree files or executable Service code. It requires a complete main-line SHA and forward ancestry from the old pin. Missing/malformed new inputs fail before writes. Same-SHA retries do nothing; a newer SHA with identical consumed Git blobs also leaves the pin and local files unchanged. Content comparison uses the upstream revisions, not reviewer edits in the SDK or a checksum manifest. Inspect any interrupted local write and restore only the affected snapshot before retrying. The next synced snapshot includes OpenAPI, the thread-stream schema, API conventions, and the Runs, Facts and Delivery, and API semantics. On a successful sync, files in the prior source manifest that are no longer selected are retired; SDK-local files are preserved. The source compare exposes runtime-only changes too. Follow recorded upstream paths for related specifications; accepted specs take precedence over inconsistent implementation.

`sync-service-contract.yml` receives Service dispatches or a manual full SHA, prepares the SDK toolchain and invokes `open-contract-pr.sh` in an ephemeral checkout. The script selects the existing rolling proposal (or current SDK `main`), copies the requested snapshot and runs `make generate`, then creates or updates a **draft PR** containing the snapshot and generated output. Only `contract/` and `generated/` are staged. The handwritten semantic workflow is reviewed separately if a contract change requires adaptation. It never merges, tags or releases.

Generation failure stops before committing or pushing; discard the ephemeral checkout and retry after fixing the cause. There is no contract-only fallback. Full SDK CI runs on drafts, so compilation/test failures remain visible for maintainer adaptation. Review compatibility and generated changes, fix templates or handwritten code as needed, regenerate and resolve CI failures before marking ready.

Each repository has at most one open automatic update PR on `sync/service-contract`. Its snapshot still records the complete immutable Service SHA. The workflow serializes updates; the script additionally compares incoming ancestry against both the accepted `main` pin and the pending proposal, so equal or older notifications never regenerate or rewind newer work. A newer source with changed consumed inputs merges current SDK `main` into the proposal and appends the snapshot/generated changes without force-updating the branch. Identical inputs relative to the selected pending pin stop before merging, installing dependencies, generating, or writing PR metadata; no new commit or PR CI run is created just to advance a SHA. Handwritten code, templates and reviewer commits are retained; merge conflicts, failed generation and concurrent pushes stop before replacing remote work. Generated files remain generator-owned, not a place for handwritten adaptation.

The marked source block and PR title track the proposed SHA and its range from the accepted pin. Notes outside that block are preserved. Changed contract inputs return ready PRs to draft and require fresh review; same-SHA retries preserve readiness and only reconcile metadata. A newer SHA with identical inputs preserves the existing source pin, reviewer notes and readiness. Retry the recorded pending SHA when recovering a metadata-only publication failure. If push succeeded before PR creation/editing failed, retry reuses the remote snapshot without regenerating it. Closing an unmerged rolling PR pauses automatic proposals until a maintainer reopens it.

After merge, the next update starts from SDK `main`. Normally GitHub deletes the merged branch; if it remains at exactly the recorded merged head, automation removes it with an exact-head lease only after generation succeeds, then creates the next branch without replacing a concurrently created ref. Any commits added after merge are instead retained and reviewed. A missing branch for an open PR is an error, not permission to discard reviewer work. During migration, inspect old `sync/service-contract-<SHA>` PRs for manual work and close superseded proposals only after the rolling replacement is verified.

### Setup

Install both repositories' workflows on their default branches first. Use a dedicated GitHub App installed only on Service and the four SDK repos, with Contents and Pull requests read/write. Configure variable `SERVICE_CONTRACT_APP_CLIENT_ID` and secret `SERVICE_CONTRACT_APP_PRIVATE_KEY` in those repos (or restrict an organization secret to them). Never copy a developer's OAuth token. Workflows request separate Service-read and destination-write tokens and do not persist checkout credentials. Without a client ID the job is skipped; configuration enables it without another feature flag.

Verify one known main SHA end to end before relying on notifications: dispatch, generation, draft contents, provenance and draft CI. Successful generation is not compatibility acceptance. Offline tests use temporary Git repos and fake GitHub responses; they do not prove App installation or delivery. Registry credentials and release authorization remain separate.
