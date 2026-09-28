-- Optional but recommended: makes phone-number lookups during attendance
-- scanning use an index instead of a sequential scan over all students.
--
-- The match query needs to compare on "last 9 digits, ignoring formatting"
-- because existing phone_number values in this table are a mix of
-- formats (+254..., 0740..., with/without spaces). A plain index on
-- phone_number wouldn't help a LIKE '%...' style query, but an expression
-- index on the normalized suffix does.
--
-- Run this any time after 0001 — it's independent and safe to skip if
-- your student roster is small enough that a sequential scan is fine.

CREATE INDEX IF NOT EXISTS students_phone_suffix_idx
    ON public.students USING btree (
        (right(regexp_replace(phone_number, '\D', '', 'g'), 9))
    )
    TABLESPACE pg_default
    WHERE deleted_at IS NULL;
