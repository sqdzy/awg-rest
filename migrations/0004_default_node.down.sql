DROP INDEX IF EXISTS vpn_nodes_single_default_idx;
ALTER TABLE vpn_nodes DROP COLUMN IF EXISTS is_default;
