-- Migration: attendance tracking via ID card scans.
--
-- Two tables, deliberately kept separate:
--
--   scans              — every OCR attempt, matched or not. This is the
--                         audit trail / accuracy dataset the project
--                         roadmap called for: it lets you measure OCR
--                         accuracy over time regardless of whether
--                         attendance was actually marked.
--
--   attendance_records — one row per (student, day) that a match was
--                         successfully made and attendance was marked.
--                         Deliberately NOT storing attendance per scan
--                         attempt — re-scanning the same card five times
--                         in one day should not create five records.
--
-- Run with: psql "$DATABASE_URL" -f migrations/0001_attendance.sql

CREATE TABLE IF NOT EXISTS public.scans
(
    id                  uuid NOT NULL DEFAULT uuid_generate_v4(),
    raw_ocr_text        text,
    raw_match           text,
    normalized_phone    text,
    confidence          double precision,
    matched_student_id  uuid,
    created_at          timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT scans_pkey PRIMARY KEY (id),
    CONSTRAINT scans_matched_student_id_fkey FOREIGN KEY (matched_student_id)
        REFERENCES public.students (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.scans
    OWNER to hms;

CREATE INDEX IF NOT EXISTS scans_matched_student_id_idx
    ON public.scans USING btree (matched_student_id ASC NULLS LAST)
    TABLESPACE pg_default;

CREATE INDEX IF NOT EXISTS scans_created_at_idx
    ON public.scans USING btree (created_at DESC)
    TABLESPACE pg_default;


CREATE TABLE IF NOT EXISTS public.attendance_records
(
    id                  uuid NOT NULL DEFAULT uuid_generate_v4(),
    student_id          uuid NOT NULL,
    hub_id              uuid NOT NULL,
    session_date        date NOT NULL,
    scanned_at          timestamp with time zone NOT NULL DEFAULT now(),
    scan_id             uuid,
    created_at          timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT attendance_records_pkey PRIMARY KEY (id),
    CONSTRAINT attendance_records_student_id_fkey FOREIGN KEY (student_id)
        REFERENCES public.students (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE,
    CONSTRAINT attendance_records_hub_id_fkey FOREIGN KEY (hub_id)
        REFERENCES public.hubs (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT attendance_records_scan_id_fkey FOREIGN KEY (scan_id)
        REFERENCES public.scans (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL,
    -- The core rule: one attendance record per student per day. A second
    -- scan of the same card on the same day hits this constraint rather
    -- than creating a duplicate row; the application layer treats that
    -- as an "already marked" outcome, not an error.
    CONSTRAINT attendance_records_one_per_day UNIQUE (student_id, session_date)
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.attendance_records
    OWNER to hms;

CREATE INDEX IF NOT EXISTS attendance_records_hub_date_idx
    ON public.attendance_records USING btree (hub_id ASC NULLS LAST, session_date ASC NULLS LAST)
    TABLESPACE pg_default;

CREATE INDEX IF NOT EXISTS attendance_records_student_idx
    ON public.attendance_records USING btree (student_id ASC NULLS LAST)
    TABLESPACE pg_default;
