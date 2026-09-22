-- 239_reclaude_device_id_unique.sql
-- V-4：reclaude 账号的 device_id 全局唯一。
--
-- 为什么必须在 DB 层做，而不是只靠应用层 check：
-- 一套设备凭据建成两行 account，会让**同一台设备被当成两个池子调度** ——
-- 并发翻倍，直接打穿单设备包络；而并发建号时应用层的 SELECT-then-INSERT 必漏。
--
-- credentials 是 JSONB 且没有唯一索引，所以用表达式 partial unique index。
-- 条件里同时带上 type 与 deleted_at：
--   * 只约束 reclaude 账号，不影响其它类型（它们的 credentials 里没有这个键）
--   * 软删除的账号不占用唯一位置，允许删后用同一套凭据重建

CREATE UNIQUE INDEX IF NOT EXISTS accounts_reclaude_device_id_unique_active
    ON accounts ((credentials ->> 'reclaude_device_id'))
    WHERE type = 'reclaude'
      AND deleted_at IS NULL
      AND credentials ->> 'reclaude_device_id' IS NOT NULL;
