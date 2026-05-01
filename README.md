# awg-rest - Production AmneziaWG V2 Control Plane

A declarative, idempotent, multi-tenant control plane for [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go)
V2, written in Go. Implements a backend-only control-plane pipeline:
**REST API → Postgres (source of truth) → durable outbox → reconciler → node-agent → `awg`/kernel module.**

## Highlights

- **AmneziaWG V2 native**: full support for `Jc/Jmin/Jmax`, `S1-S4`, `H1-H4`, and `I1-I5` parameters
  (incl. ranges) with versioned protocol profiles. Compatible with `amneziawg-go` and the kernel module.
- **Postgres source of truth**: peers, profiles, IPAM, operations, outbox, audit, idempotency keys.
- **Idempotent REST API** with `Idempotency-Key` (request-hash dedup, 409 on conflict, replay returns
  original response). RFC 9110 / RFC 6585 compliant; `429 + Retry-After` on throttling.
- **Outbox + reconciler** (`FOR UPDATE SKIP LOCKED`) so applying never races, and `awg syncconf`
  is non-disruptive. Drift detection via `awg show <iface> dump`.
- **Three-binary deployment**: `awg-api`, `awg-worker`, and `awg-node-agent`.
- **Strict JWT validation**: dev HMAC or production asymmetric PEM (`RS256`/`ES256`/`EdDSA`),
  `alg` allowlist, `iss`/`aud`/`exp`/`typ`, tenant-scoped RBAC, and mTLS for worker-to-agent.
- **Test pyramid**: unit (validation, IPAM, renderer, parsers, crypto), integration (Postgres via
  Testcontainers), e2e (full peer lifecycle with a fake AWG executor; tagged build for real-AWG
  CI on Linux).

## Layout

```
cmd/
  awg-api/                   # control plane HTTP API
  awg-worker/                # outbox/reconcile worker
  awg-node-agent/            # node-local apply agent (Linux)
internal/
  api/                       # HTTP handlers, middleware, problem+json
  auth/                      # JWT + RBAC + mTLS helpers
  awg/                       # AmneziaWG executor (real CLI + fake) and parsers
  config/                    # app configuration
  crypto/                    # X25519 keygen, base64, preshared keys
  domain/                    # domain types, profile, peer, errors
  ipam/                      # IP allocator with concurrent safety
  obs/                       # logging, Prometheus, OTel
  outbox/                    # outbox worker + reconciler
  ratelimit/                 # token bucket
  render/                    # AmneziaWG V2 config renderer
  repo/                      # Postgres repositories (pgx)
  server/                    # bootstrap
migrations/                  # SQL migrations
api/openapi.yaml             # OpenAPI 3.1
deploy/
  docker/                    # multi-stage Dockerfile
  compose/                   # docker-compose stack
test/
  integration/               # Postgres-backed tests (testcontainers)
  e2e/                       # end-to-end against in-process fake AWG host
```

## Quick start (dev)

```bash
# 1. Bring up Postgres + the API + a dev node-agent.
# Ports are bound to 127.0.0.1, not all host interfaces.
docker compose -f deploy/compose/docker-compose.yml up --build

# 2. Run the unit suite (cross-platform)
go test ./...

# 3. Run integration tests (needs Docker for testcontainers)
go test -tags=integration ./test/integration/...

# 4. Run E2E with the fake executor (cross-platform)
go test -tags=e2e ./test/e2e/...

# 5. Real-AWG E2E (Linux, needs amneziawg-tools + kernel module)
go test -tags="e2e linux_awg" ./test/e2e/...
```

## Operational model

- API/worker write only **desired state** transactionally; the worker brings runtime to desired via
  the node agent (which calls `awg syncconf` / `awg set`).
- `SaveConfig=false` is enforced; the rendered config under `/etc/amnezia/rendered/<iface>.conf`
  plus the DB row are the single declarative source.
- After any restart: `awg-quick up <iface>` (if missing) -> `awg syncconf <iface> <conf>` ->
  `awg show <iface> dump` → reconcile delta into DB → mark node ready.

## Security

- Tenant isolation is enforced in the service layer: non-`platform_admin` tokens must carry a
  `tenant_id` matching the `{tenant}` URL tenant or the operation tenant.
- JWT BCP (RFC 8725): explicit `alg` allowlist, `iss`/`aud` pinning, `exp/nbf` enforced, and
  `typ=at+jwt` for access tokens. Production should use `JWT_PUBLIC_KEY_FILE` with an asymmetric
  public key. `JWT_SECRET`/HS256 is for local dev or tightly internal deployments with a strong
  secret.
- Worker -> node-agent is HTTPS+mTLS by default. Plain HTTP requires explicit
  `AGENT_INSECURE_HTTP=true` and `NODE_AGENT_INSECURE_HTTP=true`, intended only for dev/test.
- Client private keys are never accepted in query strings. If the API generated a peer key, the
  private key appears only in the first create response and is not persisted into idempotency
  replay responses.
- `/dump` and `/showconf` on node-agent redact interface private keys and peer preshared keys.
- All destructive operations append to `audit_events`.

## Production compose

`deploy/compose/docker-compose.prod.yml` is a hardened skeleton for a backend-only deployment:

- `awg-api`, `postgres`, `awg-worker`, and `awg-node-agent` do not publish host ports.
- `awg-api` listens on `:18080` inside the container so other containers on the same Docker network
  can call `http://awg-api:18080`.
- The API network is `internal: true` and named `awg-backend-internal` by default. Attach your
  backend container to that same network and do not expose `awg-api` to the public internet.
- If you must access the API from the host, publish loopback only: `127.0.0.1:18080:18080`.
  Do not set `HTTP_ADDR=127.0.0.1:18080` inside the API container if another container must reach it.

Prepare secrets under `deploy/compose/secrets/`:

```text
postgres_password          # DB password
database_url               # postgres://awg:<password>@postgres:5432/awg?sslmode=disable
jwt_public_key.pem         # IdP/API public signing key, PEM
worker.crt / worker.key    # client cert/key for awg-worker
agent_ca.pem               # CA that signed the node-agent server cert
agent.crt / agent.key      # node-agent server cert/key
agent_client_ca.pem        # CA that signed worker client certs
```

Non-secret deployment placeholders and required image names are documented in `.env.example`.

Then run:

```bash
docker compose -f deploy/compose/docker-compose.prod.yml up -d
```

For a backend in another compose project, reference the created network as external:

```yaml
networks:
  awg-backend-internal:
    external: true
```

and attach the backend service to it. The backend should call `http://awg-api:18080`.

## GitHub and release readiness

- CI builds/tests all Go packages and Docker targets; Dependabot is configured for Go modules,
  GitHub Actions, and Dockerfiles.
- `AGENTS.md` and `llms.txt` describe the repository for coding agents and LLM tooling.
- Runtime secrets stay outside Git and images: use env vars, Docker secrets, or mounted secret
  files.
- Recommended next release step: publish the three Docker targets (`awg-api`, `awg-worker`,
  `awg-node-agent`) to GHCR from semver tags (`vX.Y.Z`), with immutable SHA tags, SBOM/provenance,
  and no VPS deployment logic in the image-publish workflow.
