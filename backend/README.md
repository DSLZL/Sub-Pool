# Sub-Pool backend

Go owns authentication and PostgreSQL state. This service implements local administrator initialization, login, session restore/renewal/logout, password changes, ordinary-user/permission management, single-instance Sub2API connection configuration, read-only diagnostics/account discovery, local managed accounts, safe runtime/model reads, timezone settings, login/source controls, a management journal and durable passive quota collection. The existing web login, profile, user-management and connection/configuration pages use a fixed same-origin Next.js proxy. Account-detail/collection controls, security-page integration and other business modules remain pending.

Requires Go 1.25 or newer and PostgreSQL. Build from this directory with `go build -o sub-pool ./cmd/sub-pool` (use `sub-pool.exe` on Windows).

## Configuration and startup

Set `SUB_POOL_DATABASE_URL` to a dedicated application database connection string. Supply it through the process environment or your service secret store. Do not put credentials in arguments, source files or logs.

- `SUB_POOL_PUBLIC_ORIGIN`: exact browser origin, for example `https://pool.example.com`; no path, trailing slash, query or credentials.
- `SUB_POOL_LISTEN`: defaults to `127.0.0.1:8081`.
- `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`: explicitly permits an HTTP origin using a loopback IP for local development. Production requires an HTTPS public origin and a reverse proxy providing TLS.
- `SUB_POOL_TRUSTED_PROXY_CIDRS`: optional comma-separated trusted proxy prefixes (at most 32). Empty trusts no forwarded headers. Only a trusted direct peer may supply one valid, bounded `X-Forwarded-For` chain; the service walks it from the right through trusted proxies. Invalid or ambiguous chains fall back to the direct peer. Configure only proxies that sanitize this header and restrict direct access to the backend. The current Next proxy strips forwarding headers, so Go observes its peer address; configuring this variable alone does not identify the browser's IP. Deployment forwarding behavior needs separate verification.
- `SUB_POOL_SECRET_KEY`: base64 encoding of a cryptographically random 32-byte deployment secret, used for AES-256-GCM encryption of upstream management credentials. Keep the same protected key across restarts. Without it, local authentication and configuration metadata remain usable, but credentials cannot be saved or decrypted. Replacing it cannot decrypt existing credentials; deliberately replace or clear those credentials before using a different key. Do not put the key in source, browser configuration or logs.

Run `sub-pool migrate` to apply embedded SQL migrations. Every command checks migrations; applied files cannot change, unknown newer database versions are rejected, and no command resets an existing database.

Initialize once with `sub-pool bootstrap <username>`, providing the newly chosen password through standard input as UTF-8 without a BOM. Use your deployment secret mechanism or raw stdin redirection; Windows PowerShell string pipelines can add a BOM and are rejected explicitly. There is no default password, public initialization endpoint or credential overwrite. Concurrent requests using different usernames still create only one initial administrator. A disabled existing administrator also prevents reinitialization.

Usernames are trimmed and ASCII-lowercased, then must match `[a-z0-9][a-z0-9._-]{2,63}`. Passwords need ten Unicode characters, an ASCII letter and digit, and at most 128 UTF-8 bytes. Spaces are significant; stdin removes one final line ending. Password hashes use salted PBKDF2-HMAC-SHA256 with 600,000 iterations. Start the service using `sub-pool serve`. Normal startup preserves identities and sessions.

## Web application

Install dependencies once from the repository root with `bun install --frozen-lockfile`. The root workspace contains `web` and the shared `contracts` package; keep the root `bun.lock` as the single lockfile. Run `bun run build` and `bun run start` from `web`.

The Next process needs the same `SUB_POOL_PUBLIC_ORIGIN` as Go, plus `SUB_POOL_BACKEND_ORIGIN` pointing to the fixed Go service origin. For local HTTP use a loopback IP origin and explicitly set `SUB_POOL_ALLOW_LOOPBACK_HTTP=true` on both processes. For example, serve Next on `http://127.0.0.1:3000` and Go on `http://127.0.0.1:8081`. Keep backend origin/configuration server-side. Production needs HTTPS and a deployment-specific reverse proxy check.

The browser holds only an HttpOnly session cookie; local storage preferences cannot grant roles. Protected pages wait for matching current session and access responses. Identity or permission changes clear prior page state; focus and foreground activity recheck the server. Logout revokes the current session. Password changes revoke all sessions and require login again. Requests with uncertain outcomes are not automatically replayed. Business pages remain unavailable until their real modules are connected.

