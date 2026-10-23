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

## ChatGPT connection — 14 October 2026

Implemented on `feat/chatgpt-sign-in` from hardening baseline `1fa2cc4`.
The checkout was moved by the owner's cleanup to
`/Users/amriu/Developer/_attic/2026-10-14-cleanup/ARTEX-backup`; this work does
not restore or deploy that archive.

Model settings now support the official Sign in with ChatGPT public flow:
loopback authorization-code callback, PKCE, state, nonce, signed ID-token
validation, multiple account registrations, reconnect and sign-out. Native
ARTEX and the browser must run on the same computer. Remote and ordinary
container installations need callback forwarding and are not supported by this
initial loopback implementation.

Credentials live in `chatgpt-auth/credentials.enc` beneath the authentication
key directory (`ARTEX_AUTH_DIR` when set). AES-GCM encryption derives from the
existing JWT key; preserve that key with the encrypted credentials when moving
an installation. Directory permissions are 0700 and credential files 0600.
OS file locks serialize refresh across processes; each operation reloads disk
under the lock. Rotated material survives a temporary signing-key outage but
cannot be used until the new identity validates. Sign-out invalidates returning
sign-in attempts across processes and attempts remote token revocation.
Profiles and browser APIs expose account references, never bearer credentials.

The existing Norma Responses provider remains the inference adapter. The
ChatGPT transport fixes the destination to OpenAI, attaches managed tokens,
uses streaming with `store:false`, removes unsupported preview parameters,
groups function tools in a namespace, and preserves full tool history. Model
choices come from the selected account's catalog. A ChatGPT error cannot switch
to an API-key profile or replay the whole worker intent; incomplete streams are
reported as failures. This does not expand account model eligibility or limits.

Validation after review fixes:

- `go test -race -p 1 -count=1 ./...` passed against a fresh disposable PostgreSQL
  database. A prior parallel run against a reused database failed two existing
  company-attribution fixture tests; the documented fresh/serial run passed.
- Focused OAuth tests cover PKCE/state/nonce, identity mismatch, encrypted
  persistence, rotating refresh tokens, concurrent managers, transient JWKS
  failure recovery, sign-out races, scopes, catalog filtering and SSE failures.
- Database round-trip and authenticated server endpoint tests passed.
- `go vet ./...`, `git diff --check` and the Next.js static build passed.
- Separate read-only verification repeated the focused race tests and database
  round-trip and found no remaining blocker in the reported paths.
- Native embedded UI built and ran at `http://127.0.0.1:18787`. Browser setup,
  model settings and Continue with ChatGPT worked; the new tab reached OpenAI's
  verification page. Live consent, actual account eligibility and real inference
  remain unverified. No external model requests or paid operations were made.

Manual acceptance: open `/system/llm/`, create a profile with **新建**, click
**Continue with ChatGPT**, complete OpenAI sign-in/consent, and return to ARTEX.
Expect the account and **Using ChatGPT plan**. Load models with the button beside
**模型**, choose one, name and save the profile, then activate it with **设为激活**.
The connection test (**测试连接**) makes a real request using the plan allowance.
Sign out should retain the registration while making its credentials unusable.
Final acceptance remains pending the owner's live check.

Protocol references:
- https://developers.openai.com/siwc/token-sharing-open-source/sign-in
- https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions
- https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
- https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations

### Live acceptance follow-up — 14 October 2026

The owner completed ChatGPT consent and requested a live end-to-end test.
This supersedes the earlier unverified status for sign-in, catalog, inference,
credential persistence across restart, and the chat/tool/history path.

The first live response was valid SSE with HTTP 200 and no Content-Type header.
The transport's strict MIME check incorrectly rejected it. The transport now
allows an absent header and still requires validated SSE with a completion
event; explicit incompatible MIME types remain rejected. A regression test
covers both valid headerless SSE and rejection of a headerless JSON body.
Independent read-only verification passed the stream regression tests.

Live evidence on the local native preview:

- Signed-in account is connected with plan usage enabled; catalog loaded.
- Restarted the local server with the same encrypted store; no new sign-in needed.
- Connection test through ARTEX: `gpt-6-sol`, reply `OK`, 4041 ms.
- Saved profile 1 (`ChatGPT · GPT-6 Sol`) and conversation 1 (`ChatGPT live
  connection and tool test`), explicitly bound to this profile.
