CREATE OR REPLACE FUNCTION check_alert_offsets_range(offsets INTEGER[])
RETURNS BOOLEAN AS $$
    SELECT bool_and(v BETWEEN 1 AND 180) FROM unnest(offsets) AS v;
$$ LANGUAGE sql IMMUTABLE;

ALTER TABLE routes ADD COLUMN alert_offsets INTEGER[] NOT NULL DEFAULT '{30}';
UPDATE routes SET alert_offsets = ARRAY[alert_offset_mins];
ALTER TABLE routes DROP COLUMN alert_offset_mins;
ALTER TABLE routes ADD CONSTRAINT chk_alert_offsets_length
    CHECK (array_length(alert_offsets, 1) BETWEEN 1 AND 3);
ALTER TABLE routes ADD CONSTRAINT chk_alert_offsets_range
    CHECK (check_alert_offsets_range(alert_offsets));
