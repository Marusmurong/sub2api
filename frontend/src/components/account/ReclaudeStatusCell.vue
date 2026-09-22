<template>
  <div class="flex flex-col gap-1 text-xs">
    <div v-if="tierLabel" class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeCellTier') }}</span>
      <span class="font-medium text-gray-900 dark:text-gray-100">{{ tierLabel }}</span>
    </div>

    <div class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeUsageToday') }}</span>
      <span class="font-mono" :class="usageClass">{{ formattedUsed }}</span>
      <template v-if="dailyLimit > 0">
        <span class="text-gray-400">/</span>
        <span class="font-mono text-gray-700 dark:text-gray-300">{{ formattedLimit }}</span>
      </template>
    </div>

    <!-- 限额为 0 = 包络未标定。此时账号根本不会被调度，必须一眼看出来。 -->
    <div v-if="dailyLimit <= 0" class="flex items-center gap-1.5">
      <span class="font-medium text-red-600 dark:text-red-400">
        {{ t('admin.accounts.reclaudeCellCapMissing') }}
      </span>
    </div>

    <div v-if="boundEmail" class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeCellBound') }}</span>
      <span class="truncate text-gray-700 dark:text-gray-300">{{ boundEmail }}</span>
    </div>

    <div v-if="lastSwitchedAt" class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeCellSwitched') }}</span>
      <span class="text-gray-700 dark:text-gray-300">{{ lastSwitchedAt }}</span>
    </div>

    <div v-if="clientVersion" class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeCellClient') }}</span>
      <span class="font-mono text-gray-700 dark:text-gray-300">{{ clientVersion }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { findReclaudePlanTier, RECLAUDE_PLAN_TIER_EXTRA_KEY } from '@/components/account/reclaudeCredentials'
import type { Account } from '@/types'

/**
 * rec 账号在号池列表里的专属信息。
 *
 * 为什么不复用 AccountUsageCell：那个展示的是 5h / 7d 窗口，而 reclaude 账号
 * **刻意不写会话窗口** —— 上游返回的窗口属于底层那个 Claude 账号，不是我们的
 * 配额包，显示出来就是在假装知道一个我们其实不知道的数。
 *
 * 🔴 日用量口径与其它账号类型一致：**美元**，直接读账号行的 quota_daily_used。
 * 早先这里单独拉 Redis 的 token 水位，导致页面上两类账号显示两种东西、对不上账；
 * 限额改为按套餐档位换算成美元后，两边归一到同一个数。
 */
const props = defineProps<{ account: Account }>()

const { t } = useI18n()

const extra = computed(() => (props.account.extra as Record<string, unknown>) || {})
const credentials = computed(() => (props.account.credentials as Record<string, unknown>) || {})

const dailyLimit = computed(() => Number(extra.value.quota_daily_limit ?? 0))
const dailyUsed = computed(() => Number(extra.value.quota_daily_used ?? 0))

// 历史账号可能没有档位键 —— 缺席时不显示标签，限额照常显示（限额才是生效值）。
const tierLabel = computed(() => {
  const id = String(extra.value[RECLAUDE_PLAN_TIER_EXTRA_KEY] ?? '')
  return id ? (findReclaudePlanTier(id)?.label ?? '') : ''
})

function formatUSD(value: number): string {
  return `$${value.toFixed(2)}`
}

const formattedUsed = computed(() => formatUSD(dailyUsed.value))
const formattedLimit = computed(() => formatUSD(dailyLimit.value))

// 越接近上限越醒目。超过上限时账号已经不可调度了，必须一眼看见。
const usageClass = computed(() => {
  if (dailyLimit.value <= 0) return 'text-gray-900 dark:text-gray-100'
  const ratio = dailyUsed.value / dailyLimit.value
  if (ratio >= 1) return 'text-red-600 dark:text-red-400 font-medium'
  if (ratio >= 0.8) return 'text-amber-600 dark:text-amber-400'
  return 'text-gray-900 dark:text-gray-100'
})

const boundEmail = computed(() => String(extra.value.reclaude_bound_email ?? ''))
const clientVersion = computed(() => String(credentials.value.reclaude_client_version ?? ''))

// 换号频率 = 供给稳定性指标。只到日期，列表里不需要精确到秒。
const lastSwitchedAt = computed(() => {
  const raw = String(extra.value.reclaude_last_switched_at ?? '')
  if (!raw) return ''
  const parsed = new Date(raw)
  return Number.isNaN(parsed.getTime()) ? '' : parsed.toLocaleDateString()
})
</script>
