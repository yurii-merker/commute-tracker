ALTER TABLE routes DROP CONSTRAINT IF EXISTS chk_alert_offsets_range;
ALTER TABLE routes DROP CONSTRAINT IF EXISTS chk_alert_offsets_length;
ALTER TABLE routes ADD COLUMN alert_offset_mins INTEGER NOT NULL DEFAULT 60 CHECK (alert_offset_mins > 0);
UPDATE routes SET alert_offset_mins = alert_offsets[1];
ALTER TABLE routes DROP COLUMN alert_offsets;
DROP FUNCTION IF EXISTS check_alert_offsets_range;
