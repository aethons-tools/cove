-- The PR watcher's high-water mark (docs/modules/ROOT/pages/usage/jam/work-units.adoc):
-- the id of the last review of a unit's pull request posted to its work log.
ALTER TABLE work_unit_prs ADD COLUMN last_review bigint NOT NULL DEFAULT 0;
