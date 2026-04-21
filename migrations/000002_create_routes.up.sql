CREATE TABLE routes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    from_station_crs TEXT NOT NULL,
    to_station_crs TEXT NOT NULL,
    departure_time TIME NOT NULL,
    days_of_week INTEGER NOT NULL DEFAULT 31 CHECK (days_of_week BETWEEN 0 AND 127),
    alert_offset_mins INTEGER NOT NULL DEFAULT 60 CHECK (alert_offset_mins > 0),
    is_active BOOLEAN NOT NULL DEFAULT true
);

CREATE INDEX idx_routes_user_id ON routes(user_id);
CREATE INDEX idx_routes_active ON routes(is_active) WHERE is_active = true;
