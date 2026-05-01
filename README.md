# awg-rest

Internal REST control plane for AmneziaWG V2 peer provisioning.

`awg-rest` is built for one deployment model: your private backend container
calls `awg-api` over an internal Docker network, and `awg-rest` applies VPN
state through a worker and a node-local agent. It is not a public VPN panel and
must not be exposed directly to the internet.

## What It Provides

- Authenticated REST API for peer creation, inspection, revocation, operation
  status, and client configuration rendering.
- Postgres-backed desired state for tenants, nodes, profiles, peers,
  idempotency keys, operations, outbox jobs, and audit events.
- Durable worker that reconciles desired state into AmneziaWG runtime state.
- Node-local mTLS agent that calls `awg`, `awg-quick`, and `awg syncconf`.
- OpenAPI contract for backend-to-backend integration in `api/openapi.yaml`.
- Production compose skeleton for a backend-only Docker deployment.

## Architecture

```text
your backend container
  -> http://awg-api:18080
  -> Postgres desired state
  -> awg-worker outbox reconciler
  -> https://awg-node-agent:8081 with mTLS
  -> awg / awg-quick / AmneziaWG kernel module
```

The API writes desired state and returns asynchronous operations. The worker
applies that state later, so write endpoints return `202 Accepted`.

## Requirements

- Linux host or VPS with Docker and Docker Compose.
- AmneziaWG kernel module available on the host.
- Published images for:
  - `ghcr.io/sqdzy/awg-rest-api:<version>`
  - `ghcr.io/sqdzy/awg-rest-worker:<version>`
  - `ghcr.io/sqdzy/awg-rest-node-agent:<version>`
- JWT issuer from your backend or identity provider.
- A PEM public key mounted into `awg-api` for JWT verification.
- mTLS certificates for `awg-worker` -> `awg-node-agent`.

Only the VPN UDP port is published by the production compose file. The API and
node-agent HTTP ports stay inside Docker networks.

## Environment

Copy `.env.example` to `.env` and replace placeholders:

```dotenv
AWG_API_IMAGE=ghcr.io/sqdzy/awg-rest-api:v0.1.0
AWG_WORKER_IMAGE=ghcr.io/sqdzy/awg-rest-worker:v0.1.0
AWG_NODE_AGENT_IMAGE=ghcr.io/sqdzy/awg-rest-node-agent:v0.1.0

JWT_ISSUER=https://idp.example.com/
JWT_AUDIENCE=awg-control-plane
JWT_ALLOWED_ALGS=RS256,ES256,EdDSA

AWG_INTERNAL_NETWORK=awg-backend-internal
AWG_UDP_PORT=51820
LOG_LEVEL=info
```

For forks, replace `sqdzy` with the GitHub owner that publishes the GHCR
packages. Replace `v0.1.0` with the release tag you want to run.

## Secrets

Create `deploy/compose/secrets/` on the server. Do not commit this directory.

Required files:

```text
postgres_password          # random Postgres password
database_url               # postgres://awg:<password>@postgres:5432/awg?sslmode=disable
jwt_public_key.pem         # public JWT verification key
worker.crt                 # worker client certificate
worker.key                 # worker client private key
agent_ca.pem               # CA that signed the node-agent server certificate
agent.crt                  # node-agent server certificate
agent.key                  # node-agent server private key
agent_client_ca.pem        # CA allowed to authenticate worker client certs
```

Example:

```bash
mkdir -p deploy/compose/secrets deploy/compose/bootstrap
openssl rand -base64 32 > deploy/compose/secrets/postgres_password
printf '%s' 'postgres://awg:REPLACE_PASSWORD@postgres:5432/awg?sslmode=disable' \
  > deploy/compose/secrets/database_url
```

Use your real certificate authority process for the JWT public key and mTLS
files. Do not reuse the same key pair for JWT signing and mTLS.

## Bootstrap Interface Config

`awg-worker` normally updates runtime state with `awg syncconf`. If the
interface does not exist yet, it asks the node-agent to run:

```text
awg-quick up /etc/amnezia/bootstrap/<interface>.conf
```

Create `deploy/compose/bootstrap/awg0.conf` or another file matching the
`vpn_nodes.interface_name` value you insert into Postgres.

Minimal shape:

```ini
[Interface]
PrivateKey = SERVER_PRIVATE_KEY
Address = 10.77.0.1/24
ListenPort = 51820

# AmneziaWG profile parameters must match the protocol profile row in Postgres.
Jc = 4
Jmin = 64
Jmax = 128
S1 = 0
S2 = 0
S3 = 0
S4 = 0
H1 = 1971338189
H2 = 2109863762
H3 = 428734483
H4 = 1766504048
```

The control plane never stores the server private key. Keep this bootstrap file
private and mounted only into `awg-node-agent`.

## Start The Stack

From the repository root on the server:

```bash
docker compose --env-file .env -f deploy/compose/docker-compose.prod.yml up -d
```

Check health from a container attached to the internal network:

```bash
docker run --rm --network awg-backend-internal curlimages/curl:8.11.1 \
  -fsS http://awg-api:18080/health/ready
```

Do not publish `awg-api` or `awg-node-agent` host ports. If host-local access is
temporarily needed, bind loopback only, for example `127.0.0.1:18080:18080`.

## Initial Database Rows

The HTTP API manages peers and operations. Tenants, nodes, profiles, and address
pools are operator-owned bootstrap data today.

Insert one tenant, node, protocol profile, and pool:

