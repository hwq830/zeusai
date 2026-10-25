-- +goose Up
-- Chinese / English. ui_language is the language the app shows (the client sends its resolved
-- choice, so it is always zh or en); briefings, pushes and messages follow it. report_language
-- is the default for new reports: auto follows ui_language. Each report and briefing keeps the
-- language it was generated in.
ALTER TABLE users ADD COLUMN ui_language text NOT NULL DEFAULT 'zh' CHECK (ui_language IN ('zh', 'en'));
ALTER TABLE users ADD COLUMN report_language text NOT NULL DEFAULT 'auto' CHECK (report_language IN ('auto', 'zh', 'en'));
ALTER TABLE reports ADD COLUMN language text NOT NULL DEFAULT 'zh';
ALTER TABLE briefings ADD COLUMN language text NOT NULL DEFAULT 'zh';

-- Optional English texts of an official template, see template.Translation. Missing keys fall
-- back to the Chinese columns.
ALTER TABLE templates ADD COLUMN translations jsonb NOT NULL DEFAULT '{}';

-- Optional English push; English-language users get the Chinese text when it is empty.
ALTER TABLE campaigns ADD COLUMN title_en text NOT NULL DEFAULT '';
ALTER TABLE campaigns ADD COLUMN body_en text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE campaigns DROP COLUMN body_en;
ALTER TABLE campaigns DROP COLUMN title_en;
ALTER TABLE templates DROP COLUMN translations;
ALTER TABLE briefings DROP COLUMN language;
ALTER TABLE reports DROP COLUMN language;
ALTER TABLE users DROP COLUMN report_language;
ALTER TABLE users DROP COLUMN ui_language;
