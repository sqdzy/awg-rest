-- awg-rest initial schema. Source of truth for the AmneziaWG control plane.
-- All tenant-scoped tables carry tenant_id and are eligible for RLS in
-- production deployments (`ALTER TABLE ... ENABLE ROW LEVEL SECURITY`).

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS tenants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS subjects (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer      TEXT NOT NULL,
    subject     TEXT NOT NULL,
    type        TEXT NOT NULL,            -- 'user' | 'service' | 'node_agent'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

CREATE TABLE IF NOT EXISTS role_bindings (
    tenant_id   UUID REFERENCES tenants(id) ON DELETE CASCADE,
    subject_id  UUID NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    role        TEXT NOT NULL,            -- 'platform_admin' | 'tenant_admin' | 'support_readonly' | 'automation_client' | 'node_agent'
    PRIMARY KEY (tenant_id, subject_id, role)
);

CREATE TABLE IF NOT EXISTS vpn_nodes (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    region            TEXT NOT NULL,
    hostname          TEXT NOT NULL UNIQUE,
    public_endpoint   TEXT NOT NULL,
    base_port         INT  NOT NULL,
    interface_name    TEXT NOT NULL,
    server_public_key TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'unknown', -- 'unknown' | 'ready' | 'degraded' | 'unreachable'
    agent_last_seen_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS protocol_profiles (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL UNIQUE,
    protocol_version TEXT NOT NULL,         -- 'v1' | 'v2'
    jc               INT NOT NULL,
    jmin             INT NOT NULL,
    jmax             INT NOT NULL,
    s1               INT NOT NULL,
    s2               INT NOT NULL,
    s3               INT NOT NULL DEFAULT 0,
    s4               INT NOT NULL DEFAULT 0,
    h1_min           BIGINT NOT NULL,
    h1_max           BIGINT NOT NULL,
    h2_min           BIGINT NOT NULL,
    h2_max           BIGINT NOT NULL,
    h3_min           BIGINT NOT NULL,
    h3_max           BIGINT NOT NULL,
    h4_min           BIGINT NOT NULL,
    h4_max           BIGINT NOT NULL,
    i1               TEXT,
    i2               TEXT,
    i3               TEXT,
    i4               TEXT,
    i5               TEXT,
    listen_port_policy TEXT NOT NULL DEFAULT 'fixed',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS protocol_profiles_version_idx ON protocol_profiles (protocol_version);

CREATE TABLE IF NOT EXISTS address_pools (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
    node_id   UUID NOT NULL REFERENCES vpn_nodes(id) ON DELETE CASCADE,
    cidr      CIDR NOT NULL,
    cursor    INET,
    policy    TEXT NOT NULL DEFAULT 'sequential',
    UNIQUE (node_id, cidr)
);

CREATE TABLE IF NOT EXISTS peers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    node_id             UUID NOT NULL REFERENCES vpn_nodes(id) ON DELETE CASCADE,
    profile_id          UUID NOT NULL REFERENCES protocol_profiles(id),
    external_id         TEXT NOT NULL,
    display_name        TEXT NOT NULL DEFAULT '',
    public_key          TEXT NOT NULL,
    preshared_key_ref   TEXT,
    allowed_ip          INET NOT NULL,
    state               TEXT NOT NULL DEFAULT 'pending',
    desired_revision    BIGINT NOT NULL DEFAULT 1,
    applied_revision    BIGINT NOT NULL DEFAULT 0,
    expires_at          TIMESTAMPTZ,
    revoked_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, external_id),
    UNIQUE (node_id, allowed_ip),
    UNIQUE (node_id, public_key)
);

CREATE INDEX IF NOT EXISTS peers_tenant_state_idx ON peers (tenant_id, state);
CREATE INDEX IF NOT EXISTS peers_node_state_idx ON peers (node_id, state);

CREATE TABLE IF NOT EXISTS peer_runtime (
    peer_id              UUID PRIMARY KEY REFERENCES peers(id) ON DELETE CASCADE,
    last_handshake_at    TIMESTAMPTZ,
    rx_bytes             BIGINT NOT NULL DEFAULT 0,
    tx_bytes             BIGINT NOT NULL DEFAULT 0,
    runtime_present      BOOLEAN NOT NULL DEFAULT FALSE,
    last_runtime_sync_at TIMESTAMPTZ,
    endpoint             TEXT
);

CREATE TABLE IF NOT EXISTS operations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id       UUID REFERENCES peers(id) ON DELETE SET NULL,
    node_id       UUID NOT NULL REFERENCES vpn_nodes(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    request_hash  TEXT,
    error_code    TEXT,
    error_message TEXT,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS operations_status_started_idx ON operations (status, started_at);
CREATE INDEX IF NOT EXISTS operations_peer_idx ON operations (peer_id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    idempotency_key   TEXT NOT NULL,
    request_hash      TEXT NOT NULL,
    operation_id      UUID REFERENCES operations(id) ON DELETE SET NULL,
    response_status   INT  NOT NULL,
    response_body     JSONB NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    node_id         UUID NOT NULL REFERENCES vpn_nodes(id) ON DELETE CASCADE,
    operation_id    UUID REFERENCES operations(id) ON DELETE SET NULL,
    kind            TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}'::JSONB,
    status          TEXT NOT NULL DEFAULT 'pending',  -- 'pending' | 'leased' | 'applied' | 'failed_terminal' | 'failed_retryable'
    attempts        INT  NOT NULL DEFAULT 0,
    leased_until    TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS outbox_status_idx ON outbox (status, leased_until);
CREATE INDEX IF NOT EXISTS outbox_node_status_idx ON outbox (node_id, status);

CREATE TABLE IF NOT EXISTS audit_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID REFERENCES tenants(id) ON DELETE SET NULL,
    subject_id      UUID REFERENCES subjects(id) ON DELETE SET NULL,
    action          TEXT NOT NULL,
    target_type     TEXT NOT NULL,
    target_id       TEXT NOT NULL,
    before_json     JSONB,
    after_json      JSONB,
    request_id      TEXT,
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_tenant_created_idx ON audit_events (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_target_idx ON audit_events (target_type, target_id);

CREATE TABLE IF NOT EXISTS config_snapshots (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     UUID NOT NULL REFERENCES vpn_nodes(id) ON DELETE CASCADE,
    revision    BIGINT NOT NULL,
    config_hash TEXT NOT NULL,
    rendered    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (node_id, revision)
);

CREATE TABLE IF NOT EXISTS usage_rollups (
    peer_id       UUID NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    bucket_start  TIMESTAMPTZ NOT NULL,
    rx_bytes      BIGINT NOT NULL DEFAULT 0,
    tx_bytes      BIGINT NOT NULL DEFAULT 0,
    session_count INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (peer_id, bucket_start)
);
