-- Marzban-compatible API facade (docs/marzban-compat.md): one panel-wide switch.
--
-- Off by default. While FALSE every Marzban route (/api/admin/token, /api/user/...)
-- answers 404 exactly as if it were not compiled in, so a panel that never sells
-- through a Telegram bot exposes no extra surface. There is deliberately no per-key
-- flag: the credentials are the existing Bot API keys, and revoking a key already cuts
-- its compat access (bearer tokens are re-checked against the key on every request).
ALTER TABLE panel_settings ADD COLUMN marzban_compat_enabled BOOLEAN NOT NULL DEFAULT FALSE;