- Model invoked Read on `smoke-check.txt`, returned its exact random marker and
  computed 17 + 25 = 42. The marker was supplied only by the tool result.
- A second chat turn returned the same marker and 84 without further tool calls.
- No target-site testing, code generation by the test model, API-key billing or
  paid service operations. These requests used the owner's ChatGPT plan allowance.

Focused `go test -race ./chatgpt`, `go vet ./chatgpt`, `git diff --check` and the
embedded native rebuild passed. Local evidence is in
`/tmp/artex-chatgpt-local/{connection-test,tool-test,context-test}.json` and the
persisted conversation. Actual time-based token renewal remains fixture-tested;
the live token had not yet reached its renewal window. Task planner/worker
orchestration, MCP integrations and real target workflows were not covered by
this bounded chat smoke test.

## Update research and blind baseline — 22 October 2026

Owner scope: research updates/add-ons, run a blind external test against the selected
owned VPS `51.195.252.69`, and perform English translation with subagents only after
approval of selected changes. No upstream feature merge, translation or server
configuration change has been approved or performed in this batch.

### Update selection

The original Autumn-27 repository currently returns 404. Hinln/ARTEX is a public
backup; the active RuoJi6 fork released v0.3.23 on 20 October (tag commit 620ca5d).
The tag was fetched into a review-only ref. Our tested source remains f9367f8 on
feat/chatgpt-sign-in. A wholesale replacement would lose our ChatGPT adapter and
could regress dependency versions: the reviewed upstream lockfile resolves Next
16.2.9 and sharp 0.34.5, while our branch uses Next 16.4.0; its Go module specifies
1.26.3 and older crypto/net/compress modules than our hardening baseline.

Recommended approval order:
1. Refresh supported dependencies without regressing our patches: Next 16.4.1 or
   a subsequently verified fixed version, and Node 22.23.4 or a later verified fix.
   Official advisories supersede the local npm production audit, which reported
   zero vulnerabilities on this date. Our served UI is a Go-hosted static export,
   with no Next server or image-optimization endpoint; this reduces those runtime
   paths but does not remove the need to update build/tool dependencies. The local
   baseline tool image reports Node 22.23.3.
2. Selectively backport reliability improvements: production SSE same-origin
   URLs, bounded graph context, tool-output truncation, session/compaction fixes,
   and evidence-preserving finding deduplication. Preserve OAuth/refresh isolation,
   no paid-provider fallback, hardening tests and pinned dependencies.
3. Add enforced scope and a shared request/time budget, then durable negative
   findings and evidence provenance. Prompt constraints and asset scope are not
   sufficient enforcement for arbitrary shell/network tools.
4. Add a clear interruption record distinguishing an app rule/approval, reviewer
   timeout/invalid output, provider authentication/quota/network error, model
   refusal, and target rejection. Existing app approvals already have an audit
   history; provider refusals need accurate classification, not string guessing.
5. Select a small tested tool set and evaluate it before adding whole frameworks.

### What v0.3.23 changes about refusals

Source inspection of b2373b2 and ed7eda5 shows an optional judge failure-policy
change: after an initial attempt plus three retries, reviewer failure defaults to
human approval (`ask`) rather than automatic allowance. The judge remains disabled
by default; saved explicit settings are honored. A valid deny/ask verdict is final,
not retried as a transient failure. The review prompt changes include script-source
snapshots and more concise structured explanations; these are not evidence of a
new blanket prohibition on authorized security testing. Model/provider refusals
are separate from these configurable application decisions. No ARTEX version or
setting can guarantee a hosted model will never refuse. No safeguards were disabled
for the baseline. Preserve scope and destructive-action protection; tune demonstrated
false positives narrowly if they appear.

### Named tool projects

