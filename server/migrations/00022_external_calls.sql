-- +goose Up
CREATE TABLE external_calls (
 id uuid PRIMARY KEY, seq bigserial UNIQUE NOT NULL, group_id uuid NOT NULL,
 attempt integer NOT NULL CHECK (attempt>0), kind text NOT NULL CHECK (kind IN ('model','search')),
 provider text NOT NULL, model text NOT NULL DEFAULT '', protocol text NOT NULL DEFAULT '',
 query_text varchar(400) NOT NULL DEFAULT '', response_model text NOT NULL DEFAULT '',
 purpose text NOT NULL DEFAULT '', origin text NOT NULL DEFAULT 'app',
 user_id uuid REFERENCES users(id) ON DELETE CASCADE, task_id uuid REFERENCES tasks(id) ON DELETE SET NULL,
 region text NOT NULL DEFAULT '', dispatched boolean NOT NULL,
 status text NOT NULL CHECK (status IN ('running','success','empty','failed','timeout','cancelled','skipped','unknown')),
 error_code text NOT NULL DEFAULT '', request_id text NOT NULL DEFAULT '', http_status integer,
 input_tokens integer, output_tokens integer, cached_tokens integer, result_count integer,
 started_at timestamptz NOT NULL, deadline_at timestamptz NOT NULL, finished_at timestamptz, duration_ms bigint,
 CHECK ((input_tokens IS NULL) = (output_tokens IS NULL))
);
CREATE INDEX external_calls_task_seq_idx ON external_calls(task_id,seq DESC);
CREATE INDEX external_calls_provider_seq_idx ON external_calls(provider,seq DESC);
CREATE INDEX external_calls_started_idx ON external_calls(started_at DESC);
CREATE INDEX external_calls_group_idx ON external_calls(group_id,attempt);
-- +goose Down
DROP TABLE external_calls;