## Authentication API

The versioned oRPC/Zod contract is in `../contracts/auth.ts`. Browser mutations require the exact configured Origin and `application/json`; undeclared/duplicate fields, malformed Unicode, unexpected queries and oversized bodies are rejected. Cookies are HttpOnly, host-only and SameSite=Lax; HTTPS uses a Secure `__Host-sub_pool_session` cookie. Explicit HTTP development uses `sub_pool_dev_session`.

| Endpoint | Input | Result |
| --- | --- | --- |
| POST `/api/v1/auth/login` | `username`, `password` | Current identity and deadlines; sets cookie |
| GET `/api/v1/auth/session` | No query/body | Current stored session; does not extend it |
| POST `/api/v1/auth/renew` | `{}` | Extends idle deadline, bounded by absolute expiry |
| POST `/api/v1/auth/logout` | `{}` | Durable revocation and cookie removal; idempotent |
| POST `/api/v1/auth/password` | `current_password`, `new_password` | Atomic new hash and revocation of all sessions, including caller |

Idle lifetime is eight hours; absolute lifetime is seven days. Disabled identities, revoked sessions and expired sessions are denied using current database state. Login failure limits persist for fifteen minutes: five failures per normalized identifier and twenty-five per server-observed source IP, following the configured trust policy. Responses never include session tokens, hashes or database errors. A network failure after a mutation leaves its outcome unknown; clients must not automatically replay it. Restore/read current state or sign in explicitly as appropriate.

## Users and read permissions

The separate `users-v1` contract is in `../contracts/users.ts`; `createUsersClient` shares the same validated, fixed-origin transport as the auth client. The existing `/users` page uses these endpoints through the Next.js proxy. It reads every backend page before displaying a complete list; a failed page leaves the list unavailable. Create/save and confirmed deletion wait for actual server results. Conflicts keep the dialog open; uncertain writes block repeat submission until a deliberate read, with feedback retained during recovery.

New users start without business grants. The existing scope selectors are not yet connected to the managed-account catalog; the participant catalog remains pending. They show unavailable state and preserve stored scope IDs when saving other settings. New scope selection, personal API Keys and authorization inside future business modules remain pending their respective implementations.

| Endpoint | Input | Result |
| --- | --- | --- |
| GET `/api/v1/access` | No query/body | Own current identity, read grants and independent account/participant scopes |
| POST `/api/v1/users/list` | `after` (empty initially), `search` (username prefix), `limit` (1–100) | Ordinary-user summaries and nullable next cursor |
| POST `/api/v1/users/get` | `id` | Ordinary identity, enabled state, version and grants |
| POST `/api/v1/users/create` | `username`, `password`, `enabled`, `features`, `account_ids`, `participant_ids` | Ordinary identity and its initial grants saved atomically |
| POST `/api/v1/users/update` | `id`, `revision`, `username`, `enabled`, `features`, `account_ids`, `participant_ids`, `new_password` (empty means unchanged) | Identity/grants and any credential reset saved atomically with one new revision |
| POST `/api/v1/users/delete` | `id`, `revision`, `confirmation` (exact current username) | Login identity removed; target sessions revoked |

All `users/*` actions require a current administrator; they cannot create, promote, edit, reset or delete an administrator. Self access requires a live authenticated session. Every POST requires the exact Origin and JSON; the user boundary rejects duplicate, missing, extra, null or incorrectly typed fields and bodies over 64 KiB. A stale revision returns a defined 409 conflict; read current state before making a deliberate new edit. Failed transport does not establish whether a write committed.

Read features are `overview`, `accounts`, `pools`, `usage`, `observations`, `estimates`, `balances`, `temporary`, `pricing`, `dispatch`, `notifications` and `security`. Each feature is granted at most once. The explicit `security` grant allows global login-audit and source-rule reads; account/participant scopes do not narrow those records. It grants no management-audit read or configuration writes. Account and participant scopes each contain at most 500 unique canonical local IDs (16 random bytes encoded as 22 unpadded base64url characters). They are independent; an unknown ID matches no business facts. Configuration does not prove that an upstream resource exists or a domain capability is available. Profile/password/logout remain personal operations; global settings and management writes are administrator only.

