-- Add AmneziaWG 3.1-only protocol profile parameters.
-- Columns stay nullable so existing V1/V2 rows remain byte-for-byte compatible.

ALTER TABLE protocol_profiles
    ADD COLUMN IF NOT EXISTS header_protection_key TEXT,
    ADD COLUMN IF NOT EXISTS content_padding_addition_min INT,
    ADD COLUMN IF NOT EXISTS content_padding_addition_max INT,
    ADD COLUMN IF NOT EXISTS rekey_after_time_min INT,
    ADD COLUMN IF NOT EXISTS rekey_after_time_max INT,
    ADD COLUMN IF NOT EXISTS rekey_timeout_min INT,
    ADD COLUMN IF NOT EXISTS rekey_timeout_max INT,
    ADD COLUMN IF NOT EXISTS reject_after_time_min INT,
    ADD COLUMN IF NOT EXISTS reject_after_time_max INT,
    ADD COLUMN IF NOT EXISTS keepalive_timeout_min INT,
    ADD COLUMN IF NOT EXISTS keepalive_timeout_max INT,
    ADD COLUMN IF NOT EXISTS max_handshake_attempts_min INT,
    ADD COLUMN IF NOT EXISTS max_handshake_attempts_max INT,
    ADD COLUMN IF NOT EXISTS random_trailers BOOLEAN,
    ADD COLUMN IF NOT EXISTS disable_cookies BOOLEAN;

DO $$
DECLARE
    col TEXT;
BEGIN
    FOREACH col IN ARRAY ARRAY[
        'content_padding_addition_min', 'content_padding_addition_max',
        'rekey_after_time_min', 'rekey_after_time_max',
        'rekey_timeout_min', 'rekey_timeout_max',
        'reject_after_time_min', 'reject_after_time_max',
        'keepalive_timeout_min', 'keepalive_timeout_max',
        'max_handshake_attempts_min', 'max_handshake_attempts_max'
    ]
    LOOP
        BEGIN
            EXECUTE format(
                'ALTER TABLE protocol_profiles ADD CONSTRAINT %I CHECK (%I IS NULL OR (%I BETWEEN 0 AND 65535))',
                'protocol_profiles_' || col || '_u16_check', col, col
            );
        EXCEPTION
            WHEN duplicate_object THEN
                NULL;
        END;
    END LOOP;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_content_padding_order_check
        CHECK (
            content_padding_addition_min IS NULL OR content_padding_addition_max IS NULL OR
            content_padding_addition_min <= content_padding_addition_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_rekey_after_time_order_check
        CHECK (
            rekey_after_time_min IS NULL OR rekey_after_time_max IS NULL OR
            rekey_after_time_min <= rekey_after_time_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_rekey_timeout_order_check
        CHECK (
            rekey_timeout_min IS NULL OR rekey_timeout_max IS NULL OR
            rekey_timeout_min <= rekey_timeout_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_reject_after_time_order_check
        CHECK (
            reject_after_time_min IS NULL OR reject_after_time_max IS NULL OR
            reject_after_time_min <= reject_after_time_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_keepalive_timeout_order_check
        CHECK (
            keepalive_timeout_min IS NULL OR keepalive_timeout_max IS NULL OR
            keepalive_timeout_min <= keepalive_timeout_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    ALTER TABLE protocol_profiles
        ADD CONSTRAINT protocol_profiles_max_handshake_attempts_order_check
        CHECK (
            max_handshake_attempts_min IS NULL OR max_handshake_attempts_max IS NULL OR
            max_handshake_attempts_min <= max_handshake_attempts_max
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;
