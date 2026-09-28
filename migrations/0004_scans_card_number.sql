-- Adds card-number tracking to the scans audit table, now that scanning
-- can match a student via card_number (preferred) or phone (fallback).
--
-- matched_via records which field actually identified the student on scans
-- that succeeded ('card_number' | 'phone'), NULL when unmatched or when
-- OCR found nothing at all. This is exactly the kind of thing the scans
-- table exists for: once there's enough real scan volume, a query like
--
--   SELECT matched_via, count(*) FROM scans GROUP BY matched_via;
--
-- tells you how often each match path is actually being used in practice
-- — useful for deciding whether it's worth pushing harder on getting
-- card_number backfilled/printed clearly versus phone number.

ALTER TABLE public.scans
    ADD COLUMN IF NOT EXISTS card_number text,
    ADD COLUMN IF NOT EXISTS matched_via text;

ALTER TABLE public.scans
    ADD CONSTRAINT scans_matched_via_check
    CHECK (matched_via IS NULL OR matched_via IN ('card_number', 'phone'));