Create/save includes enabled state, grants and credentials in one transaction. Empty initial arrays grant no business access. Lists return grant/scope counts and the latest recorded successful local login time (null when none exists), without returning every user's scope IDs. Permission edits increment the stored revision; new requests read current grants. Disable/reset/delete revoke all sessions atomically with the identity change and actor/target audit record. Re-enabling never resurrects revoked sessions. Deletion keeps a disabled tombstone with no password or grants, reserves the username to avoid historical ambiguity, and preserves audit and other business references. It does not remove upstream users, pool participants or their history.

Domain handlers must retain their PostgreSQL transaction through `AuthorizeRead` or `AuthorizeAdmin` and the protected operation. Use `Access.ScopeSQL` with structured `pgx.Identifier` column names and the first bind position to filter SQL before details, search, grouping, pagination or notifications; attribution facts require both scopes and must exclude unknown attribution for ordinary users. Detail checks return the same not-found result for absent and out-of-scope identities. Future domain endpoints and personal API Key authentication must use these guards and current enabled/revision state; their integration is not implied by a grant or by these endpoint tests.

## Sub2API connection and timezone

The `connection-v1` contract is in `../contracts/connection.ts`; `createConnectionClient` uses the same fixed-origin, validated transport. These endpoints reuse the strict JSON/Origin/cookie boundary and current transactional authorization. Connection metadata, diagnostics and discovery require an administrator. Settings reads require a live session; settings writes require an administrator.

The existing `/connection` page reads saved configuration, independent capability checks, complete discovery and managed-account lists, monitoring policy and persisted run counts. It supports draft tests, explicit credential replacement/clearing, confirmed target changes, adoption with a local name, per-account monitoring/source settings and timezone updates. Server acknowledgements determine saved state. A failed page exposes no partial discovery; conflicts and uncertain writes require a deliberate read before another save. The shared sidebar reads the saved timezone. Newly entered management credentials stay in the form and are cleared on save submission, leaving the tab, reloading or identity invalidation; saved secrets never return to the browser.

The connection page preserves policy fields that have no existing controls. It displays the latest recorded run counts and the worker heartbeat separately; the backend does not provide a global next-run time. Local name editing after adoption still needs an existing control. Manual collection is available on the accounts page. Active quota execution remains unavailable as described below.

| Endpoint | Input | Result |
| --- | --- | --- |
| GET `/api/v1/connection` | No query/body | Current revision/source, address, credential-presence flag and saved diagnostic; never a credential |
| POST `/api/v1/connection/save` | `revision`, `base_url`, `credential_action` (`keep`, `replace`, `clear`), `credential`, `confirmation` | Atomically saved configuration and whether its source changed |
| POST `/api/v1/connection/test` | `revision`, `base_url`, `credential` (empty uses the saved credential only for the saved address) | Independent health, authentication, version, mode and capability checks |
| POST `/api/v1/connection/discover` | `revision`, `source_id`, `page`, `search` | At most 100 safe account summaries, nullable total and explicit next page |
| GET `/api/v1/settings` | No query/body | Timezone and settings revision |
| POST `/api/v1/settings/update` | `revision`, `timezone` | Saved IANA timezone; facts and upstream reset times stay unchanged |

Addresses require HTTPS; the development flag permits HTTP only for a loopback IP. A base path is supported; credentials, query strings and fragments in addresses are rejected. Supply an opaque management API key, or explicitly prefix a JWT with `Bearer `. The service sends only its selected authentication header and never forwards browser cookies. Upstream requests use fixed GET paths, bounded responses/timeouts and no redirects or automatic retries. There are no upstream mutations in this module.

Save does not test the connection. A draft test neither saves its address/credential nor replaces the saved diagnostic. A saved test binds its result to the exact current revision. Saving invalidates the previous diagnostic. Changing an existing address requires `confirmation` equal to `更换地址` and either a newly supplied credential or explicit clearing; the old credential cannot follow it. Every address change creates a fresh source ID, including a return to a former address. History retains older source IDs. Future mappings and jobs must bind their source and revision, and must never replay against a new target.

