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
  `MaxHandshakeAttempts`, `RandomTrailers`, and `DisableCookies`.
- **FR-7** V3.1 ranges MUST be validated using unsigned protocol bounds.
- **FR-8** V3.1 profiles with `RandomTrailers=on` MUST reject ranged H1-H3
  until upstream packet-classification behavior is proven safe by real E2E.
- **FR-9** Existing V2 render output MUST remain compatible with current clients.
- **FR-10** A V3.1 rollout MUST be possible on a separate node/interface without
  mutating existing V2 peers.

## Non-functional requirements

- **NFR-1 Reproducibility:** upstream runtime sources MUST be pinned by tag and
  exact commit SHA.
- **NFR-2 Rollback:** runtime uplift, schema ownership change, and V3.1 feature
  support SHOULD remain separable commits.
- **NFR-3 Secrets:** HeaderProtectionKey MUST NOT be written to logs or audit
  messages as plaintext. Persistence and snapshot handling require explicit
  review before production enablement.
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
Range-valued 3.x parameters are stored as min/max integer pairs. Boolean toggles
are nullable for legacy rows and required by V3.1 validation.

## API contract

Peer creation may continue accepting `profile_id` / `profile_name` for
backward compatibility, but the resolved value MUST equal the target node
profile. Omitting the profile means inherit the node profile.

A later API cleanup MAY deprecate per-peer profile selection.

## Acceptance criteria

- **AC-1 / FR-1:** the Docker image builds with the pinned 3.1-capable tools and
  userspace runtime while an unchanged V2 profile renders and applies.
- **AC-2 / FR-2:** all existing V1/V2 domain and renderer tests continue passing.
- **AC-3 / FR-3:** reconciliation loads the profile from the node, never from the
  most recently updated peer.
- **AC-4 / FR-4:** peer creation with a mismatched node/profile combination
  returns a validation/conflict error and creates no desired state.
- **AC-5 / FR-5..7:** a valid V3.1 profile round-trips through validation,
  repository persistence, and rendering.
- **AC-6 / FR-8:** V3.1 validation rejects RandomTrailers with ranged H1-H3.
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
7. On the self-hosted real-AWG runner:
   - V2 client -> 3.1-capable runtime
   - V3.1 client -> V3.1 runtime
   - UDP/TCP transfer, rekey, reconnect, MTU boundaries
   - RandomTrailers with fixed H values
   - separate V2 and V3.1 interfaces

## Out of scope for the first implementation slice

- automatic end-user credential migration;
- deleting V2 support;
- enabling ranged H1-H3 with RandomTrailers;
- silently rewriting existing client configs;
- merging the V2 and V3.1 interfaces onto one protocol profile.
