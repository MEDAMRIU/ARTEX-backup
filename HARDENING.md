# v0.3.12 security hardening

Branch: `hardening-v0.3.12`. Baseline: `0ec51e37993aa50047317b009a05593c61bbdf1e` (`v0.3.12`). Reviewed 11 October 2026.

This branch repairs the application and deployment boundaries identified in the baseline audit. It preserves the v0.3.12 agent implementation and Norma 0.4.0. English localization has not started.

## Changes

| Area | Repair |
|---|---|
| First-time setup | Fail closed on database errors; insert the first password atomically instead of upserting; only one concurrent setup request succeeds. |
| Password handling | Enforce the UI's eight-character minimum on the API, with bcrypt's 72-byte maximum; fail closed for login and password-change database reads. |
| JWTs | Require HS256, expiration and the expected subject; publish complete signing keys atomically without overwriting a concurrent creator; preserve migrated keys and reject corrupt existing keys. |
| Workspace | Use Go `os.Root` operations for listing, reading, writing, creating directories, deleting, downloading and uploading. Resolution remains confined during symlink swaps; relative links within the root work. Read/download use an opened file handle. |
| Chat attachments | Apply the same workspace boundary; create collision-free filenames exclusively so simultaneous uploads cannot overwrite an attachment. Remove multipart spill files after handling requests. |
| Skill editor | Confine file and metadata editing to the selected skill through root-relative handles. Reject non-regular archive entries and enforce the actual decompressed per-file limit. |
| Browser access | Replace wildcard CORS with same-host origins, the local development UI on port 3000, and optional exact `ARTEX_ALLOWED_ORIGINS`. Reject unrelated browser origins before they reach setup or authenticated endpoints. |
| Database creation | Quote embedded double quotes in database identifiers. |
| Deployment | Bind native HTTP and Compose published ports to loopback; build the local fork instead of pulling an upstream `latest` image; use an unprivileged container account and `no-new-privileges`. Persist the JWT key outside the workspace. |
| Install/update supply chain | Remove downloaded shell installers; pin base-image digests and browser-tool versions with an integrity lockfile; disable Norma's automatic ripgrep installation by default. Point self-update at `MEDAMRIU/ARTEX-backup`. |
| Installer credentials | Create credential files with restrictive permissions and encode JSON as data; avoid interpolating credentials into sed programs. |
| Test reliability | Fix cleanup registration so fake model profiles are removed before the manager closes. |

The authentication backport derives from upstream commit `a951e4aad3c415f41b71d9a026e6a066ec0e98e2`; unrelated changes in that commit and newer feature bundles were not imported.

## Behavior preserved

`agent/`, `guard/`, `intercept/` and `db/schema.sql` have no diff against v0.3.12. Existing prompts, tool-approval policies and task execution logic are retained. Norma remains exactly `v0.4.0`.

No later asset allow/block rules, target restrictions, planner-context limits or new approval policies were added. A preserved source implementation does not prove identical responses from live models or third-party services.

Intentional operational changes are the stronger password validation, filesystem and browser boundaries, localhost defaults, controlled update source, and unprivileged Docker account. Commands that require operating-system privileges may need explicitly configured container capabilities. The application has not acquired a new target-policy layer.

## Dependencies

| Component | Baseline | Hardened |
|---|---|---|
| Minimum Go toolchain | 1.26.3 (audit built with 1.26.6) | 1.26.9 |
| Norma | 0.4.0 | 0.4.0, unchanged |
| `golang.org/x/net` | 0.58.0 | 0.60.0 |
| `golang.org/x/crypto` | 0.55.0 | 0.57.0 |
| `github.com/klauspost/compress` | 1.18.6 | 1.18.7 |
| Next.js | 16.2.9 | 16.4.0 |
| shadcn generator | 4.12.0 dependency | Removed; its exact required CSS is retained under `web/src/styles/vendor/` with the MIT license |
| Optional API-recon Puppeteer | 23.11.1 | 25.13.0 |
| Playwright runtime tooling | Floating `latest` installs | Exact versions plus `runtime-tools/package-lock.json` |

Additional transitive dependencies were patched through the lockfiles. The web application, optional recon tool and container browser-tool lockfile have zero advisories in the successful npm audit responses captured on 11 October 2026.

`govulncheck` on Darwin/arm64 and Linux/arm64 has no affected imported packages or reachable vulnerable symbols. It still reports **GO-2026-5932 at module level** because `x/crypto` contains the unmaintained OpenPGP package. The application does not import that package. This distinction is retained rather than suppressing the advisory.

Base images and npm packages are pinned. Debian packages are obtained from signed distribution repositories at build time, so this is not a claim of bit-for-bit reproducible operating-system layers or an exhaustive OS vulnerability audit.

## Verification