Health, management authentication, run mode and each capability are separate. An API key cannot establish run mode through the JWT-only profile route. Empty scopes, denied/unsupported routes and malformed responses remain distinct from valid zero values. Pricing reads, quota/resource checks and wallet/scheduling/pricing writes remain unverified until their own evidence; a readable list or `standard` mode cannot authorize them. Discovery projects safe account metadata and exact decimal-string IDs, discards upstream credentials/errors, reports partial coverage explicitly and never adopts or deletes accounts. OpenAI OAuth identity remains unverified because restricted lists do not establish shadow/resource identity.

## Managed accounts and safe observations

The `accounts-v1` contract is in `../contracts/accounts.ts`; `createAccountsClient` shares the validated fixed-origin transport. Every route below uses POST with the strict JSON/Origin/cookie boundary. Adoption, local editing and model reads require a current administrator. List, detail and explicit runtime refresh require the `accounts` read feature and the current local account scope; administrators can read all accounts. Scope filtering happens in PostgreSQL before search and pagination. An absent or out-of-scope detail returns the same 404 without contacting the upstream.

| Endpoint | Input | Result |
| --- | --- | --- |
| `/api/v1/accounts/list` | `after` (empty initially), `search` (literal name substring), `limit` (1–100) | Visible managed accounts and nullable next cursor |
| `/api/v1/accounts/get` | `id` | Local configuration and last safe observation |
| `/api/v1/accounts/adopt` | `source_id`, `connection_revision`, `upstream_id`, `name`, `monitoring`, `source_mode` (`active` or `passive`) | Verified detail read and new local identity; unavailable reads return a safe check with no account |
| `/api/v1/accounts/update` | `id`, `revision`, `name`, `monitoring`, `source_mode` | Persisted local configuration with a new revision |
| `/api/v1/accounts/refresh` | `id`, `revision`, `source_id`, `connection_revision` | Safe runtime read, attempt time and last successful observation time |
| `/api/v1/accounts/models` | `id`, `revision`, `source_id`, `connection_revision` | Readable mapping/passthrough configuration and advisory native model catalog |

Adoption reads the exact upstream identity before creating a random local ID. Source/upstream bindings are immutable. A private fingerprint prevents duplicate OpenAI root resources within a source; shadow accounts never represent independent quota. Restricted or unsupported identities remain explicitly unverified or unsupported. A changed resource identity is quarantined as conflict and cannot silently rebind history. Changing the connection address isolates older accounts, even when returning to a former address. Their history remains readable within the caller's scope.

Local pause, resume, rename and source-mode edits never change upstream scheduling or delete observations. `paused` excludes normal collection. The accounts-v1 `collection_state` is a prerequisite projection; `awaiting_collector` does not describe a live job. Use monitoring-v1 for durable quota jobs and snapshots. The account module's explicit runtime/model reads do not query quota, recover wallets, clear errors or write models/scheduling. Pausing cannot reclaim an upstream balance.

`connection_available` reports configured credential/storage presence, not tested authentication. Each successful snapshot records `observed_connection_revision`. Clearing credentials or losing the storage key produces `connection_unavailable`; replacing configuration at the same address produces `connection_changed` until a successful refresh. Failed refreshes update the safe check and attempt time while preserving the last successful snapshot, its time and its connection revision. Missing runtime fields stay null, real false/zero values remain real, and upstream status is separate from local collection state. All timestamps are UTC. Raw credentials, resource IDs and upstream error text are excluded; only error presence is returned.

Model mappings are configuration, not proof of access or reliable blocking. Missing mapping/passthrough remains unknown; an explicitly empty mapping is not all-disabled. The native catalog may be a fallback, so `access_proven` is always false. Model reads do not change configuration, synchronize models, query quota or refresh tokens. Local writes and audit records commit atomically, use optimistic revisions and recheck authority after waits. Uncertain writes are never automatically replayed.

The existing `/accounts` page and overview read complete, currently scoped account metadata and passive observations through the validated contracts. Cards, tables and drawers keep source measurement, snapshot receipt, metadata observation and latest collection attempt separate. Missing windows and unavailable finance/participant/temporary data stay unknown. Administrators can read advisory model mappings and queue a single-account or collect-all run; ordinary readers receive only their permitted accounts and no global run/model controls. Account grants also populate the existing user-management selector.

