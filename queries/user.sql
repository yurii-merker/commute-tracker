-- name: CreateUser :one
INSERT INTO users (telegram_chat_id, state)
VALUES ($1, $2)
ON CONFLICT (telegram_chat_id) DO NOTHING
RETURNING *;

-- name: GetUserByChatID :one
SELECT * FROM users
WHERE telegram_chat_id = $1;

-- name: UpdateUserState :exec
UPDATE users SET state = $2
WHERE telegram_chat_id = $1;

-- name: GetActiveChatIDsForWeekday :many
SELECT DISTINCT u.telegram_chat_id
FROM users u
JOIN routes r ON r.user_id = u.id
WHERE r.is_active = true
  AND (r.days_of_week & (1 << $1::int)) != 0;

-- name: ToggleSystemAlerts :one
UPDATE users SET system_alerts = NOT system_alerts
WHERE telegram_chat_id = $1
RETURNING system_alerts;

-- name: GetSystemAlertsChatIDs :many
SELECT DISTINCT u.telegram_chat_id
FROM users u
WHERE u.system_alerts = true;
