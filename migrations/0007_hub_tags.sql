-- Introduces hub_tags: a small, clean, authoritative set of scan-session
-- identities (AIY, ASA, YOM, ...), completely decoupled from the existing
-- hubs table.
--
-- WHY A SEPARATE TABLE, NOT hubs.login_code (added in migration 0005):
-- The card-number prefix codes (AIY, EWI, ...) were already shown to map
-- to MULTIPLE different hubs.id values in production (see the
-- card_number / hub_id investigation earlier in this project) — e.g. EWI
-- spans 5 distinct hub_id rows, GKP spans 10. That means the existing
-- hubs table has duplicate/fragmented rows for what should conceptually
-- be one hub. Rather than trying to silently merge or guess which hubs.id
-- row is "the real one" per code — a data-quality problem outside this
-- migration's scope to fix — hub_tags is a fresh table seeded directly
-- from an authoritative code->name list, with no dependency on hubs at
-- all. migrations/0005's hubs.login_code/pin_hash columns are superseded
-- by this and can be left unused (not dropped, to avoid a destructive
-- migration on columns that may or may not have been populated yet).

CREATE TABLE IF NOT EXISTS public.hub_tags
(
    id                     uuid NOT NULL DEFAULT uuid_generate_v4(),
    code                   text NOT NULL,
    name                   text NOT NULL,
    pin_hash               text,
    failed_login_attempts  integer NOT NULL DEFAULT 0,
    locked_until           timestamp with time zone,
    created_at             timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT hub_tags_pkey PRIMARY KEY (id),
    CONSTRAINT hub_tags_code_key UNIQUE (code)
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.hub_tags
    OWNER to hms;

-- Session storage, mirroring hub_sessions (migration 0006) but scoped to
-- hub_tags instead of hubs.
CREATE TABLE IF NOT EXISTS public.hub_tag_sessions
(
    id            uuid NOT NULL DEFAULT uuid_generate_v4(),
    hub_tag_id    uuid NOT NULL,
    token_hash    text NOT NULL,
    created_at    timestamp with time zone NOT NULL DEFAULT now(),
    last_seen_at  timestamp with time zone NOT NULL DEFAULT now(),
    expires_at    timestamp with time zone NOT NULL,
    CONSTRAINT hub_tag_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT hub_tag_sessions_hub_tag_id_fkey FOREIGN KEY (hub_tag_id)
        REFERENCES public.hub_tags (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.hub_tag_sessions
    OWNER to hms;

CREATE UNIQUE INDEX IF NOT EXISTS hub_tag_sessions_token_hash_uidx
    ON public.hub_tag_sessions USING btree (token_hash)
    TABLESPACE pg_default;

CREATE INDEX IF NOT EXISTS hub_tag_sessions_expires_at_idx
    ON public.hub_tag_sessions USING btree (expires_at)
    TABLESPACE pg_default;

-- Scan attribution now points at hub_tags, not hubs. The scanning_hub_id
-- columns added in migration 0006 are left in place but unused/deprecated
-- (again, avoiding a destructive migration) — scanning_hub_tag_id is the
-- one the application actually writes to going forward.
ALTER TABLE public.scans
    ADD COLUMN IF NOT EXISTS scanning_hub_tag_id uuid;

ALTER TABLE public.scans
    ADD CONSTRAINT scans_scanning_hub_tag_id_fkey FOREIGN KEY (scanning_hub_tag_id)
        REFERENCES public.hub_tags (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL;

ALTER TABLE public.attendance_records
    ADD COLUMN IF NOT EXISTS scanning_hub_tag_id uuid;

ALTER TABLE public.attendance_records
    ADD CONSTRAINT attendance_records_scanning_hub_tag_id_fkey FOREIGN KEY (scanning_hub_tag_id)
        REFERENCES public.hub_tags (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL;

-- Seed the authoritative code -> name list. pin_hash is left NULL for all
-- rows — each tag needs a PIN set before it can log in (see README for
-- the setup command). ON CONFLICT so this migration is safe to re-run.
INSERT INTO public.hub_tags (code, name) VALUES
    ('AIY', 'Advocacy Initiative for Youth Development'),
    ('ASA', 'ASAL iLab'),
    ('BAY', 'Bayak Nhial'),
    ('CWU', 'Code With Us Hub'),
    ('EWI', 'EmpowerWithIT'),
    ('FAU', 'Faulu Centre'),
    ('GAH', 'Garissa Augab Hub'),
    ('GAU', 'Garissa University'),
    ('GKP', 'Girl Kind Power Hub'),
    ('HOP', 'Hope Community Hub'),
    ('HSD', 'Human Shine Dreams'),
    ('IFO', 'Ifo Main'),
    ('KAN', 'Kanamkemer ICT Hub'),
    ('KIC', 'Kalobeyei Innovative Center (Village 2)'),
    ('KNC', 'KNCCI Hub Garissa'),
    ('KSA', 'Kalobeyei Sustainable Action'),
    ('KUA', 'KUA Initiative'),
    ('LFT', 'Lift C.B.O'),
    ('LTC', 'Lodwar Technical College'),
    ('NEN', 'NENAP Hub'),
    ('NGV', 'Nguvu-Teach Hub'),
    ('NIE', 'NieHub'),
    ('NNP', 'North Eastern National Polytechnic'),
    ('NNT', 'Nyot Nyuol Teny'),
    ('PHI', 'Peace House Innovation Ltd'),
    ('RAI', 'Resilience Action International'),
    ('RCK', 'RCK Reception Centre Kalobeyei'),
    ('REM', 'Remote'),
    ('RFH', 'Remote Fugee Hub'),
    ('SAV', 'SAVIC Hub'),
    ('SUN', 'Sunshine College'),
    ('TSD', 'Transformative Skills Digital Hub'),
    ('YID', 'Youth Initiative for Development (YID)'),
    ('YOM', 'Yomlat'),
    ('NO-HUB', 'Hub not in PLP network')
ON CONFLICT (code) DO NOTHING;
