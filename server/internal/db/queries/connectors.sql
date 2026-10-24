-- name: ListConnectorsForUser :many
SELECT c.*,
       coalesce(uc.enabled, c.default_on)::bool AS enabled,
       (cc.user_id IS NOT NULL)::bool          AS authorized
FROM connectors c
LEFT JOIN user_connectors uc ON uc.connector_id = c.id AND uc.user_id = $1
LEFT JOIN connector_credentials cc ON cc.connector_id = c.id AND cc.user_id = $1
WHERE c.available
ORDER BY c.sort_order, c.id;

-- name: GetConnectorForUser :one
SELECT c.*,
       coalesce(uc.enabled, c.default_on)::bool AS enabled,
       (cc.user_id IS NOT NULL)::bool          AS authorized
FROM connectors c
LEFT JOIN user_connectors uc ON uc.connector_id = c.id AND uc.user_id = $1
LEFT JOIN connector_credentials cc ON cc.connector_id = c.id AND cc.user_id = $1
WHERE c.available AND c.id = $2;

-- name: SetUserConnector :exec
INSERT INTO user_connectors (user_id, connector_id, enabled) VALUES ($1, $2, $3)
ON CONFLICT (user_id, connector_id) DO UPDATE SET enabled = excluded.enabled, updated_at = now();

-- name: UpsertCredential :exec
INSERT INTO connector_credentials (user_id, connector_id, ciphertext, key_id, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, connector_id) DO UPDATE SET
    ciphertext = excluded.ciphertext, key_id = excluded.key_id, expires_at = excluded.expires_at, created_at = now();

-- name: DeleteCredential :exec
DELETE FROM connector_credentials WHERE user_id = $1 AND connector_id = $2;

-- name: UpsertConnector :exec
INSERT INTO connectors (id, name, description, icon_url, kind, category, requires_auth, default_on, tools,
                        transport, mcp_version, available, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
    kind = excluded.kind, requires_auth = excluded.requires_auth, tools = excluded.tools,
    transport = excluded.transport, mcp_version = excluded.mcp_version,
    -- Fields editable in the admin console keep their edits once a row is admin-managed.
    name = CASE WHEN connectors.admin_managed THEN connectors.name ELSE excluded.name END,
    description = CASE WHEN connectors.admin_managed THEN connectors.description ELSE excluded.description END,
    icon_url = CASE WHEN connectors.admin_managed THEN connectors.icon_url ELSE excluded.icon_url END,
    category = CASE WHEN connectors.admin_managed THEN connectors.category ELSE excluded.category END,
    default_on = CASE WHEN connectors.admin_managed THEN connectors.default_on ELSE excluded.default_on END,
    available = CASE WHEN connectors.admin_managed THEN connectors.available ELSE excluded.available END,
    sort_order = CASE WHEN connectors.admin_managed THEN connectors.sort_order ELSE excluded.sort_order END;
