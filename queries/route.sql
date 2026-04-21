-- name: CreateRoute :one
INSERT INTO routes (user_id, label, from_station_crs, to_station_crs, departure_time, days_of_week, alert_offsets)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRoutesByUserID :many
SELECT * FROM routes
WHERE user_id = $1
ORDER BY departure_time;

-- name: GetActiveRoutesForWeekday :many
SELECT * FROM routes
WHERE is_active = true
  AND (days_of_week & $1::int) != 0;

-- name: UpdateRouteActive :exec
UPDATE routes SET is_active = $2
WHERE id = $1;

-- name: DeleteRoute :exec
DELETE FROM routes
WHERE id = $1;

-- name: CountRoutesByUserID :one
SELECT count(*) FROM routes
WHERE user_id = $1;

-- name: GetActiveRoutesWithChatID :many
SELECT r.*, u.telegram_chat_id
FROM routes r
JOIN users u ON u.id = r.user_id
WHERE r.is_active = true
  AND (r.days_of_week & $1::int) != 0;

-- name: UpdateRouteDepartureTime :exec
UPDATE routes SET departure_time = $2
WHERE id = $1;

-- name: GetRouteByID :one
SELECT * FROM routes
WHERE id = $1;
