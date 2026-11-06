# Sub-Pool backend

Go owns authentication and PostgreSQL state. This service implements local administrator initialization, login, session restore/renewal/logout, password changes, ordinary-user/permission management, single-instance Sub2API connection configuration, read-only diagnostics/account discovery and timezone settings. The existing web login, profile and user-management forms use a fixed same-origin Next.js proxy. Connection-page integration and other business modules remain pending.

Requires Go 1.25 or newer and PostgreSQL. Build from this directory with `go build -o sub-pool ./cmd/sub-pool` (use `sub-pool.exe` on Windows).

## Configuration and startup

Set `SUB_POOL_DATABASE_URL` to a dedicated application database connection string. Supply it through the process environment or your service secret store. Do not put credentials in arguments, source files or logs.

- `SUB_POOL_PUBLIC_ORIGIN`: exact browser origin, for example `https://pool.example.com`; no path, trailing slash, query or credentials.
- `SUB_POOL_LISTEN`: defaults to `127.0.0.1:8081`.
- `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`: explicitly permits an HTTP origin using a loopback IP for local development. Production requires an HTTPS public origin and a reverse proxy providing TLS. Arbitrary forwarded-IP headers are ignored; login source limits use the direct peer IP.
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

Idle lifetime is eight hours; absolute lifetime is seven days. Disabled identities, revoked sessions and expired sessions are denied using current database state. Login failure limits persist for fifteen minutes: five failures per normalized identifier and twenty-five per direct source IP. Responses never include session tokens, hashes or database errors. A network failure after a mutation leaves its outcome unknown; clients must not automatically replay it. Restore/read current state or sign in explicitly as appropriate.

## Users and read permissions

The separate `users-v1` contract is in `../contracts/users.ts`; `createUsersClient` shares the same validated, fixed-origin transport as the auth client. The existing `/users` page uses these endpoints through the Next.js proxy. It reads every backend page before displaying a complete list; a failed page leaves the list unavailable. Create/save and confirmed deletion wait for actual server results. Conflicts keep the dialog open; uncertain writes block repeat submission until a deliberate read, with feedback retained during recovery.

New users start without business grants. Account and participant catalogs are not implemented yet, so the existing scope selectors show unavailable state and preserve stored scope IDs when saving other settings. New scope selection, personal API Keys and authorization inside future business modules remain pending their respective implementations.

| Endpoint | Input | Result |
| --- | --- | --- |
| GET `/api/v1/access` | No query/body | Own current identity, read grants and independent account/participant scopes |
| POST `/api/v1/users/list` | `after` (empty initially), `search` (username prefix), `limit` (1–100) | Ordinary-user summaries and nullable next cursor |
| POST `/api/v1/users/get` | `id` | Ordinary identity, enabled state, version and grants |
| POST `/api/v1/users/create` | `username`, `password`, `enabled`, `features`, `account_ids`, `participant_ids` | Ordinary identity and its initial grants saved atomically |
| POST `/api/v1/users/update` | `id`, `revision`, `username`, `enabled`, `features`, `account_ids`, `participant_ids`, `new_password` (empty means unchanged) | Identity/grants and any credential reset saved atomically with one new revision |
| POST `/api/v1/users/delete` | `id`, `revision`, `confirmation` (exact current username) | Login identity removed; target sessions revoked |

All `users/*` actions require a current administrator; they cannot create, promote, edit, reset or delete an administrator. Self access requires a live authenticated session. Every POST requires the exact Origin and JSON; the user boundary rejects duplicate, missing, extra, null or incorrectly typed fields and bodies over 64 KiB. A stale revision returns a defined 409 conflict; read current state before making a deliberate new edit. Failed transport does not establish whether a write committed.

Read features are `overview`, `accounts`, `pools`, `usage`, `observations`, `estimates`, `balances`, `temporary`, `pricing`, `dispatch` and `notifications`. Each feature is granted at most once. Account and participant scopes each contain at most 500 unique canonical local IDs (16 random bytes encoded as 22 unpadded base64url characters). They are independent; an unknown ID matches no business facts. Configuration does not prove that an upstream resource exists or a domain capability is available. Profile/password/logout remain personal operations; global settings and management writes are administrator only.

Create/save includes enabled state, grants and credentials in one transaction. Empty initial arrays grant no business access. Lists return grant/scope counts and the latest recorded successful local login time (null when none exists), without returning every user's scope IDs. Permission edits increment the stored revision; new requests read current grants. Disable/reset/delete revoke all sessions atomically with the identity change and actor/target audit record. Re-enabling never resurrects revoked sessions. Deletion keeps a disabled tombstone with no password or grants, reserves the username to avoid historical ambiguity, and preserves audit and other business references. It does not remove upstream users, pool participants or their history.

Domain handlers must retain their PostgreSQL transaction through `AuthorizeRead` or `AuthorizeAdmin` and the protected operation. Use `Access.ScopeSQL` with structured `pgx.Identifier` column names and the first bind position to filter SQL before details, search, grouping, pagination or notifications; attribution facts require both scopes and must exclude unknown attribution for ordinary users. Detail checks return the same not-found result for absent and out-of-scope identities. Future domain endpoints and personal API Key authentication must use these guards and current enabled/revision state; their integration is not implied by a grant or by these endpoint tests.

## Sub2API connection and timezone

The `connection-v1` contract is in `../contracts/connection.ts`; `createConnectionClient` uses the same fixed-origin, validated transport. These endpoints reuse the strict JSON/Origin/cookie boundary and current transactional authorization. Connection metadata, diagnostics and discovery require an administrator. Settings reads require a live session; settings writes require an administrator.

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

## Tests

Run `go test ./...` and `go vet ./...`. Database tests explicitly skip unless `SUB_POOL_TEST_DATABASE_URL` points to a disposable loopback database whose name starts with `subpool_test_`; each test creates and removes its own random schema. They never reset a database. After the root workspace install, run `bun run typecheck` and `bun test` in `../contracts` and `../web`; also run `bun run build` in `../web`. Its live oRPC test additionally requires an isolated running backend and `SUB_POOL_TEST_AUTH_ORIGIN`, `SUB_POOL_TEST_AUTH_USERNAME`, `SUB_POOL_TEST_AUTH_PASSWORD`; it changes that test administrator's password and revokes sessions. The separate live users test uses `SUB_POOL_TEST_USERS_ORIGIN`, `SUB_POOL_TEST_USERS_USERNAME`, `SUB_POOL_TEST_USERS_PASSWORD`; it creates, edits, resets, disables and tombstones an ordinary identity. Both live tests require explicit isolated fixtures. Never point them at real accounts.

The live connection contract test uses `SUB_POOL_TEST_CONNECTION_ORIGIN`, `SUB_POOL_TEST_CONNECTION_USERNAME`, `SUB_POOL_TEST_CONNECTION_PASSWORD` against an isolated backend with loopback HTTP enabled and a secret-storage key. It replaces/clears that fixture's connection and updates its timezone, using an owned local read-only upstream server. Never use a deployment containing real connection configuration. Missing live-test variables produce explicit skips; static passes do not establish native or external Sub2API acceptance.

Deployment/TLS/proxy behavior, distributed load, other business modules and external Sub2API capabilities require their own acceptance checks.
