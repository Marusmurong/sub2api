-- 240_usage_logs_upstream_trace_id.sql
-- 身份收敛的排障锚点。
--
-- 统一 user_id / session_id 之后，**我们自己也分不清上游报错是哪个下游客户
-- 触发的了**。统一动作发生在「发出去那一刻」，所以必须在本地留一张
-- 真实用户 ↔ 统一身份 ↔ traceId 的映射，否则无法排障、无法给客户交代。
--
-- 为什么挂在 usage_logs 上：每请求一条，与 traceId 天然一对一。
-- OpsUpstreamErrorEvent 只在错误时记，覆盖不全，只能当补充。
-- 保留期因此自动与 usage_logs 一致。
--
-- 可空：只有 reclaude 链路会写它，其它账号类型恒为 NULL。

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_trace_id VARCHAR(64);

-- 排障入口是「拿着上游报错里的 traceId 反查是谁触发的」，所以按 traceId 查。
-- partial index：绝大多数行该列为 NULL，不必进索引。
CREATE INDEX IF NOT EXISTS idx_usage_logs_upstream_trace_id
    ON usage_logs (upstream_trace_id)
    WHERE upstream_trace_id IS NOT NULL;