- Full Linux/arm64 suite with PostgreSQL: **523 top-level tests passed, 0 failed, 1 skipped**, across 14 test packages. Two additional packages have no tests.
- The skipped test is `TestLiveContextReview`, which requires private live-model configuration.
- The final server suite was repeated after the last filesystem change: **199 passed, 1 live-model skip**.
- Race detector passed for the focused authentication, JWT key creation, workspace, chat-upload, skill-editor and browser-origin regression tests.
- Eight concurrent setup requests produced exactly one winner; the winning password remained stored.
- Go static analysis and module verification passed.
- Next.js static export and TypeScript checks passed; the build generated 29 pages.
- Fifteen HTTP smoke checks passed, including origin rejection, authentication and workspace read/write/download/delete.
- Real-browser setup/login reached the task screen; ten additional main routes loaded. This checks navigation/rendering, not every interactive feature.
- Installer shell syntax, Compose configuration, credential encoding and restrictive file permissions were checked.
- Docker image built successfully; runtime UID is 10001. MCP/CLI tools resolve offline. The signing key is mode 0600 and persists across container recreation; a previously issued JWT and a workspace file remain valid afterward.
- Embedded builds succeeded for Linux/arm64, Linux/amd64, macOS/arm64 and Windows/amd64. Only Linux/arm64 was exercised as a running application.

The local evidence directory is `/Users/amriu/Developer/ARTEX-hardening-2026-10-11/evidence/`. It includes raw audit outputs, package test logs, security race-test logs, HTTP smoke results, frontend/TypeScript logs and the container build log. It contains only disposable test credentials, not real LLM keys.

## Running the hardened branch

Use Go 1.26.9 or newer in the 1.26 line, Node/npm, Python 3 for installer configuration, Docker and Compose. The Go minimum is enforced by `go.mod`.

For a fresh local Docker installation:

```sh
cp .env.example .env
# Set POSTGRES_PASSWORD to a strong URL-safe value in .env.
./scripts/build-local-image.sh
docker compose up -d --build
```

Open `http://127.0.0.1:8787`. The image is built from this checkout. No ARTEX image is pulled from the original maintainer. Native builds also default to `127.0.0.1:8787`; an explicit `-addr` can change that.

Docker publishing from the release workflow stays disabled until the repository variable `ARTEX_DOCKER_IMAGE` is explicitly set to an owned image name; release write permission is limited to the release job.

The image runs as UID/GID 10001. Named volumes preserve workspace data, skills, PostgreSQL data and `/app/auth/jwt.key`. `ARTEX_AUTH_DIR=/app/auth` keeps the signing key outside the browsable workspace. A separate frontend can be permitted using comma-separated exact origins in `ARTEX_ALLOWED_ORIGINS`; no wildcard is supported.

**Existing installations:** this Compose file uses named application volumes rather than the old `./data` and `./skills` bind mounts. Back up and deliberately migrate existing data/skills and the signing key before switching an existing installation. Do not assume old bind-mounted data is automatically copied. The work performed for this branch used fresh disposable fixtures and did not migrate a production deployment.

Tests modify database state. Run against a disposable database, never production. Package-serial execution avoids shared-fixture interference:

```sh
ARTEX_PG_DSN='postgres://test:test@127.0.0.1:5432/artextest?sslmode=disable' \
  NORMA_RIPGREP_NO_INSTALL=1 go test -p 1 -count=1 ./...
go vet ./...
(cd web && npm ci --ignore-scripts && npm run build:static && npx tsc --noEmit)
```

The isolated authentication test creates and drops its own randomly named database; its test PostgreSQL account needs permission to create databases.

## Remaining release checks

1. **Pending upstream security disclosure.** On October 8, Next.js announced an October 14, 2026 security update for two critical and one high-severity upstream issues. Affected versions and fixes are not yet published. Today's clean audit cannot resolve that pending disclosure. Reassess and patch once the release is available. This deployment serves the exported frontend through Go and does not run a Next.js server, but unpublished impact cannot be ruled out. Source: https://nextjs.org/blog/upcoming-nextjs-security-update-october-2026
2. **Live-model verification.** No real provider credentials or external target tests were used. Real AI behavior and provider compatibility remain unverified.
3. **Execution privilege model.** Shell, Python and MCP tools intentionally retain the application's account privileges. Filesystem API containment is not a sandbox for an intentionally authorized shell. The hardened container separates the application from the host; do not mount host credentials or the Docker socket into it.
4. **Distribution/license review.** The v0.3.12 tag lacks a root LICENSE; a later upstream tag adds one. This branch does not decide the licensing of an English distribution. The vendored stylesheet's own license is included.

This is a tested hardening baseline for continuing development, not a guarantee that no undiscovered vulnerability exists. English translation should preserve protocol keys, schemas, enums and agent semantics and use these regression tests as its baseline.