Manual collection acknowledges a durable run, then reads its seven possible per-account outcomes until completion. A completed run can contain failures or skips. History and explicit recovery only read stored results; an uncertain submission stays blocked from automatic replay even after a successful read. Process liveness, worker heartbeat, enabled policy and account collection results remain separate health signals. Identity changes invalidate the page state and pending reads. The shared frontend proxy exposes only exact `GET /health` process output, without database or business readiness claims.

## Passive quota monitoring and durable jobs

The `monitoring-v1` contract is in `../contracts/monitoring.ts`; `createMonitoringClient` uses the same validated fixed-origin transport. The service's `serve` process runs the collector independently of browser lifetime. Global monitoring starts disabled. No additional environment variable enables it; an administrator saves the persisted policy.

| Endpoint | Input | Result and authority |
| --- | --- | --- |
| GET `/health` | No query/body | Public process liveness only |
| GET `/api/v1/monitoring/config` | No query/body | Current policy and revision; administrator |
| POST `/api/v1/monitoring/config/update` | `revision`, `enabled`, `interval_seconds`, `stale_after_seconds`, `refresh_on_change`, `near_reset_enabled`, `near_reset_lead_seconds`, `near_reset_interval_seconds`, `stale_warnings_enabled` | Optimistic persisted policy; administrator |
| GET `/api/v1/monitoring/status` | No query/body | Enabled state, worker heartbeat and explicit active-query gate; administrator |
| POST `/api/v1/monitoring/collect` | `source_id`, `connection_revision`, `all`, `account_ids` | Durable run with per-state counts; administrator |
| POST `/api/v1/monitoring/runs/list` | `after` (`0` initially), `limit` (1–100) | Persisted runs and nullable next cursor; administrator |
| POST `/api/v1/monitoring/runs/get` | `id`, `after` (`0` initially), `limit` (1–100) | Run counts and paged per-account jobs; administrator |
| POST `/api/v1/monitoring/accounts/get` | `id` | Last successful quota snapshot, latest job, freshness and next schedule; current `accounts` read grant and account scope |

The shared strict JSON/Origin/cookie/source boundary protects every private endpoint. A selected collection requires 1–500 unique local account IDs; `all=true` requires an empty array. A selected one-off read is allowed while automatic monitoring or that account is paused; collect-all skips paused accounts. Scope denial returns the same 404 as an absent account and never dispatches an upstream read. Ordinary readers cannot queue work or read global runs/policy.

Policy defaults are a 300-second interval, 1800-second stale threshold, refresh after local configuration changes on, near-reset refresh off (600-second lead, 60-second interval), and stale warnings on. Intervals are 60–86400 seconds; stale threshold must be at least the normal interval and at most seven days; near-reset interval cannot exceed the normal interval. The lead is 60–86400 seconds. The warning preference describes returned freshness; it does not send notifications.

Passive collection reads only the fixed upstream account-detail GET for a verified OpenAI OAuth root resource. It revalidates the private resource fingerprint before accepting raw `codex_5h_*` and `codex_7d_*` extra fields. Shadow, unverified and changed identities cannot become independent quota; changed identities are quarantined. It does not call generic usage, the Anthropic-only passive usage path, quota refresh/reset, or any upstream mutation.

