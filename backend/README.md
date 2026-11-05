# Sub-Pool backend

Go owns authentication and PostgreSQL state. This service currently implements local administrator initialization, login, session restore/renewal/logout and password changes. The existing web UI has not yet been connected to it. Other business modules and Sub2API operations are not implemented by these endpoints.

Requires Go 1.25 or newer and PostgreSQL. Build from this directory with `go build -o sub-pool ./cmd/sub-pool` (use `sub-pool.exe` on Windows).

## Configuration and startup

Set `SUB_POOL_DATABASE_URL` to a dedicated application database connection string. Supply it through the process environment or your service secret store. Do not put credentials in arguments, source files or logs.

- `SUB_POOL_PUBLIC_ORIGIN`: exact browser origin, for example `https://pool.example.com`; no path, trailing slash, query or credentials.
- `SUB_POOL_LISTEN`: defaults to `127.0.0.1:8081`.
- `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`: explicitly permits an HTTP origin using a loopback IP for local development. Production requires an HTTPS public origin and a reverse proxy providing TLS. Arbitrary forwarded-IP headers are ignored; login source limits use the direct peer IP.

Run `sub-pool migrate` to apply embedded SQL migrations. Every command checks migrations; applied files cannot change, unknown newer database versions are rejected, and no command resets an existing database.

Initialize once with `sub-pool bootstrap <username>`, providing the newly chosen password through standard input as UTF-8 without a BOM. Use your deployment secret mechanism or raw stdin redirection; Windows PowerShell string pipelines can add a BOM and are rejected explicitly. There is no default password, public initialization endpoint or credential overwrite. Concurrent requests using different usernames still create only one initial administrator. A disabled existing administrator also prevents reinitialization.

Usernames are trimmed and ASCII-lowercased, then must match `[a-z0-9][a-z0-9._-]{2,63}`. Passwords need ten Unicode characters, an ASCII letter and digit, and at most 128 UTF-8 bytes. Spaces are significant; stdin removes one final line ending. Password hashes use salted PBKDF2-HMAC-SHA256 with 600,000 iterations. Start the service using `sub-pool serve`. Normal startup preserves identities and sessions.

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

## Tests

Run `go test ./...` and `go vet ./...`. Database tests explicitly skip unless `SUB_POOL_TEST_DATABASE_URL` points to a disposable loopback database whose name starts with `subpool_test_`; each test creates and removes its own random schema. They never reset a database. Run `bun install --frozen-lockfile`, `bun run typecheck` and `bun test` in `../contracts`. Its live oRPC test additionally requires an isolated running backend and `SUB_POOL_TEST_AUTH_ORIGIN`, `SUB_POOL_TEST_AUTH_USERNAME`, `SUB_POOL_TEST_AUTH_PASSWORD`; it changes that test administrator's password and revokes sessions. Never point it at a real account.

Deployment/TLS/proxy behavior, UI integration, distributed load and external Sub2API capabilities require their own acceptance checks.
