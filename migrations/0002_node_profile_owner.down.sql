DROP INDEX IF EXISTS vpn_nodes_profile_idx;
ALTER TABLE vpn_nodes DROP COLUMN IF EXISTS profile_id;
