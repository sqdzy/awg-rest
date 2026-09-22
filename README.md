# awg-rest

Internal REST API for provisioning versioned AmneziaWG VPN peers from another
backend container. The control plane supports legacy V1/V2 profiles and staged
AmneziaWG 3.1 rollout.

`awg-rest` is designed for a single private control-plane path:

```text
your backend container -> awg-rest REST API -> embedded Postgres -> embedded worker -> awg/amneziawg-go
```

The default distribution is an all-in-one Docker image. It contains the API,
embedded worker, PostgreSQL, `amneziawg-tools`, and `amneziawg-go` userspace
fallback. The bundled runtime is pinned to a 3.1-capable release while the
bootstrap profile deliberately remains V2 until the V3.1 real-network release
gate is completed.

Do not expose the REST API to the public internet. Publish only the VPN UDP
port.

## Requirements

- Linux VPS with Docker Engine and Docker Compose.
- `/dev/net/tun` available on the host.
- UDP port `38823` open in the VPS firewall, or the port you configure.
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
JWT_SECRET=<output-of-openssl-rand-base64-32>
CLIENT_DNS=1.1.1.1,1.0.0.1
AWG_API_BIND=127.0.0.1:18080
AWG_UDP_BIND=38823
AWG_UDP_PORT=38823
```

Generate `JWT_SECRET` with:

```bash
openssl rand -base64 32
```

Start the stack:

```bash
docker compose up -d
```

What starts:

- `awg-rest` on the internal Docker network `awg-backend-internal`
- REST API on `http://awg-rest:18080` inside Docker
- optional host-local API access on `127.0.0.1:18080`
- VPN UDP listener on `38823/udp`
- persistent volumes `awg-postgres` and `awg-state`

On first start, the container creates:

- tenant `default`
- AmneziaWG V2 profile `default-v2`
- node `awg-node-1`
- address pool `10.200.0.0/24`
- server private key and bootstrap interface config in `awg-state`

The server uses the first usable IPv4 address in the pool, so the default
server address is `10.200.0.1/24` and generated peers start from
`10.200.0.2/32`.

The default `default-v2` profile uses AmneziaWG V2 parameters compatible with
current AmneziaVPN clients, including `Jmin=10`, `Jmax=50`, `S1-S4`, and
non-overlapping `H1-H4` ranges below `2147483647`. It also renders the same
default `I1` special junk packet used by the Amnezia client for new AWG
profiles.

Back up both Docker volumes. `awg-state` contains the server private key.

### Parallel AmneziaWG 3.1 rollout

Existing installations stay on the bootstrap V2 node until an operator explicitly
creates a separate V3.1 node. The supported rollout path does not rewrite V2
profiles or peers.

Expose a second UDP port:

```bash
docker compose -f compose.yaml -f compose.v31.yaml up -d
```

Then create the V3.1 profile, node, address pool, server key and bootstrap
configuration inside the running all-in-one container:

```bash
docker compose exec -T awg-rest /awg-api -provision-v31-node
```

The command is create-only and fails rather than overwriting an existing
profile, hostname, interface, UDP port, bootstrap config, or overlapping CIDR.
Defaults are:

- profile `default-v31`
- node `awg-node-31`
- interface `awg31`
- UDP `38824`
- pool `10.201.0.0/24`
- the same public endpoint, region, NAT setting and egress interface as the
  initial bootstrap node

It prints non-secret JSON including the new `node_id`. Pass that `node_id`
when creating V3.1 peers; omitting `node_id` continues to use the normal
deterministic node selection and should not be relied on for migration.

The generated V3.1 preset follows the parameters currently assigned by the
Amnezia client installer: `S1-S4=12`, fixed `H1-H4=1/2/3/4`, a fresh
HeaderProtectionKey, timings `100-120 / 3-7 / 150-180 / 5-15 / 15-20`,
`PersistentKeepalive=25-35`, `RandomTrailers=on`, `DisableCookies=on`,
and the current default I1 packet. `ContentPaddingAddition` is deliberately
left unset: the current client defines `10-100` as a constant but does not
assign it in `generateAwgParameters()`.

