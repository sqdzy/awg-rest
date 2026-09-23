-- Preserve the pre-multi-node API default deterministically when parallel
-- protocol-generation nodes are introduced.

ALTER TABLE vpn_nodes
    ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT FALSE;

DO $$
DECLARE
    defaults_count INT;
BEGIN
    SELECT COUNT(*) INTO defaults_count FROM vpn_nodes WHERE is_default;
    IF defaults_count > 1 THEN
        RAISE EXCEPTION 'cannot migrate vpn_nodes.is_default: multiple default nodes already exist';
    END IF;

    IF defaults_count = 0 THEN
        UPDATE vpn_nodes
        SET is_default = TRUE
        WHERE id = (
            SELECT id
            FROM vpn_nodes
            ORDER BY hostname
            LIMIT 1
        );
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS vpn_nodes_single_default_idx
    ON vpn_nodes ((is_default))
    WHERE is_default;