Active collection is unavailable with `ACTIVE_QUERY_SIDE_EFFECT`. In the inspected [Sub2API revision](https://github.com/Wei-Shaw/sub2api/blob/d851a38477597157e78450d30af2d8504764d085/backend/internal/handler/admin/openai_oauth_handler.go#L477), the OpenAI quota GET notifies an automatic credit-reset service, so issuing it could trigger a reset. Active-mode jobs are explicitly skipped without contacting that route, and automatic scheduling excludes them. Selecting active mode records intent only. A reset-free upstream capability still needs independent verification before active dispatch can be implemented.

Snapshots retain actual `codex_usage_updated_at` as nullable `source_at`, alongside local `received_at`. Retrieval time never substitutes for source measurement time. Percentages remain exact numeric strings and keep their units; duration uses seconds only when the upstream supplies its window length. Missing fields remain null, and a missing window never becomes zero. A relative reset is anchored only to a real source timestamp. An elapsed reset sets `reset_expired` while preserving recorded usage.

Freshness is `missing`, `unknown`, `future`, `fresh`, `stale`, or an explicit connection/source/identity/mode invalidation. A failed attempt leaves the last successful snapshot intact; inspect `latest_attempt` separately. All times are UTC. The last snapshot and its binding remain historical after configuration changes; they cannot establish current capability.

Job/run IDs are returned as decimal strings, but queue order, history cursors and the latest attempt use their numeric PostgreSQL columns. This preserves ordering when IDs cross a digit boundary.

Jobs bind source, connection, account and policy revisions before dispatch. One queued/running job per account is enforced by PostgreSQL. Manual runs privately retain the issuing session's SHA-256 digest and actor revision; workers recheck the live, enabled administrator session before dispatch and after the read. Raw cookies and private digests never appear in job responses. Logout, expiry and revoked authority prevent protected observations from being persisted. Scheduled runs use the collector task and current policy/account state.

A dedicated database session owns the single-worker advisory lock. A replacement marks abandoned running jobs `interrupted` with an unknown audit outcome, preserves committed successes and processes queued work. Jobs distinguish `queued`, `running`, `succeeded`, `failed`, `skipped`, `conflict` and `interrupted`; a completed run may contain several outcomes. Intent/result records share an operation ID and commit atomically with their local state. Unknown outcomes never establish success or authorize replay.

Normal/change/near-reset preferences use persisted attempt and actual reset times. Both manual and scheduled reads obey minimum 60-second spacing and persisted Retry-After cooldowns. There is no immediate retry or cooldown bypass. Pausing stops normal automatic scheduling and preserves facts; future temporary-resource recovery obligations remain separately unimplemented.

`/health` says only that the HTTP process responds. Private `worker_recent` describes a heartbeat within 15 seconds; it does not prove successful monitoring, database readiness over time, upstream availability or active quota capability. Inspect durable per-account results for business health.

## Login security and management audit

The `security-v1` contract is in `../contracts/security.ts`; `createSecurityClient` uses the same validated transport. All endpoints use the shared strict boundary. Source rules apply to new requests across authentication and current domain APIs, including existing sessions; `SOURCE_BLOCKED` is a defined 403. Already executing non-login requests are not retroactively cancelled. Login checks source rules again inside its session-issuing transaction.

| Endpoint | Input | Result and authority |
| --- | --- | --- |
| GET `/api/v1/security/public` | No query/body | Diagnostic-enabled flag only; no session required |
| GET `/api/v1/security/config` | No query/body | Current diagnostic configuration; administrator |
| POST `/api/v1/security/config/update` | `revision`, `network_diagnostics_enabled` | Optimistic configuration save; administrator |
| POST `/api/v1/security/network/compare` | `reported_ip`, `consent` (true) | Untrusted comparison with server source; authenticated session and enabled diagnostics |
| POST `/api/v1/security/sources/list` | `after` (empty initially), `state` (`all`, `blocked`, `unblocked`), `limit` (1–100) | Exact-address rules; administrator or explicit `security` read grant |
| POST `/api/v1/security/sources/update` | `ip`, `revision` (`0` when absent), `blocked` | Persisted block/unblock; administrator |
| POST `/api/v1/security/login-audit` | `from`, `until`, `after` (`0` initially), `limit` (1–100), `outcome` | Login events and counts; administrator or explicit `security` read grant |
| POST `/api/v1/security/management-audit` | `from`, `until`, `after` (`0` initially), `limit` (1–100) | Safe operation metadata; administrator |

Rules use canonical individual IPv4/IPv6 addresses, not ranges. Both the current administrator's direct peer and effective source are protected against self-blocking. Unknown attribution cannot authorize a new block. A stale revision returns 409; unblock increments the revision and preserves history. IPv6 outputs are normalized consistently with request validation, and source pagination follows database address order. Rules survive process replacement.

Login audit distinguishes server-observed peer/effective source, explicitly trusted forwarding and legacy/unknown records. It records validated credential outcomes, persisted throttling and source-blocked attempts; malformed HTTP requests and transient work-budget rejection are not a complete packet log. Time ranges are UTC `[from, until)`, at most 31 days. Event IDs, revisions and counts are exact decimal strings. Counts cover the requested time/outcome filter independently of the page cursor; they are not a frozen cross-page snapshot. `outcome` is `all`, `success`, `failure`, `limited` or `blocked`. New requests recheck current read grants.

Optional network diagnostics default off. An authenticated user may submit an address with explicit consent; the response always marks it untrusted. The hint is not stored or used for authentication, throttling or source rules. No external discovery or STUN request occurs. Disabled diagnostics and declined consent do not prevent ordinary login; browser diagnostic integration remains pending.

The management journal retains actor or system-task identity, operation correlation, action, target/source/revision and an explicit outcome. Existing auth/user/connection/account event helpers append to it in the same transaction; a journal failure rolls back the local change. Historical tables remain intact and their safe metadata is backfilled once. No passwords, tokens, credentials, arbitrary request payloads or upstream error bodies are stored. For existing connection tests and account refreshes, `succeeded` means the local result was recorded successfully; inspect the domain's capability/result fields for upstream availability. It does not prove a successful remote operation.

`audit.Record(ctx, tx, Entry)` supports `pending`, `succeeded`, `failed`, `unknown`, `denied` and `conflict` with a shared operation ID. Future remote-write/recovery modules must persist their actual intent, domain state and observed result; metadata alone cannot restore prior grants, balances or remote state. Those integrations and the security page remain separately unaccepted. Unknown writes must never be automatically replayed.

## Tests

Run `go test ./...` and `go vet ./...`. Database tests explicitly skip unless `SUB_POOL_TEST_DATABASE_URL` points to a disposable loopback database whose name starts with `subpool_test_`; each test creates and removes its own random schema. They never reset a database. After the root workspace install, run `bun run typecheck` and `bun test` in `../contracts` and `../web`; also run `bun run build` in `../web`. Its live oRPC test additionally requires an isolated running backend and `SUB_POOL_TEST_AUTH_ORIGIN`, `SUB_POOL_TEST_AUTH_USERNAME`, `SUB_POOL_TEST_AUTH_PASSWORD`; it changes that test administrator's password and revokes sessions. The separate live users test uses `SUB_POOL_TEST_USERS_ORIGIN`, `SUB_POOL_TEST_USERS_USERNAME`, `SUB_POOL_TEST_USERS_PASSWORD`; it creates, edits, resets, disables and tombstones an ordinary identity. Both live tests require explicit isolated fixtures. Never point them at real accounts.

The live connection contract test uses `SUB_POOL_TEST_CONNECTION_ORIGIN`, `SUB_POOL_TEST_CONNECTION_USERNAME`, `SUB_POOL_TEST_CONNECTION_PASSWORD` against an isolated backend with loopback HTTP enabled and a secret-storage key. It replaces/clears that fixture's connection and updates its timezone, using an owned local read-only upstream server. Never use a deployment containing real connection configuration. Missing live-test variables produce explicit skips; static passes do not establish native or external Sub2API acceptance.

The live managed-account contract test uses `SUB_POOL_TEST_ACCOUNTS_ORIGIN`, `SUB_POOL_TEST_ACCOUNTS_USERNAME`, `SUB_POOL_TEST_ACCOUNTS_PASSWORD` against its own isolated backend. It changes fixture connection credentials, adopts controlled upstream identities and creates a scoped ordinary user. It requires loopback HTTP and a secret-storage key, and must never target real deployment data. Its upstream server accepts only reads; it does not establish external Sub2API capability or account-page acceptance.

The live security contract test uses `SUB_POOL_TEST_SECURITY_ORIGIN`, `SUB_POOL_TEST_SECURITY_USERNAME`, `SUB_POOL_TEST_SECURITY_PASSWORD` against a dedicated disposable backend with loopback HTTP enabled and `SUB_POOL_TRUSTED_PROXY_CIDRS=127.0.0.1/32`. It sends controlled forwarded addresses, changes diagnostic settings, blocks/unblocks fixture sources and creates/changes an ordinary user's security grant. Never run it against a real deployment. Source controls, audit migrations/rollback and actual lock-wait expiry also have native PostgreSQL tests.

The live monitoring contract test uses `SUB_POOL_TEST_MONITORING_ORIGIN`, `SUB_POOL_TEST_MONITORING_USERNAME`, `SUB_POOL_TEST_MONITORING_PASSWORD` against its own disposable backend with loopback HTTP and a secret-storage key. It changes fixture connection configuration, adopts passive/active test identities, exercises the real background worker and creates/revokes a scoped reader. Its local upstream accepts only fixed account-detail GETs. It does not establish external Sub2API capability or monitoring-page acceptance.

Deployment/TLS/proxy behavior, distributed load, other business modules and external Sub2API capabilities require their own acceptance checks.
