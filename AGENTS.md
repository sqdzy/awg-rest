# AGENTS.md

## Project

`awg-rest` is a Go control plane for AmneziaWG 2.0. The pipeline is:
REST API -> Postgres desired state -> durable outbox -> worker/reconciler ->
node-agent -> `awg`/kernel module.

## Commands

- Unit tests: `go test ./...`
- Integration tests: `go test -tags=integration ./test/integration/...`
- E2E with fake AWG: `go test -tags=e2e ./test/e2e/...`
- Real-AWG E2E on Linux: `go test -tags="e2e linux_awg" ./test/e2e/...`
- Static checks: `go vet ./...`
- Docker targets: `awg-api`, `awg-worker`, `awg-node-agent` in `deploy/docker/Dockerfile`

## Security invariants

- Do not commit secrets, generated certs, `.env`, `.omx`, or files under `deploy/compose/secrets/`.
- Production API must be reachable only from the backend/internal Docker network unless loopback
  host access is explicitly needed.
- Worker to node-agent traffic must use HTTPS+mTLS in production. Plain HTTP flags are dev/test only.
- Tenant-scoped tokens must match the URL tenant unless the caller is `platform_admin`.
- Client private keys must not travel in query strings or idempotency replay bodies.
- Node-agent diagnostic output must redact `PrivateKey` and `PresharedKey`.
- AWG2 profile limits must follow official docs: `Jc` 0..10, `Jmin/Jmax` 64..1024 when junk
  packets are enabled, `S1-S3` 0..64, `S4` 0..32, disjoint `H1-H4` uint32 ranges.

## Editing notes

- Keep migrations append-only once the repository is public.
- Prefer small, focused changes and tests near the behavior being changed.
- Preserve the backend-only deployment model; do not add public host port publishing to prod compose.
