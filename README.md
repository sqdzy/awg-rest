# awg-rest

Internal REST API for provisioning AmneziaWG V2 VPN peers from another backend
container.

`awg-rest` is designed for a single private control-plane path:

```text
your backend container -> awg-rest REST API -> Postgres -> embedded worker -> awg/amneziawg-go
```

The default distribution is an all-in-one Docker image. It contains the API,
embedded worker, PostgreSQL, `amneziawg-tools`, and `amneziawg-go` userspace
fallback, so the host does not need an AmneziaWG kernel module.

Do not expose the REST API to the public internet. Publish only the VPN UDP
port.

## Requirements

- Linux VPS with Docker Engine and Docker Compose.
- `/dev/net/tun` available on the host.
- UDP port `51820` open in the VPS firewall, or the port you configure.
- Public IP or DNS name for generated client configs.

The all-in-one image still needs `NET_ADMIN` and `/dev/net/tun` because the VPN
interface is created inside the container.

## Deploy

Clone the repository or place `compose.yaml` and `.env` in an empty directory:

```bash
cp .env.example .env
```

Edit `.env`:

```dotenv
BOOTSTRAP_NODE_ENDPOINT=203.0.113.10
JWT_SECRET=replace-with-at-least-32-random-bytes
AWG_API_BIND=127.0.0.1:18080
AWG_UDP_BIND=51820
AWG_UDP_PORT=51820
```

Start the stack:

```bash
docker compose up -d
```

What starts:

- `awg-rest` on the internal Docker network `awg-backend-internal`
- REST API on `http://awg-rest:18080` inside Docker
- optional host-local API access on `127.0.0.1:18080`
- VPN UDP listener on `51820/udp`
- persistent volumes `awg-postgres` and `awg-state`

On first start, the container creates:

- tenant `default`
- AmneziaWG V2 profile `default-v2`
- node `awg-node-1`
- address pool `10.200.0.0/24`
- server private key and bootstrap interface config in `awg-state`

Back up both Docker volumes. `awg-state` contains the server private key.

## Connect Your Backend

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

Use this base URL from the backend container:

```text
http://awg-rest:18080
```

If the backend runs in the same compose project, it can also use the alias:

```text
http://awg-api:18080
```

## Authentication

Every protected API call needs:

```http
Authorization: Bearer <JWT>
```

Default all-in-one auth uses `HS256` with `JWT_SECRET`. Your backend signs JWTs
with the same secret and these claims:

- `iss`: value of `JWT_ISSUER`
- `aud`: value of `JWT_AUDIENCE`
- `exp`, `nbf`, `iat`
- `roles`: include `platform_admin`, `tenant_admin`, or `automation_client`

For tenant-scoped roles, include `tenant_id`. `platform_admin` can access all
tenants.

For a local smoke test, generate a temporary admin token inside the container:

```bash
JWT="$(docker compose exec -T awg-rest /awg-api -dev-token)"
```

## API Usage

Create a peer:

```bash
curl -sS -X POST "http://127.0.0.1:18080/v1/tenants/default/peers" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: user-123-create-v1" \
  -d '{
    "external_id": "user-123",
    "display_name": "User 123",
    "profile_name": "default-v2"
  }'
```

The first create response includes one-time secret material:

- `private_key`
- `client_config`

Store `client_config` in your backend and deliver it to the user once. It is not
returned again on idempotency replay.

Poll the operation:

```bash
curl -sS "http://127.0.0.1:18080/v1/operations/$OPERATION_ID" \
  -H "Authorization: Bearer $JWT"
```

Read public peer metadata:

```bash
curl -sS "http://127.0.0.1:18080/v1/tenants/default/peers/$PEER_ID" \
  -H "Authorization: Bearer $JWT"
```

Render the non-secret client config skeleton:

```bash
curl -sS "http://127.0.0.1:18080/v1/tenants/default/peers/$PEER_ID/configuration" \
  -H "Authorization: Bearer $JWT"
```

Revoke a peer:

```bash
curl -sS -X POST "http://127.0.0.1:18080/v1/tenants/default/peers/$PEER_ID:revoke" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: user-123-revoke-v1" \
  -d '{"reason":"user disabled"}'
```

OpenAPI contract: [`api/openapi.yaml`](api/openapi.yaml).

## Configuration

Important `.env` values:

| Variable | Meaning |
| --- | --- |
| `AWG_REST_IMAGE` | all-in-one image tag |
| `BOOTSTRAP_NODE_ENDPOINT` | public IP or DNS name placed into client configs |
| `JWT_SECRET` | HMAC signing secret shared only with your backend |
| `AWG_API_BIND` | host binding for REST API, keep loopback-only |
| `AWG_UDP_BIND` | host UDP binding for VPN traffic |
| `AWG_UDP_PORT` | UDP listen port inside client configs |
| `BOOTSTRAP_POOL_CIDR` | VPN client address pool |
| `AWG_INTERNAL_NETWORK` | Docker network for backend-to-API traffic |

The image also supports split API/worker/node-agent deployments through the
separate images `awg-rest-api`, `awg-rest-worker`, and `awg-rest-node-agent`,
but the default single-VPS path is the all-in-one compose file.

## Security Notes

- Keep `AWG_API_BIND=127.0.0.1:18080` unless a private reverse proxy or private
  Docker network protects it.
- Do not publish the node-agent API.
- Rotate `JWT_SECRET` if it was exposed.
- Back up `awg-state`; losing it loses the server private key.
- Do not delete volumes unless you want to recreate the VPN node and reissue
  client configs.

## License

Repository code is MIT licensed. The all-in-one and node-agent images bundle
`amneziawg-tools`, which is GPL-2.0-only upstream software.