| Project | What it adds | Recommendation |
|---|---|---|
| ScopeSentry | Distributed discovery and asset import | ARTEX already has a ScopeSentry sync adapter. Do not duplicate it. Review the current project's commercial licensing terms before deploying for business use. |
| rea | Ghidra/Hopper/IDA reverse-engineering tools over MCP | Defer until there is an APK/binary assessment; tool-provider licenses may be separate. |
| Strix | A separate sandboxed security-agent workflow | Use as an independent benchmark later, rather than nesting two planners. Its documented API credentials are not proof of support for our ChatGPT OAuth adapter. |
| PentAGI | Containerized tools, persistent memory and agent orchestration | Borrow bounded workflows and memory ideas; verify edition boundaries (some MCP/profile features are documented as Enterprise). |
| CyberStrikeAI | MCP orchestration, tool recipes, workflow and knowledge retrieval | Prioritize bounded output, cancellation, allowlists and resumable jobs; a recipe catalog does not prove every tool is installed. |
| garak | LLM vulnerability evaluation | Useful for testing ARTEX against malicious instructions in target content; not an external VPS scanner. |
| promptfoo | Model/agent regression and red-team evaluation | Useful for repeatable tool-choice, evidence-quality and English-translation tests. |

Primary sources checked:
- https://github.com/RuoJi6/ARTEX/releases/tag/v0.3.23
- https://github.com/RuoJi6/ARTEX/commit/b2373b2b766328a6eb6cf8d3d0413e5567656fbf
- https://github.com/RuoJi6/ARTEX/commit/ed7eda5e0c87f81cd3ce3f6ba8cbe446a277e583
- https://github.com/Autumn-27/ScopeSentry
- https://github.com/morluto/rea
- https://github.com/usestrix/strix
- https://github.com/vxcontrol/pentagi
- https://github.com/AIPentest/CyberStrikeAI
- https://github.com/NVIDIA/garak
- https://github.com/promptfoo/promptfoo
- https://nextjs.org/blog/nextjs-security-update-october-14-2026
- https://nodejs.org/en/blog/vulnerability/october-2026-security-releases

### Completed blind baseline

Existing f9367f8 was rebuilt as a Linux/arm64 embedded binary (Go 1.26.9) and ran
locally with a fresh PostgreSQL database. The task received only `51.195.252.69`
as target knowledge, plus authorization, a read-only scope and operating limits.
No SSH credentials, host inventory, previous tasks or project folders were mounted.
The app ran as UID 10001 with all capabilities dropped and no-new-privileges.
A separate gateway namespace restricted egress to the target, database, DNS and
specific provider/CDN IPs. This is useful host isolation, not a complete sandbox:
the worker shares the app identity and can access its mounted authentication store;
provider/CDN IP allowances are not domain-level enforcement. No owner server was
logged into, reconfigured or deployed to.

The saved ChatGPT token had expired on 14 October. A live connection request
renewed it and returned OK from GPT-6 Sol in 5710 ms; the renewed access expiry was
22 October 11:41:01 UTC. Planner and worker resolutions both selected GPT-6 Sol.
No paid-provider fallback was configured. Requests used the owner's ChatGPT plan.

Task 1 ran from 10:41:31 to 10:45:59 UTC (4m28s), with one worker and 1/1 goals
met. The planner created constraints/scope, assigned an intent, read the worker's
report and evidence fact, and marked the goal met. A separate read-only verifier
confirmed lifecycle and report consistency against actual APIs, scripts and JSONL.

- 12 TCP connection attempts across 11 unique ports, all to the selected IP.
- Minimum recorded initiation interval: 1.600623 seconds.
- Port 22: 43-byte SSH banner, then a second connection yielding a 43-byte banner
  and 1120-byte KEXINIT packet. Self-reported OpenSSH 9.6p1 Ubuntu packaging.
- TCP 21,25,53,80,443,3306,3389,5432,8080,8443 timed out. These are not proof of
  closed ports, and no HTTP application was reached in the sample.
- Zero confirmed vulnerabilities, no model refusal or app approval interruption.
  Optional legacy MAC advertisements remain unverified hardening leads, not exploits.
- No UDP/IPv6/full-port scan, authenticated access, remote execution, destructive
  action, or application exploitation. This narrow result does not certify the VPS.

