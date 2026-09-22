# AmneziaWG 3.1 migration specification

Status: Approved for implementation  
Scope: staged migration of `awg-rest` from a V2-only control plane to versioned V2 + V3.1 support.

## Context

The current control plane models AmneziaWG V1/V2 profiles and pins pre-3.x
`amneziawg-tools` and `amneziawg-go` builds. AmneziaWG 3.1 adds interface-level
parameters and runtime behavior that must be rolled out without invalidating
existing V2 peers.

The current runtime model has one AmneziaWG interface per node, but a peer stores
its own `profile_id`. Reconciliation selects the profile of the most recently
updated peer on the node and applies it to the entire interface. This is an
invalid ownership boundary for interface-level protocol parameters and must be
fixed before mixed V2/V3.1 operation.

## Functional requirements

- **FR-1** The all-in-one image MUST use a pinned AmneziaWG runtime capable of
  AWG 3.1 while preserving V2 config support.
- **FR-2** Existing V1/V2 protocol profiles MUST remain valid after the migration.
- **FR-3** A node/interface MUST own exactly one protocol profile used by every
  peer reconciled onto that interface.
- **FR-4** Creating a peer with an explicit profile that differs from the node
  profile MUST be rejected.
- **FR-5** The control plane MUST support a distinct protocol version
  `v3.1`; V2 records MUST NOT be upgraded in place.
- **FR-6** V3.1 profiles MUST model and render the upstream 3.x interface
  parameters needed by current Amnezia clients:
  `HeaderProtectionKey`, `ContentPaddingAddition`, `RekeyAfterTime`,
  `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`,
  `MaxHandshakeAttempts`, peer-side `PersistentKeepalive`,
  `RandomTrailers`, and `DisableCookies`.
- **FR-7** V3.1 ranges MUST be validated using unsigned protocol bounds.
- **FR-8** V3.1 profiles with `RandomTrailers=on` MUST reject ranged H1-H3
  until upstream packet-classification behavior is proven safe by real E2E.
  This protects against the still-open upstream `amneziawg-go` issue #186.
- **FR-8a** I1-I5 obfuscation specs MUST reject malformed, negative, and
  unreasonably large fixed-size tags before persistence. The pinned 3.1 runtime
  accepts signed `Atoi` values for `r`/`rc`/`rd`/`dz`; negative values are part
  of the upstream crash class tracked in `amneziawg-go` issue #189.
- **FR-9** Existing V2 render output MUST remain compatible with current clients.
- **FR-10** A V3.1 rollout MUST be possible on a separate node/interface without
  mutating existing V2 peers.

## Non-functional requirements

- **NFR-1 Reproducibility:** upstream runtime sources MUST be pinned by tag and
  exact commit SHA.
- **NFR-2 Rollback:** runtime uplift, schema ownership change, and V3.1 feature
  support SHOULD remain separable commits.
- **NFR-3 Secrets:** HeaderProtectionKey MUST NOT be exposed through logs,
  audit JSON, diagnostic dumps, or non-secret configuration endpoints.
  Persistence uses the same secret-bearing Postgres trust boundary as existing
  peer preshared keys; volumes and backups MUST be protected accordingly.
  A future encryption-at-rest layer SHOULD cover all persisted VPN key material
  together rather than encrypting only one key type.
- **NFR-4 Compatibility:** migrations MUST be additive; `0001_init` is immutable.
- **NFR-5 Verification:** unit, integration, fake-AWG E2E, Docker build, and
  real-AWG E2E form the release gate.

## Data model

### vpn_nodes

Add nullable `profile_id UUID REFERENCES protocol_profiles(id)` first, backfill
from existing peers, validate that each node has at most one distinct historical
profile, then make the column non-null for nodes that participate in peer
provisioning.

Migration MUST fail rather than silently choose a profile if a node has peers
with multiple distinct `profile_id` values.

### protocol_profiles

Keep all V2 columns. Add nullable V3.1-only fields so legacy rows remain valid.
Range-valued 3.x parameters, including `PersistentKeepalive`, are stored as
min/max integer pairs. Boolean toggles
are nullable for legacy rows; V3.1 inserts persist concrete true/false values and
the renderer emits explicit `on`/`off` settings.

