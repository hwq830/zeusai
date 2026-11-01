-- +goose Up
-- Traditional Chinese (zh-Hant) as a display and report language. Its texts are converted from
-- the Simplified source (see i18n.Hant); reports and briefings keep the language they were
-- generated in, so their language columns need no change.
ALTER TABLE users DROP CONSTRAINT users_ui_language_check;
ALTER TABLE users ADD CONSTRAINT users_ui_language_check CHECK (ui_language IN ('zh', 'zh-Hant', 'en'));
ALTER TABLE users DROP CONSTRAINT users_report_language_check;
ALTER TABLE users ADD CONSTRAINT users_report_language_check CHECK (report_language IN ('auto', 'zh', 'zh-Hant', 'en'));

-- +goose Down
UPDATE users SET ui_language = 'zh' WHERE ui_language = 'zh-Hant';
UPDATE users SET report_language = 'zh' WHERE report_language = 'zh-Hant';
ALTER TABLE users DROP CONSTRAINT users_report_language_check;
ALTER TABLE users ADD CONSTRAINT users_report_language_check CHECK (report_language IN ('auto', 'zh', 'en'));
ALTER TABLE users DROP CONSTRAINT users_ui_language_check;
ALTER TABLE users ADD CONSTRAINT users_ui_language_check CHECK (ui_language IN ('zh', 'en'));
