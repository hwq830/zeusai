-- name: StartExternalCall :exec
INSERT INTO external_calls
(id,group_id,attempt,kind,provider,model,protocol,purpose,origin,user_id,task_id,region,dispatched,status,started_at,deadline_at,query_text)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'running',$14,$15,$16)
ON CONFLICT (id) DO NOTHING;

-- name: FinishExternalCall :execrows
UPDATE external_calls SET status=$2,error_code=$3,request_id=$4,http_status=$5,
input_tokens=$6,output_tokens=$7,cached_tokens=$8,result_count=$9,finished_at=$10,duration_ms=$11,response_model=$12
WHERE id=$1;

-- name: ListExternalCalls :many
SELECT *, CASE WHEN status='running' AND deadline_at < now() THEN 'unknown' ELSE status END::text AS display_status
FROM external_calls
WHERE (sqlc.narg(before_seq)::bigint IS NULL OR seq < sqlc.narg(before_seq))
 AND (sqlc.narg(kind)::text IS NULL OR kind=sqlc.narg(kind))
 AND (sqlc.narg(provider)::text IS NULL OR provider=sqlc.narg(provider))
 AND (sqlc.narg(model)::text IS NULL OR model=sqlc.narg(model) OR response_model=sqlc.narg(model))
 AND (sqlc.narg(status)::text IS NULL OR (CASE WHEN status='running' AND deadline_at < now() THEN 'unknown' ELSE status END)=sqlc.narg(status))
 AND (sqlc.narg(task_id)::uuid IS NULL OR task_id=sqlc.narg(task_id))
 AND (sqlc.narg(user_id)::uuid IS NULL OR user_id=sqlc.narg(user_id))
 AND (sqlc.narg(since)::timestamptz IS NULL OR started_at>=sqlc.narg(since))
 AND (sqlc.narg(until)::timestamptz IS NULL OR started_at<sqlc.narg(until))
ORDER BY seq DESC LIMIT sqlc.arg(page_limit);