Recorded task totals: 201141 input, 5411 output and 55552 cache-read tokens (as
reported by ARTEX; not a money charge or a single request's context size). This
provides a useful efficiency baseline before expanding tool/schema/context volume.

### Defects and limits demonstrated by the test

1. **Live UI updates:** embedded UI at localhost:18787 requests SSE on :8787 and
   gets connection refused. REST works. Upstream e2c8a23 already fixes production
   same-origin SSE; backport this independently of unrelated features.
2. **Report delivery:** the worker wrote a useful English report, but the built-in
   report export includes only generic finding rows and omits that assessment,
   negative results and evidence. The report also references `i3/...` while actual
   evidence is under `i3/i3/...`; do not silently correct the baseline artifact.
3. **Metrics consistency:** GET /api/tasks includes real 1/1 goal and token totals,
   while GET /api/tasks/1 returns default zeros for those metrics. Read the enriched
   list/token endpoints when verifying this version.
4. **Enforced limits and credentials:** instructions limited the run correctly,
   but the script's counter reset across processes; there is no shared enforced
   request budget. App authentication and worker execution also need separation
   before hostile-content/unattended assessments. Firewall packet counters are not
   request counts; observed compliance is based on script/log review, not packet capture.

An app stop/restart preserved the completed task, goal/token totals and connected
account with renewed expiry. Final evidence is outside the repository at
`/Users/amriu/.local/share/artex-blind-20261022`: `task-evidence/report.md`, raw
scripts/JSONL beneath `task-evidence/i3/i3/`, API snapshots, redacted browser console,
screenshots, restart check and private database/auth backups. The `current-auth`
copy and named auth volume contain the latest rotated tokens; the older 14 October
snapshot is historical and must not be resumed as the current token store.

The local test containers and dedicated browser are stopped after verification;
volumes/data are retained. A future isolated run must reapply and verify the saved
network policy before starting the app (namespace firewall rules do not persist
across container recreation/restart). No feature or translation source changed.
This dated documentation is saved locally for owner review, not committed/pushed.

Manual acceptance: open the saved English report and compare its 12-row table to
`task-evidence/i3/i3/probes.jsonl`; inspect the completed screenshot and compare
`report-export.md` to the worker report. Approval remains pending for a repair batch
and subsequent English translation with verified GPT-6 Sol coding subagents.

### Tool readiness audit — owner follow-up, 22 October 2026

Owner explicitly requires missing dependencies, unavailable Linux functionality,
broken integrations and untested paths to be visible. A completed narrow task is
not acceptance of the complete toolset. This supersedes any inference from the
11 October “MCP/CLI tools resolve offline” check that browser actions were tested.

Current readiness verdict: **NOT READY for a complete unattended assessment.**
The blind run and code tests passed within their scope; runtime failures below
remain unrepaired. No feature/package/configuration fix has been applied to the
source or baseline deployment. Audit-only containers and local fixtures were used.

| Capability | Measured state | Required action / limit |
|---|---|---|
| Linux execution | Running Linux/arm64 inside OrbStack, UID10001; not native macOS | A VPS is not required merely to obtain Linux. |
| ARTEX code suite | 540 top-level tests passed, 0 failed, 1 skipped, across 15 packages on Linux/arm64 and fresh PostgreSQL | Skip is the live context-review test requiring private model configuration; no race claim for this cross-compiled run. |
| Norma core tools | 61 top-level tests passed on Linux, no skips/failures | Covers tested file/shell/background/session/network helper paths; not every external integration. |
| Nmap TCP connect + curl | Actual loopback HTTP fixture checks passed | Core unprivileged connectivity works. |
| Nmap SYN/UDP modes | Actual loopback invocations exit1: root privileges required | Needs an explicitly scoped scanner runner with required privileges; do not mark the whole nmap tool ready for all modes. |
| ping | Execution fails: Operation not permitted with all capabilities dropped | Capability restriction, not target rejection. |
| Browser MCP | Disabled in fresh DB; manual readiness test lists25 tools, then navigation fails because Google Chrome is missing | Installed package/listed tools do not prove working browser actions. Configure installed browser and sandbox, then enable only after actual action tests. |
| Playwright CLI | Actual open fails: /opt/google/chrome/chrome missing | Same browser-channel mismatch; CLI remains failed in default configuration. |
| Bundled Chromium | Binary exists under /opt/playwright/chromium-1248/chrome-linux-arm64/chrome; explicit executable passes missing-browser stage, then sandbox startup fails | Sandbox-compatible runtime configuration required. |
| Official Playwright seccomp candidate | Container fails before app startup; local runtime log shows runc/libpathrs SIGSEGV | Environment blocker recorded; exact underlying cause unresolved. Do not attribute it conclusively to ARTEX, ARM or the VPS architecture. |
| Browser fixture diagnostic | Explicit bundled executable plus --no-sandbox passes MCP navigation, click, snapshot marker and screenshot against an offline local HTML fixture | Diagnostic only, not an approved configuration for untrusted target websites. Initial harness used obsolete click ref input; corrected to advertised target schema before counting success. |
| API-recon runtime harvest | Actual script exits1: puppeteer-core missing; configured default /usr/bin/chromium also absent | Pin/install its dependency and configure browser path; crawl/extraction not accepted end-to-end. Static-script importability is not whole-skill readiness. |
| ScopeSentry | Disabled placeholder, no service URL/API key | Unconfigured; no live synchronization claim. Existing adapter is not a running reconnaissance service. |
| Python extras | paramiko, requests, bs4, aiohttp and cryptography absent | Optional packages, not claimed dependencies of every built-in tool. Worker substituted a custom SSH parser when paramiko was absent; report the fallback explicitly. |
| External scanner additions | ssh client, nuclei, subfinder, httpx, katana, naabu, masscan, sqlmap, ffuf, tcpdump and tshark absent | Not installed in this image; do not advertise them as available tools. Select and test the needed set before additions. |
| Remote ChatGPT onboarding | Current sign-in supports native local loopback; container baseline used previously authorized encrypted credentials and renewed them | A VPS deployment needs a designed and verified callback/tunnel or credential-broker path before acceptance. Token renewal alone is not remote sign-in support. |

There are 52 ARTEX database catalog entries, mostly internal management/graph tools,
not 52 installed security scanners. `builtin-tool-coverage.csv` lists each binding
and whether the live baseline invoked it. The aggregate code-suite pass does not
turn every unexercised live workflow into a live pass. ScopeSentry, external search
with service credentials, a positive known-vulnerability detection workflow,
report-finding/retest acceptance, and all browser tool operations remain separate
acceptance requirements. No claim that “all tools work” is supported.

Additional environment failure: Docker automatic address pools were exhausted.
The offline test suite used a manually selected non-overlapping internal subnet,
10.247.220.0/24; no existing network was deleted or modified. The suite runner had
only the tracked source snapshot/test binaries and disposable DB credentials, no
model account, inventory, SSH keys or route to external targets. Browser diagnostics
used --network none and a loopback fixture. No additional rifi-dev probes occurred.

The first browser diagnostic also hit expected tool-contract limitations (snapshot
returned a file reference; screenshot outside allowed roots was rejected). The
harness was corrected to use browser_snapshot, the advertised target argument and
an allowed screenshot path. These harness errors are retained in first/second JSON
artifacts and are not misreported as product defects; the third artifact is the
successful local-only diagnostic.

Recommended execution design: a dedicated Linux runner, separate from the tested
VPS and production hosts, with a reproducible dependency image, sandboxed browser
service, narrowly privileged scanner service and model credentials outside worker
reach. Linux amd64 can simplify third-party binary availability, but hardware/OS
choice alone will not repair the demonstrated packaging, UI or export problems.
Validate the selected host with this same readiness matrix before deploying.

Proposed first repair batch (approval pending): fix browser/dependency packaging
and sandbox compatibility; expose startup/per-task preflight status for every
required tool; stop or visibly mark the task incomplete when a required capability
fails; report fallback/missing dependency explicitly; fix SSE, report artifact links
and export, metric consistency; preserve event evidence and enforce budgets/scope.
Only then run a known-positive local vulnerability fixture and a repeat external
baseline before translation. No new VPS purchase or remote server install authorized.

Evidence: tool-readiness.json, executable-inventory.txt, tool-catalog.json,
builtin-tool-coverage.csv, browser-candidate-local-only-third.json,
official-seccomp-runtime-crash.log, norma-tools-linux.log and linux-tests/summary.json
under the same private evidence directory. Sources:
https://playwright.dev/docs/docker ; https://github.com/microsoft/playwright-mcp ;
https://nmap.org/book/man-port-scanning-techniques.html .

Owner stop — 2026-10-22 10:59:09: all remaining ARTEX audit/database containers stopped; evidence and volumes retained. No further testing, source repair, purchase or deployment. Report delivered for the owner to choose next steps. Documentation remains local and uncommitted; application source remains pushed f9367f8.

### Approved repairs in progress — 22 October 2026

The owner subsequently approved the proposed repair work, superseding the stop
above. The following changes are saved locally; they are not yet committed,
pushed, deployed or accepted as a complete assessment platform.

- Production SSE uses the application origin. Task list/detail metrics share the
  same calculation. Report exports include task-specific assets, worker assessments,
  facts, evidence paths and recorded runtime failures, with explicit size limits.
  Filesystem confinement checks cover directory swaps, symlinks and blocking FIFOs.
- Node22.23.4 and Next16.4.1 are pinned; API-recon has a locked Puppeteer dependency.
  Chromium's installed path is explicit. Sandbox support uses the reviewed,
  version-pinned Moby seccomp profile. Default TLS verification remains enabled.
- Separate runtime-runner/runtime-egress commands isolate tool processes from the
  application's database and ChatGPT credentials. Tool children receive a task
  workspace, readonly tools, fresh process/mount namespaces and no capabilities.
  A fixed outbound policy pins approved destinations and keeps shared HTTP/TCP
  budgets and pacing in durable state. HTTP TLS requests are inspected and counted;
  there is no opaque tunnel or direct-fetch fallback. TCP probes use a separate
  bounded broker endpoint and cannot overlap the HTTP budget's destinations.
- Assessment admission requires real filesystem, shell, dependency, egress,
  browser CLI and browser MCP checks. Invalid/incomplete readiness cannot retain a
  previous success. Required tool errors are recorded and prevent completion until
  an operator repairs the issue and successfully resumes. Unconfigured execution
  fails closed. Explicit `ARTEX_EXECUTION_MODE=local` retains trusted local behavior
  and is clearly reported as unisolated.

Verification so far: focused HTTP/report/race checks and independent read-only
reviews passed after fixes; the entire Go suite passed with race detection against
a fresh disposable PostgreSQL database, and `go vet ./...` passed. A reused test DB
reproduced pre-existing company-attribution fixture contamination; the fresh run is
the acceptance result. Static frontend build and embedded Linux arm64 build passed.
The isolated runner's private-token setup was exercised with its restricted setup
capabilities; after dropping privileges, effective/bounding capabilities were zero
and no_new_privs was set. Earlier sandboxed Chromium interaction passed in the
candidate profile; that does not substitute for testing the final packaged runner.

Still pending: final image download/build, full container isolation/browser
acceptance, a known-positive local fixture with correct evidence/reporting, a fresh
external blind baseline, remote ChatGPT onboarding, and English translation with
separate verification. The first isolated stack intentionally binds one immutable
policy and workspace to one task. Its setup must not be represented as automatic
multi-task provisioning. Optional scanners/integrations remain unconfigured unless
their actual readiness checks pass. No new external assessment traffic has been
sent during this repair batch.

The owner selected the existing Winblue VPS at Linveo as a possible future runner.
Its runtime-only backup and off-host integrity verification are still in progress;
there has been no reinstall or ARTEX deployment on that host. Historical sessions,
trash and suppression working files are excluded from the new restore archive.

### Acceptance checkpoint — 2026-10-22 15:56:13
The final image has now built and the isolated local runner/broker/fixture stack starts. The packaged runner checks exposed Docker control-port routing and nested proc-mount settings; compose was adjusted locally. Browser trust setup then exposed NSS rejecting the broker's Ed25519 CA, while an ECDSA P-256 comparison imports successfully. Certificate compatibility repair and full acceptance remain pending. Earlier download-pending language above is superseded; the foundation is not yet accepted. No further external target traffic was sent.

The Winblue runtime backup is verified off-host at `~/.rifi-ops/SERVERS/WINBLUE_50.114.206.253/backup/runtime-2026-10-22`: 1.14GB archive, matching SHA256, full archive read, separate review and both database restores passed. Full application boot restore was not rehearsed. No Winblue reinstall or ARTEX deployment was performed. All source changes remain local and uncommitted/unpushed.
