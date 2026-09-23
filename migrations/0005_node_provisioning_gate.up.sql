-- Separate runtime presence from placement policy. A drained node may keep
-- existing peers/runtime while refusing new peer provisioning.

ALTER TABLE vpn_nodes
    ADD COLUMN IF NOT EXISTS accept_new_peers BOOLEAN NOT NULL DEFAULT TRUE;
