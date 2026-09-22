-- Assign AmneziaWG protocol configuration to the runtime owner (node/interface).
-- Existing peer.profile_id is retained for compatibility/history, but new writes
-- must match the node-owned profile.

ALTER TABLE vpn_nodes
    ADD COLUMN IF NOT EXISTS profile_id UUID REFERENCES protocol_profiles(id);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM peers
        GROUP BY node_id
        HAVING COUNT(DISTINCT profile_id) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot migrate vpn_nodes.profile_id: at least one node has peers with multiple protocol profiles';
    END IF;
END
$$;

UPDATE vpn_nodes AS n
SET profile_id = p.profile_id
FROM (
    SELECT DISTINCT node_id, profile_id
    FROM peers
) AS p
WHERE n.id = p.node_id
  AND n.profile_id IS NULL;

-- A fresh/idle legacy node may have no peers yet. If the installation has
-- exactly one protocol profile, ownership is still unambiguous and can be
-- backfilled safely.
UPDATE vpn_nodes
SET profile_id = (SELECT id FROM protocol_profiles LIMIT 1)
WHERE profile_id IS NULL
  AND (SELECT COUNT(*) FROM protocol_profiles) = 1;

-- If profiles remain ambiguous for any node, stop the upgrade instead of
-- leaving the API unable to provision new peers onto that node.
DO $
BEGIN
    IF EXISTS (SELECT 1 FROM vpn_nodes WHERE profile_id IS NULL)
       AND (SELECT COUNT(*) FROM protocol_profiles) > 1 THEN
        RAISE EXCEPTION
            'cannot migrate vpn_nodes.profile_id: at least one node has no peers and multiple protocol profiles exist; assign a node profile explicitly before upgrading';
    END IF;
END
$;

CREATE INDEX IF NOT EXISTS vpn_nodes_profile_idx ON vpn_nodes (profile_id);
