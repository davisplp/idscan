-- Adds card_number to students: the physical/printed ID card number
-- (e.g. "PLP-26-YOM-001-S9Z"), sourced from the existing student roster
-- spreadsheet.
--
-- Deliberately nullable with no constraints yet:
--   - Nullable: any existing INSERT into students that doesn't reference
--     this column keeps working unchanged -- nothing elsewhere in the
--     codebase needs to be touched for this migration to be safe.
--   - No UNIQUE constraint yet: add one only after backfilling and
--     confirming there are no duplicate or blank card numbers in the
--     existing data (a handful of rows in the source roster had a blank
--     Card No., and hand-maintained rosters commonly have at least one
--     accidental duplicate).
--
-- Run this BEFORE backfill_card_number.sql.

ALTER TABLE public.students
    ADD COLUMN IF NOT EXISTS card_number text;

-- Once backfilled and verified clean (see the SELECT below), consider
-- adding a uniqueness guarantee. Matches the existing partial-unique-index
-- convention already used on this table (see students_user_id_uidx /
-- students_application_response_id_uidx), so a card holder who is
-- soft-deleted doesn't block reissuing that card number:
--
-- CREATE UNIQUE INDEX IF NOT EXISTS students_card_number_uidx
--     ON public.students USING btree (card_number ASC NULLS LAST)
--     WHERE card_number IS NOT NULL AND deleted_at IS NULL;

-- Run this after backfilling to check for duplicates before adding the
-- unique index above -- if this returns any rows, resolve them first,
-- since the CREATE UNIQUE INDEX will otherwise fail outright:
--
-- SELECT card_number, count(*)
-- FROM public.students
-- WHERE card_number IS NOT NULL AND deleted_at IS NULL
-- GROUP BY card_number
-- HAVING count(*) > 1;
