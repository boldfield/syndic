-- The reviewer's reply text of an accepted attempt, as the engine delivered it
-- in the live path (raw_output holds the transport body, which is not it).

ALTER TABLE attempts ADD COLUMN text TEXT;
