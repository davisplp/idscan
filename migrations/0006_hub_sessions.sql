-- Session storage for hub logins, and a new scanning_hub_id column
-- recording WHICH HUB'S LOGIN SESSION performed a scan -- distinct from
-- the matched student's own hub_id (students.hub_id / attendance_records
-- .hub_id), which records where the STUDENT belongs, not where the
-- physical scan happened. Both are useful, separate facts: e.g. a hub's
-- device could in principle scan a visiting student from another hub.

CREATE TABLE IF NOT EXISTS public.hub_sessions
(
    id            uuid NOT NULL DEFAULT uuid_generate_v4(),
    hub_id        uuid NOT NULL,
    token_hash    text NOT NULL, -- sha256 of the bearer token; the raw
                                  -- token is never stored, only returned
                                  -- to the client once at login time
    created_at    timestamp with time zone NOT NULL DEFAULT now(),
    last_seen_at  timestamp with time zone NOT NULL DEFAULT now(),
    expires_at    timestamp with time zone NOT NULL, -- sliding expiration:
                                                       -- extended on each
                                                       -- authenticated request
    CONSTRAINT hub_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT hub_sessions_hub_id_fkey FOREIGN KEY (hub_id)
        REFERENCES public.hubs (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.hub_sessions
    OWNER to hms;

CREATE UNIQUE INDEX IF NOT EXISTS hub_sessions_token_hash_uidx
    ON public.hub_sessions USING btree (token_hash)
    TABLESPACE pg_default;

CREATE INDEX IF NOT EXISTS hub_sessions_expires_at_idx
    ON public.hub_sessions USING btree (expires_at)
    TABLESPACE pg_default;


ALTER TABLE public.scans
    ADD COLUMN IF NOT EXISTS scanning_hub_id uuid;

ALTER TABLE public.scans
    ADD CONSTRAINT scans_scanning_hub_id_fkey FOREIGN KEY (scanning_hub_id)
        REFERENCES public.hubs (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL;

ALTER TABLE public.attendance_records
    ADD COLUMN IF NOT EXISTS scanning_hub_id uuid;

ALTER TABLE public.attendance_records
    ADD CONSTRAINT attendance_records_scanning_hub_id_fkey FOREIGN KEY (scanning_hub_id)
        REFERENCES public.hubs (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL;
