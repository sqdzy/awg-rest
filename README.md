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
AWG_API_IMAGE=ghcr.io/sqdzy/awg-rest-api:v0.1.1
AWG_WORKER_IMAGE=ghcr.io/sqdzy/awg-rest-worker:v0.1.1
AWG_NODE_AGENT_IMAGE=ghcr.io/sqdzy/awg-rest-node-agent:v0.1.1

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

Create them on the server:

```bash
cd /opt/awg-rest
mkdir -p deploy/compose/secrets deploy/compose/bootstrap
chmod 700 deploy/compose/secrets

openssl rand -hex 32 > deploy/compose/secrets/postgres_password
PGPASSWORD="$(tr -d '\r\n' < deploy/compose/secrets/postgres_password)"
printf 'postgres://awg:%s@postgres:5432/awg?sslmode=disable' "$PGPASSWORD" \
  > deploy/compose/secrets/database_url

openssl genrsa -out deploy/compose/secrets/jwt_private.pem 3072
openssl rsa -in deploy/compose/secrets/jwt_private.pem -pubout \
  -out deploy/compose/secrets/jwt_public_key.pem

openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
  -keyout deploy/compose/secrets/agent_ca.key \
  -out deploy/compose/secrets/agent_ca.pem \
  -subj "/CN=awg-rest agent server CA"

cat > deploy/compose/secrets/agent-server.ext <<'EOF'
subjectAltName=DNS:awg-node-agent
extendedKeyUsage=serverAuth
EOF
openssl req -newkey rsa:4096 -nodes \
  -keyout deploy/compose/secrets/agent.key \
  -out deploy/compose/secrets/agent.csr \
  -subj "/CN=awg-node-agent"
openssl x509 -req -in deploy/compose/secrets/agent.csr \
  -CA deploy/compose/secrets/agent_ca.pem \
  -CAkey deploy/compose/secrets/agent_ca.key \
  -CAcreateserial -out deploy/compose/secrets/agent.crt \
  -days 825 -sha256 -extfile deploy/compose/secrets/agent-server.ext

openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
  -keyout deploy/compose/secrets/worker_ca.key \
  -out deploy/compose/secrets/agent_client_ca.pem \
  -subj "/CN=awg-rest worker client CA"

cat > deploy/compose/secrets/worker-client.ext <<'EOF'
extendedKeyUsage=clientAuth
EOF
openssl req -newkey rsa:4096 -nodes \
  -keyout deploy/compose/secrets/worker.key \
  -out deploy/compose/secrets/worker.csr \
  -subj "/CN=awg-worker"
openssl x509 -req -in deploy/compose/secrets/worker.csr \
  -CA deploy/compose/secrets/agent_client_ca.pem \
  -CAkey deploy/compose/secrets/worker_ca.key \
  -CAcreateserial -out deploy/compose/secrets/worker.crt \
  -days 825 -sha256 -extfile deploy/compose/secrets/worker-client.ext

rm -f deploy/compose/secrets/*.csr deploy/compose/secrets/*.srl deploy/compose/secrets/*.ext
chmod 600 deploy/compose/secrets/*
```

File meaning:

- `postgres_password` is the Postgres password used by the bundled database.
- `database_url` is the same password in a Postgres connection string for API
  and worker containers.
- `jwt_public_key.pem` is the public key used by `awg-api` to verify backend
  JWTs. Keep `jwt_private.pem` outside `awg-api`; your backend or identity
  provider uses it to sign tokens.
- `agent_ca.pem` is trusted by `awg-worker` when it connects to
  `https://awg-node-agent:8081`.
- `agent.crt` and `agent.key` are the TLS server certificate and key for
  `awg-node-agent`. The certificate must contain `DNS:awg-node-agent`.
- `agent_client_ca.pem` is trusted by `awg-node-agent` for worker client
  certificates.
- `worker.crt` and `worker.key` are the mTLS client certificate and key used by
  `awg-worker`.

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

Example bootstrap rows:

```sql
INSERT INTO tenants(slug)
VALUES ('acme')
ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug;

INSERT INTO vpn_nodes(
  region, hostname, public_endpoint, base_port, interface_name, server_public_key
) VALUES (
  'eu', 'vpn-1', '<VPS_PUBLIC_IP>:51820', 51820, 'awg0', '<SERVER_PUBLIC_KEY>'
)
ON CONFLICT (hostname) DO UPDATE SET
  public_endpoint = EXCLUDED.public_endpoint,
  base_port = EXCLUDED.base_port,
  interface_name = EXCLUDED.interface_name,
  server_public_key = EXCLUDED.server_public_key;

INSERT INTO protocol_profiles(
  name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
  h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
  listen_port_policy
) VALUES (
  'default-v2', 'v2', 5, 64, 1000, 40, 32, 10, 8,
  1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000,
  'fixed'
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
  h4_max = EXCLUDED.h4_max,
  listen_port_policy = EXCLUDED.listen_port_policy;

INSERT INTO address_pools(tenant_id, node_id, cidr)
SELECT t.id, n.id, '10.90.0.0/24'::cidr
FROM tenants t
JOIN vpn_nodes n ON n.hostname = 'vpn-1'
WHERE t.slug = 'acme'
ON CONFLICT (node_id, cidr) DO NOTHING;
```

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
