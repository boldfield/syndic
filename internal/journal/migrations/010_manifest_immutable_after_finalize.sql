-- Allow manifest to be set once during finalization, then make it immutable.
-- The original trigger prevented all updates. This version allows the initial
-- write from probing state and then locks it after finalization.

DROP TRIGGER runs_manifest_immutable;

CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
WHEN OLD.manifest != ''
BEGIN
	SELECT RAISE(ABORT, 'runs manifest is immutable');
END;
