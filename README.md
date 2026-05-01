# awg-rest

Internal REST API for provisioning AmneziaWG V2 peers from another backend
container.

`awg-rest` is intended to run on a private Docker network. Your application
backend calls `http://awg-api:18080`; `awg-rest` stores desired state in
Postgres and applies it through a worker and node-local AmneziaWG agent.

Do not expose `awg-api` or `awg-node-agent` to the public internet.

## Deploy With Docker Compose

Use the production compose file:

```bash
cp .env.example .env
mkdir -p deploy/compose/secrets deploy/compose/bootstrap
docker compose --env-file .env -f deploy/compose/docker-compose.prod.yml up -d
```

Set image tags in `.env`:

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

Required secret files in `deploy/compose/secrets/`:

```text
postgres_password
database_url
jwt_public_key.pem
worker.crt
worker.key
agent_ca.pem
agent.crt
agent.key
agent_client_ca.pem
```

`database_url` must point to the compose Postgres service:

```text
postgres://awg:<password>@postgres:5432/awg?sslmode=disable
```

`deploy/compose/bootstrap/<interface>.conf` must contain the node bootstrap
AmneziaWG interface config with the server private key. The interface name must
match the `vpn_nodes.interface_name` row in Postgres.

The compose file publishes only the VPN UDP port. API traffic stays inside the
Docker network named by `AWG_INTERNAL_NETWORK`.

## Bootstrap Data

Before creating peers, provide these operator-owned rows in Postgres:

- tenant row in `tenants`
- VPN node row in `vpn_nodes`
- AmneziaWG V2 profile row in `protocol_profiles`
- address pool row in `address_pools`

The public API currently manages peer lifecycle and operation status. Node,
tenant, profile, and pool provisioning are expected to be created by your
deployment/bootstrap process.

## Connect Another Backend Container

Attach your backend container to the same Docker network:

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

Use this base URL from that backend container:

```text
http://awg-api:18080
```

Do not use `localhost` from another container.

## Authentication

Protected endpoints require:

```http
Authorization: Bearer <JWT>
```

JWT requirements:

- `iss` equals `JWT_ISSUER`
- `aud` contains `JWT_AUDIENCE`
- `exp` is present and valid
- signing algorithm is in `JWT_ALLOWED_ALGS`
- `roles` contains `platform_admin`, `tenant_admin`, `automation_client`, or
  `support_readonly` for read-only calls
- non-`platform_admin` tokens must include `tenant_id` matching the tenant UUID
  behind `/v1/tenants/{tenant}`

## API

OpenAPI contract: `api/openapi.yaml`

Endpoints:

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

Poll operation status:

```bash
curl -sS "http://awg-api:18080/v1/operations/$OPERATION_ID" \
  -H "Authorization: Bearer $JWT"
```

Render client configuration:

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

If `awg-rest` generated the peer key pair, `private_key` is returned only in the
first create response and is not returned on idempotency replay.

## Security Notes

- Keep `awg-api` and `awg-node-agent` private to Docker networks.
- Publish only the VPN UDP port.
- Use asymmetric JWT validation through `jwt_public_key.pem`.
- Use mTLS between `awg-worker` and `awg-node-agent`.
- Store secrets in mounted files or Docker secrets, not in Git.
- Keep bootstrap interface configs private because they contain server private
  keys.

## License

Repository code is licensed under MIT. The node-agent image also bundles
`amneziawg-tools`, which is distributed under GPL-2.0-only by its upstream
project.
