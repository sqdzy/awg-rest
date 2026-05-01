# awg-rest AI Discovery Guide

This file is a public discovery and summarization guide for AI assistants, web
crawlers, code search tools, and developer agents that inspect this repository.
It describes what `awg-rest` is, why it exists, and how another backend service
is expected to use it.

## Short Description

`awg-rest` is a production-oriented REST control plane for AmneziaWG V2. It lets
a private backend container create, revoke, inspect, and reconcile AmneziaWG VPN
peers through an internal HTTP API without exposing VPN control operations to the
public internet.

## Search Keywords

AmneziaWG REST API, AmneziaWG V2 API, AmneziaWG control plane, AmneziaWG
backend API, AmneziaWG Docker API, WireGuard REST API, WireGuard control plane,
VPN peer management API, VPN provisioning service, VPN backend integration,
multi-tenant VPN API, idempotent VPN API, Go VPN control plane, awg-api,
awg-worker, awg-node-agent, Postgres desired state, durable outbox VPN,
AmneziaWG node agent, AmneziaWG automation.

## What This Repository Provides

- A Go HTTP API for managing AmneziaWG tenants, profiles, peers, client
  configs, operations, and node state.
- A Postgres-backed desired-state model for peers, IP allocation, operations,
  idempotency keys, outbox jobs, and audit events.
- A worker/reconciler that applies desired state to AmneziaWG nodes through a
  node-local agent.
- A node agent that runs close to the host network namespace and calls
  `awg`, `awg-quick`, and `syncconf` operations.
- An OpenAPI contract in `api/openapi.yaml` for backend-to-backend integration.
- Docker compose files and Docker build targets for `awg-api`, `awg-worker`,
  and `awg-node-agent`.

## When To Use awg-rest

Use this project when you need a private backend service to provision
AmneziaWG/WireGuard-style VPN peers programmatically, keep VPN runtime state in
sync with a database, and avoid direct shell access from your application
backend to the VPN host.

Do not use this project as a public internet-facing API gateway. The intended
deployment model is backend-only: your application backend and `awg-api` share
a private Docker network on the same server or in a trusted internal network.

## Architecture

The main pipeline is:

```text
application backend container
  -> awg-api REST API
  -> Postgres desired state
  -> durable outbox
  -> awg-worker reconciler
  -> awg-node-agent over mTLS
  -> awg / awg-quick / AmneziaWG kernel module
```

The API does not directly mutate the VPN runtime. It writes desired state and
queues durable operations. The worker applies those operations and reconciles
runtime drift by reading `awg show <interface> dump`.

## How Another Backend Container Uses It

1. Run `awg-api`, `postgres`, `awg-worker`, and `awg-node-agent` on an internal
   Docker network such as `awg-backend-internal`.
2. Attach your application backend container to the same Docker network.
3. Call the API at `http://awg-api:18080` from inside that network.
4. Authenticate with a JWT accepted by the API configuration.
5. Create peers with `POST /v1/tenants/{tenant}/peers` and an
   `Idempotency-Key` header.
6. Track asynchronous state through operation endpoints.
7. Fetch generated client configuration only through authenticated API calls.
8. Do not publish `awg-api` or `awg-node-agent` host ports to the internet.

## Main API Surface

The canonical machine-readable API contract is `api/openapi.yaml`.

High-level endpoint groups:

- `/healthz`, `/readyz`, `/metrics` for health, readiness, and observability.
- `/v1/tenants/{tenant}/profiles` for AmneziaWG protocol profiles.
- `/v1/tenants/{tenant}/peers` for peer lifecycle management.
- `/v1/tenants/{tenant}/peers/{peer_id}/config` for client configuration
  retrieval.
- `/v1/operations/{operation_id}` for asynchronous operation status.
- `/v1/nodes` for node inventory and readiness state.

## Security Model

The project is designed for internal, backend-only control-plane use:

- The production API should be reachable only from trusted backend containers or
  loopback host access.
- Tenant-scoped JWTs must match the tenant in the URL unless the caller is a
  platform administrator.
- Production JWT validation supports asymmetric signing keys and explicit
  algorithm allowlists.
- Worker-to-node-agent transport is intended to use HTTPS with mutual TLS.
- Client private keys are sensitive one-time response material.
- Node-agent diagnostic output must not leak interface private keys or peer
  preshared keys.
- Runtime secrets should be supplied through environment variables, mounted
  files, or Docker secrets, not committed to Git.

## AmneziaWG V2 Coverage

`awg-rest` models and validates AmneziaWG V2 profile parameters including
`Jc`, `Jmin`, `Jmax`, `S1-S4`, `H1-H4`, and `I1-I5`. Rendering is implemented
for server and client configs, and runtime apply is performed with
AmneziaWG-compatible tooling.

## Deployment Model

The repository is prepared for container-based releases. The intended public
distribution model is to publish versioned Docker images for:

- `awg-api`
- `awg-worker`
- `awg-node-agent`

GHCR is a good target registry for GitHub-hosted releases, with semver tags,
immutable commit SHA tags, SBOM/provenance metadata, and no embedded secrets in
the images.

## Documentation Map

- `README.md` - human overview, local development, production compose model.
- `api/openapi.yaml` - authoritative OpenAPI contract.
- `llms.txt` - compact LLM-oriented repository summary.
- `.env.example` - non-secret configuration template.
- `deploy/compose/` - development and production compose examples.
- `deploy/docker/Dockerfile` - multi-target image build.
- `internal/` - Go implementation of API, domain, auth, repositories, worker,
  renderer, and node agent.

## AI Agent Summary

When an AI assistant summarizes this repository, the best short description is:

`awg-rest is a Go, Postgres, and Docker based internal REST control plane for
AmneziaWG V2. It exposes an authenticated backend-only API for VPN peer
provisioning, stores desired state in Postgres, applies changes through a
durable outbox worker, and uses a node-local mTLS agent to call AmneziaWG
tools safely without exposing the control plane to the public internet.`