## API contract

Peer creation may continue accepting `profile_id` / `profile_name` for
backward compatibility, but the resolved value MUST equal the target node
profile. Omitting the profile means inherit the node profile.

A later API cleanup MAY deprecate per-peer profile selection.


## Operator rollout

The all-in-one installation keeps its original bootstrap node/profile on V2.
A second V3.1 node is created only by an explicit create-only admin command:

```bash
docker compose -f compose.yaml -f compose.v31.yaml up -d
docker compose exec -T awg-rest /awg-api -provision-v31-node
```

The provisioner creates the V3.1 profile, node, non-overlapping address pool,
server key and `0600` bootstrap config in one controlled operation. It rejects
duplicate profile names, hostnames, interface names, local UDP ports and
overlapping pools. Returned JSON omits HeaderProtectionKey and the server private
key. New peers must target the returned `node_id` explicitly during canary
migration.

The V3.1 preset follows the parameters currently assigned by the Amnezia client
installer. `ContentPaddingAddition` is intentionally omitted because the current
client source defines its `10-100` constant but does not assign that field in
`generateAwgParameters()`. This avoids inventing a default that the client
itself is not presently using.

## Acceptance criteria

- **AC-1 / FR-1:** the Docker image builds with the pinned 3.1-capable tools and
  userspace runtime while an unchanged V2 profile renders and applies.
- **AC-2 / FR-2:** all existing V1/V2 domain and renderer tests continue passing.
- **AC-3 / FR-3:** reconciliation loads the profile from the node, never from the
  most recently updated peer.
- **AC-4 / FR-4:** peer creation with a mismatched node/profile combination
  returns a validation/conflict error and creates no desired state.
- **AC-5 / FR-5..7:** a valid V3.1 profile round-trips through validation,
  repository persistence, and rendering, including the 3.x
  `PersistentKeepalive` range.
- **AC-6 / FR-8:** V3.1 validation rejects RandomTrailers with ranged H1-H3.
- **AC-6a / FR-8a:** domain tests reject negative/oversized/malformed I1-I5
  tags before they can reach `amneziawg-go`.
- **AC-7 / FR-9:** V2 golden renderer fixtures remain unchanged.
- **AC-8 / FR-10:** tests can represent separate V2 and V3.1 nodes without
  cross-profile reconciliation.

## Verification matrix

1. `go vet ./...`
2. `go build ./...`
3. `go test -count=1 ./...`
4. `go test -tags=integration ./test/integration/...`
5. `go test -tags=e2e ./test/e2e/...`
6. Build `deploy/docker/Dockerfile.all-in-one`.
7. Hosted real-userspace AWG gate builds the all-in-one image, extracts the
   exact pinned `awg` and `amneziawg-go` binaries, creates Linux network
   namespaces/TUN interfaces, and proves:
   - V2 client -> 3.1-capable userspace runtime
   - V3.1 client -> V3.1 userspace runtime
   - UDP/TCP transfer, rekey, reconnect, MTU boundaries
   - RandomTrailers with fixed H values
8. Optional self-hosted `real-awg` workflow remains available for
   host/kernel-specific validation when required.

## Out of scope for the first implementation slice

- automatic end-user credential migration;
- deleting V2 support;
- enabling ranged H1-H3 with RandomTrailers before upstream #186 is resolved
  and real-network verification passes;
- silently rewriting existing client configs;
- merging the V2 and V3.1 interfaces onto one protocol profile.


## Verification evidence — 2026-09-22

GitHub Actions CI run #148 passed the complete gate on the migration branch,
including the real userspace AWG network E2E. The test built the current
all-in-one image, extracted `amneziawg-tools v3.1.20260812` and
`amneziawg-go v3.1.20260828`, and ran the tunnel in isolated Linux network
namespaces using real TUN interfaces.

Passed real-network scenarios:

- V2 profile on the pinned 3.1-capable userspace runtime;
- V3.1 profile with fixed H1-H4, HeaderProtectionKey, RandomTrailers and
  DisableCookies;
- near-MTU ICMP;
- TCP echo and UDP echo through the encrypted tunnel;
- V3.1 rekey with an observed newer handshake;
- client interface teardown/recreate followed by a fresh handshake and traffic.