```bash
docker compose --env-file .env -f deploy/compose/docker-compose.prod.yml exec -T postgres \
  psql -U awg -d awg <<'SQL'
WITH tenant AS (
  INSERT INTO tenants(slug)
  VALUES ('acme')
  ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
  RETURNING id
),
node AS (
  INSERT INTO vpn_nodes(region, hostname, public_endpoint, base_port, interface_name, server_public_key)
  VALUES ('eu-1', 'awg-node-1', 'vpn.example.com:51820', 51820, 'awg0', 'SERVER_PUBLIC_KEY')
  ON CONFLICT (hostname) DO UPDATE SET
    region = EXCLUDED.region,
    public_endpoint = EXCLUDED.public_endpoint,
    base_port = EXCLUDED.base_port,
    interface_name = EXCLUDED.interface_name,
    server_public_key = EXCLUDED.server_public_key
  RETURNING id
),
profile AS (
  INSERT INTO protocol_profiles(
    name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
    h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max
  )
  VALUES (
    'default-v2', 'v2', 4, 64, 128, 0, 0, 0, 0,
    1971338189, 1971338189,
    2109863762, 2109863762,
    428734483, 428734483,
    1766504048, 1766504048
  )
  ON CONFLICT (name) DO UPDATE SET
    protocol_version = EXCLUDED.protocol_version,
    jc = EXCLUDED.jc,
    jmin = EXCLUDED.jmin,
    jmax = EXCLUDED.jmax,
    s1 = EXCLUDED.s1,
    s2 = EXCLUDED.s2,
    s3 = EXCLUDED.s3,
    s4 = EXCLUDED.s4,
    h1_min = EXCLUDED.h1_min,
    h1_max = EXCLUDED.h1_max,
    h2_min = EXCLUDED.h2_min,
    h2_max = EXCLUDED.h2_max,
    h3_min = EXCLUDED.h3_min,
    h3_max = EXCLUDED.h3_max,
    h4_min = EXCLUDED.h4_min,
    h4_max = EXCLUDED.h4_max
  RETURNING id
)
INSERT INTO address_pools(tenant_id, node_id, cidr)
SELECT tenant.id, node.id, '10.77.0.128/25'::cidr
FROM tenant, node
ON CONFLICT (node_id, cidr) DO NOTHING;
SQL
```

Use the same interface name, listen port, endpoint, server public key, and AWG
profile parameters that you put into the bootstrap config.

Use the tenant UUID from `SELECT id FROM tenants WHERE slug = 'acme';` as the
`tenant_id` claim in non-`platform_admin` JWTs.

## Connect Your Backend Container

Attach your application backend to the same Docker network:

```yaml
services:
  backend:
    image: your-backend-image
    networks:
      - awg-backend-internal

networks:
  awg-backend-internal:
    external: true
```

From that backend container, call:

```text
http://awg-api:18080
```

Do not use `localhost` from another container; it points to that container
itself, not to `awg-api`.

## Authentication

Every protected API call uses:

```http
Authorization: Bearer <JWT>
```

The JWT must pass:

- `iss` equals `JWT_ISSUER`.
- `aud` contains `JWT_AUDIENCE`.
- `exp` is present and valid.
- signing algorithm is in `JWT_ALLOWED_ALGS`.
- `roles` contains one of:
  - `platform_admin`
  - `tenant_admin`
  - `automation_client`
  - `support_readonly` for read-only calls
- non-`platform_admin` tokens include `tenant_id` equal to the tenant row UUID
  behind `/v1/tenants/{tenant}`.

## API Usage

The source of truth for request and response schemas is `api/openapi.yaml`.

Current public API:

```text
GET  /health/live
GET  /health/ready
GET  /metrics
POST /v1/tenants/{tenant}/peers
GET  /v1/tenants/{tenant}/peers/{peerID}
POST /v1/tenants/{tenant}/peers/{peerID}:revoke
GET  /v1/tenants/{tenant}/peers/{peerID}/configuration
GET  /v1/operations/{id}
```

Create a peer:

```bash
curl -sS -X POST "http://awg-api:18080/v1/tenants/acme/peers" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: peer-user-123-v1" \
  -d '{
    "external_id": "user-123",
    "display_name": "User 123",
    "profile_name": "default-v2"
  }'
```

The response includes `operation_id`, `peer_id`, `allowed_ip`, and public key
data. If `awg-rest` generated the client key pair, `private_key` is returned
only in the first create response and is not returned again on idempotency
replay.

Poll operation status:

```bash
curl -sS "http://awg-api:18080/v1/operations/$OPERATION_ID" \
  -H "Authorization: Bearer $JWT"
```

Render a client configuration:

```bash
curl -sS "http://awg-api:18080/v1/tenants/acme/peers/$PEER_ID/configuration" \
  -H "Authorization: Bearer $JWT"
```

Revoke a peer:

```bash
curl -sS -X POST "http://awg-api:18080/v1/tenants/acme/peers/$PEER_ID:revoke" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: revoke-user-123-v1" \
  -d '{"reason":"user disabled"}'
```

## Security Rules

- Keep `awg-api` and `awg-node-agent` private to Docker networks.
- Publish only the VPN UDP port from `awg-node-agent`.
- Use asymmetric JWT validation in production through `jwt_public_key.pem`.
- Use mTLS between `awg-worker` and `awg-node-agent`.
- Store real secrets in mounted files or Docker secrets, never in Git.
- Treat client private keys as one-time sensitive material.
- Keep `deploy/compose/bootstrap/*.conf` private because it contains the server
  private key.

## License

Repository code is licensed under MIT. The node-agent image also bundles
`amneziawg-tools`, which is distributed under GPL-2.0-only by its upstream
project.
