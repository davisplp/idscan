-- Adds hub-level login credentials, so a scanning session can be tied to
-- a specific hub rather than being anonymous/open.
--
-- Deliberately additive only (ADD COLUMN), same reasoning as the earlier
-- card_number migration: this is safe regardless of whatever else exists
-- on this table already, and doesn't require knowing its full schema.
--
-- login_code is a short, human-typeable identifier for a hub to log in
-- with, chosen fresh here -- NOT reused from the card-number-derived
-- prefix codes (e.g. "AIY", "EWI"). Those codes were already shown (see
-- conversation history / migrations/0002 investigation) to map to
-- MULTIPLE different hub_id values in this data, so using them as a login
-- identity would inherit that same ambiguity. login_code is a fresh,
-- deliberately-assigned 1:1 mapping to a single hubs.id.
--
-- pin_hash stores a bcrypt hash of a short numeric PIN -- never the PIN
-- itself. A short PIN has low entropy on its own; failed_attempts and
-- locked_until (below) are the real defense against brute-forcing it,
-- not the hash algorithm.

ALTER TABLE public.hubs
    ADD COLUMN IF NOT EXISTS login_code text,
    ADD COLUMN IF NOT EXISTS pin_hash text,
    ADD COLUMN IF NOT EXISTS failed_login_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS locked_until timestamp with time zone;

-- No UNIQUE constraint yet, same reasoning as card_number: add it only
-- once login_code has been assigned and verified duplicate-free.
-- Filtered on deleted_at IS NULL to match this table's existing
-- soft-delete convention (confirmed via \d public.hubs) — a
-- soft-deleted hub's old login_code doesn't block reassigning it.
--
-- CREATE UNIQUE INDEX IF NOT EXISTS hubs_login_code_uidx
--     ON public.hubs USING btree (login_code ASC NULLS LAST)
--     WHERE login_code IS NOT NULL AND deleted_at IS NULL;
