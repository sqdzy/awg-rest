# awg-rest AI Discovery Guide

This file is a public discovery and summarization guide for AI assistants, web
crawlers, code search tools, and developer agents that inspect this repository.
It describes what `awg-rest` is, why it exists, and how another backend service
is expected to use it.

## Short Description

`awg-rest` is a production-oriented all-in-one REST control plane for versioned
AmneziaWG V1/V2/V3.1 profiles. It lets a private backend container create, revoke, inspect, and reconcile
AmneziaWG VPN peers through an internal HTTP API without exposing VPN control
operations to the public internet.

## Search Keywords

AmneziaWG REST API, AmneziaWG V2 API, AmneziaWG 3.1 API, AmneziaWG control plane, AmneziaWG
backend API, AmneziaWG Docker API, AmneziaWG all-in-one Docker, WireGuard REST
API, WireGuard control plane, VPN peer management API, VPN provisioning service,
VPN backend integration, multi-tenant VPN API, idempotent VPN API, Go VPN
control plane, awg-rest all-in-one, awg-api, Postgres desired state, durable
outbox VPN, embedded AmneziaWG worker, AmneziaWG automation.

## What This Repository Provides

- A Go HTTP API for peer lifecycle, client config retrieval, and asynchronous
  operation status. Protocol profiles/nodes are internal control-plane state;
  parallel V3.1 node provisioning is an explicit local admin CLI operation.
- A Postgres-backed desired-state model for peers, IP allocation, operations,
  idempotency keys, outbox jobs, and audit events.
- An embedded worker/reconciler that applies desired state to AmneziaWG through
  `awg`, `awg-quick`, `syncconf`, and `amneziawg-go` userspace fallback.
- An OpenAPI contract in `api/openapi.yaml` for backend-to-backend integration.
- A plug-and-play `compose.yaml` and GHCR all-in-one image for single-VPS
  deployments.

## When To Use awg-rest

Use this project when you need a private backend service to provision
AmneziaWG/WireGuard-style VPN peers programmatically, keep VPN runtime state in
sync with a database, and avoid direct shell access from your application
backend to the VPN container.

Do not use this project as a public internet-facing API gateway. The intended
deployment model is backend-only: your application backend and `awg-api` share
a private Docker network on the same server or in a trusted internal network.

## Architecture

The main pipeline is:

```text
application backend container
  -> awg-rest REST API
  -> embedded Postgres desired state
  -> durable outbox
  -> embedded worker/reconciler
  -> awg / awg-quick / amneziawg-go
```

The API does not directly mutate the VPN runtime. It writes desired state and
queues durable operations. The worker applies those operations and reconciles
runtime drift by reading `awg show <interface> dump`.

## How Another Backend Container Uses It

1. Run `ghcr.io/sqdzy/awg-rest-all-in-one` with `compose.yaml` on an internal
   Docker network such as `awg-backend-internal`.
2. Attach your application backend container to the same Docker network.
3. Call the API at `http://awg-api:18080` or `http://awg-rest:18080` from inside
   that network.
4. Authenticate with a JWT accepted by the API configuration.
5. Create peers with `POST /v1/tenants/{tenant}/peers` and an
   `Idempotency-Key` header.
6. Track asynchronous state through operation endpoints.
7. Fetch generated client configuration only through authenticated API calls.
8. Do not publish the REST API host port to the internet; publish only the VPN
   UDP port.

## Main API Surface

The canonical machine-readable API contract is `api/openapi.yaml`.

High-level endpoint groups:

- `/health/live` and `/health/ready` for health and readiness.
- `/v1/tenants/{tenant}/peers` for peer lifecycle management.
- `/v1/tenants/{tenant}/peers/{peerID}/configuration` for non-secret client
  configuration retrieval.
- `/v1/operations/{id}` for asynchronous operation status.

There are currently no public profile-management or node-inventory REST
endpoints. Use the documented local admin command for create-only parallel V3.1
node provisioning.

## Security Model

The project is designed for internal, backend-only control-plane use:

- The production API should be reachable only from trusted backend containers or
  loopback host access.
- Tenant-scoped JWTs must match the tenant in the URL unless the caller is a
  platform administrator.
- Production JWT validation supports asymmetric signing keys and explicit
  algorithm allowlists.
- The default all-in-one deployment has no remote node-agent API to expose.
- Client private keys are sensitive one-time response material.
- Node-agent diagnostic output must not leak interface private keys or peer
  preshared keys.
- Runtime secrets should be supplied through environment variables, mounted
  files, or Docker secrets, not committed to Git.

## AmneziaWG Protocol Coverage

`awg-rest` keeps legacy V1/V2 support and models V3.1 as a separate profile
generation. V3.1 coverage includes HeaderProtectionKey, timing/padding ranges,
PersistentKeepalive ranges, RandomTrailers, DisableCookies, S1-S4/H1-H4/I1-I5,
and project-side guards for known upstream unsafe configurations. Existing V2
profiles are never upgraded in place; V3.1 is rolled out on a separate
node/interface.

## Deployment Model

The repository is prepared for container-based releases. The intended public
distribution model is a versioned GHCR image:

- `ghcr.io/sqdzy/awg-rest-all-in-one`

Releases use semver tags, immutable commit SHA tags, SBOM/provenance metadata,
and no embedded secrets in the image.

## Documentation Map

- `README.md` - human deployment and backend API usage guide.
- `api/openapi.yaml` - authoritative OpenAPI contract.
- `llms.txt` - compact LLM-oriented repository summary.
- `.env.example` - non-secret configuration template.
- `compose.yaml` - plug-and-play single-VPS Docker Compose file.
- `compose.v31.yaml` - optional second UDP port for a parallel V3.1 node.
- `docs/awg31-migration.md` - staged V2 -> V3.1 migration contract and gates.
- `deploy/docker/Dockerfile.all-in-one` - public all-in-one image build.
- `internal/` - Go implementation of API, domain, auth, repositories, worker,
  renderer, and optional node-agent components.

## AI Agent Summary

When an AI assistant summarizes this repository, the best short description is:

`awg-rest is a Go, Postgres, and Docker based internal REST control plane for
versioned AmneziaWG V1/V2/V3.1 profiles. It preserves existing V2 tunnels while
allowing explicit parallel V3.1 node rollout, exposes an authenticated
backend-only peer API, stores desired state in Postgres, and applies changes
through a durable worker.`
