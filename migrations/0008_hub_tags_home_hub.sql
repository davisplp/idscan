-- Links a hub_tag to the ONE real hubs.id it officially belongs to.
--
-- This is deliberately a manually-assigned mapping, not an auto-derived
-- one. Migration 0007 explicitly avoided deriving hub_tags from the real
-- hubs table because the card-number-prefix codes were shown to map to
-- MULTIPLE different hubs.id rows in production (a data-quality problem
-- in the legacy data, not something to silently guess around). This
-- column solves a different, narrower problem: "which hub_id is code
-- AIY's own official device" is a fact someone who knows the roster can
-- just state directly, once, per tag -- no inference needed, no
-- ambiguity to resolve.
--
-- Nullable and unpopulated until set: a tag with hub_id IS NULL just
-- means "not configured yet", not an error. The reporting API treats
-- that the same way it already treats a missing pin_hash — a normal,
-- expected state for a tag that hasn't been fully set up.

ALTER TABLE public.hub_tags
    ADD COLUMN IF NOT EXISTS hub_id uuid;

ALTER TABLE public.hub_tags
    ADD CONSTRAINT hub_tags_hub_id_fkey FOREIGN KEY (hub_id)
        REFERENCES public.hubs (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS hub_tags_hub_id_idx
    ON public.hub_tags USING btree (hub_id)
    TABLESPACE pg_default;
