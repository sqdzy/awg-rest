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

CREATE INDEX IF NOT EXISTS vpn_nodes_profile_idx ON vpn_nodes (profile_id);
