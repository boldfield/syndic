-- Separate durable run allocation from immutable manifest finalization.
-- Add state column to track run lifecycle: "probing" | "started".
-- Manifest and manifest_hash remain NOT NULL in schema for backward compatibility.
-- The application layer handles NULL manifests by checking the state column.

ALTER TABLE runs ADD COLUMN state TEXT NOT NULL DEFAULT 'started' CHECK (state IN ('probing', 'started'));