For non-default ports or networks, inspect the CLI options with
`/awg-api -h`. If the external port is translated by NAT, set
`-v31-node-endpoint` to a `host:external-port` value while
`-v31-node-port` remains the local AWG listen port.

Stopping publication of the second UDP port immediately removes the V3.1 path
from external reachability without affecting the original V2 interface. Do not
delete or rewrite V2 profiles during a canary migration.

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
    "display_name": "User 123"
  }'
```

The selected node/interface owns the protocol profile. New peers inherit that
profile automatically. `profile_id` and `profile_name` may still be sent by
older callers, but now act only as assertions and are rejected if they do not
match the node profile. This prevents one peer from changing interface-wide AWG
parameters for every other peer on the same node.

The first create response includes one-time secret material:

- `private_key` when awg-rest generated the client keypair;
- `client_config` on every successful first create;
- `preshared_key`.

When the caller supplied `public_key`, `client_config` intentionally omits
`PrivateKey` but still carries the one-time protocol secrets (including the
V3.1 HeaderProtectionKey and PSK). Merge the locally owned private key before
importing the config. Store/deliver this one-time response securely: secret
fields are not returned again on idempotency replay.

The generated `client_config` is intentionally rendered as a full-tunnel
AmneziaVPN-importable AWG config:

```ini
AllowedIPs = 0.0.0.0/0, ::/0
```

Do not rewrite this field into a custom route list for AmneziaVPN users.
AmneziaVPN applies site/app split tunneling as an application-level setting
after the config is imported. Keeping the imported AWG/WireGuard peer
full-tunnel lets the AmneziaVPN client enable and manage split tunneling from
its own UI.

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

The staged V2 -> V3.1 rollout, compatibility gates, and rollback model are
documented in [`docs/awg31-migration.md`](docs/awg31-migration.md). Existing
V2 profiles are not upgraded in place; V3.1 is intended to run on a separate
node/interface during migration.

## Configuration

Important `.env` values:

| Variable | Meaning |
| --- | --- |
| `AWG_REST_IMAGE` | all-in-one image tag |
| `BOOTSTRAP_NODE_ENDPOINT` | public IP or DNS name placed into client configs |
| `CLIENT_DNS` | comma-separated DNS servers rendered into generated client configs |
| `JWT_SECRET` | HMAC signing secret shared only with your backend |
| `AWG_API_BIND` | host binding for REST API, keep loopback-only |
| `AWG_UDP_BIND` | host UDP binding for VPN traffic |
| `AWG_UDP_PORT` | UDP listen port inside V2 bootstrap client configs |
| `AWG31_UDP_BIND` / `AWG31_UDP_PORT` | optional second published/listen port used by `compose.v31.yaml` |
| `BOOTSTRAP_POOL_CIDR` | VPN client address pool |
| `AWG_INTERNAL_NETWORK` | Docker network for backend-to-API traffic |

## Security Notes

- Keep `AWG_API_BIND=127.0.0.1:18080` unless a private reverse proxy or private
  Docker network protects it.
- Publish only the configured UDP VPN port to the internet.
- Rotate `JWT_SECRET` if it was exposed.
- Treat both persistent volumes and their backups as secret-bearing. Postgres
  stores peer preshared keys and V3.1 HeaderProtectionKey values needed for
  reconciliation; `awg-state` stores the server private key.
- Protect backup storage and access to Docker volumes accordingly. A future
  encryption-at-rest hardening should cover all persisted VPN key material
  together rather than encrypting only one key type.
- Do not delete volumes unless you want to recreate the VPN node and reissue
  client configs.

## License

Repository code is MIT licensed. The all-in-one image bundles
`amneziawg-tools` (GPL-2.0-only) and `amneziawg-go` (MIT). Their license
texts are included under `/usr/share/licenses/`, and the Dockerfile pins the
exact upstream tags and commit SHAs used to build the bundled binaries. The
corresponding `amneziawg-tools` source tree for the shipped GPL binary is also
included in the image at `/usr/share/src/amneziawg-tools`.
