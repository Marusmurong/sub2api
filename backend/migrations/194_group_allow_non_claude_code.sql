-- 分组级「允许非 Claude Code 客户端」开关。
--
-- 背景：非 CC 拦截此前只看全局配置 gateway.reject_non_claude_code_clients，
-- 后台分组页的「Claude Code 客户端限制」对 /v1/messages 毫无效果——实测
-- ToB-03/ToB-04 两个分组 claude_code_only 为 false（界面显示「允许所有客户端」），
-- 非 CC 流量仍被全部拒绝，界面与实际行为长期不一致。
--
-- 本字段默认 false ＝ 强制 Claude Code，与全局开关打开时的既有行为完全一致，
-- 因此新增列不需要迁移任何既有数据；置 true 才解除该分组的强制。
--
-- 为什么不复用 claude_code_only：
--   1. 它默认 false 的含义是「不限制」，拿它当判据会让全部未配置的分组在开关生效
--      的瞬间同时放行非 CC（本站为 15 个 anthropic 分组，含两个大流量下游）；
--   2. 它还被 responses / chat_completions 两个端点**跨平台**读取，改动其语义会
--      连带影响 OpenAI、Gemini、Grok 分组在那两个端点上的行为。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS allow_non_claude_code BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.allow_non_claude_code IS '是否允许非 Claude Code 客户端（默认否＝强制 Claude Code）';

-- 该字段进入 API Key 认证快照，必须纳入耐久失效触发器的比对列表。
--
-- 否则带外修改（直接改库、或应用层失效与更新之间崩溃）会留下仍在强制 CC 的
-- 陈旧快照，表现为「后台开关打开了但请求照样被拒」——正是本次要修的那类问题。
-- 正常的后台保存已经走 InvalidateAuthCacheByGroupID，本触发器是兜底。
-- 函数体基于 193_group_profit_control_auth_cache_invalidation.sql 的最新版本。
CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id BIGINT;
BEGIN
    target_group_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.is_exclusive IS NOT DISTINCT FROM NEW.is_exclusive
       AND OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation
       AND OLD.platform IS NOT DISTINCT FROM NEW.platform
       AND OLD.subscription_type IS NOT DISTINCT FROM NEW.subscription_type
       AND OLD.rate_multiplier IS NOT DISTINCT FROM NEW.rate_multiplier
       AND OLD.peak_rate_enabled IS NOT DISTINCT FROM NEW.peak_rate_enabled
       AND OLD.peak_start IS NOT DISTINCT FROM NEW.peak_start
       AND OLD.peak_end IS NOT DISTINCT FROM NEW.peak_end
       AND OLD.peak_rate_multiplier IS NOT DISTINCT FROM NEW.peak_rate_multiplier
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
       AND OLD.claude_code_only IS NOT DISTINCT FROM NEW.claude_code_only
       AND OLD.allow_non_claude_code IS NOT DISTINCT FROM NEW.allow_non_claude_code
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;

    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.group_id = target_group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
