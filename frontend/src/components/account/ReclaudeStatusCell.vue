<template>
  <div class="flex flex-col gap-1 text-xs">
    <div class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeUsageToday') }}</span>
      <span v-if="usageLoading" class="text-gray-400">…</span>
      <!-- 🔴 读不到就如实说读不到。回 0 会被读成「今天还没用」，而真相是「我们不知道」。 -->
      <span v-else-if="usageError" class="text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.reclaudeUsageUnavailable') }}
      </span>
      <span v-else class="font-mono" :class="usageClass">{{ formattedUsed }}</span>
    </div>

    <div class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.reclaudeCellCap') }}</span>
      <span v-if="dailyCap > 0" class="font-mono text-gray-900 dark:text-gray-100">
        {{ formattedCap }}
      </span>
      <!-- 上限为 0 = 包络未标定。此时账号根本不会被调度，必须一眼看出来。 -->
      <span v-else class="font-medium text-red-600 dark:text-red-400">
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
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import adminAPI from '@/api/admin'
import { RECLAUDE_DAILY_TOKEN_CAP_EXTRA_KEY } from '@/components/account/reclaudeCredentials'
import type { Account } from '@/types'

/**
 * rec 账号在号池列表里的专属信息。
 *
 * 为什么不复用 AccountUsageCell：那个展示的是 5h / 7d 窗口，而 reclaude 账号
 * **刻意不写会话窗口** —— 上游返回的窗口属于底层那个 Claude 账号，不是我们的
 * 配额包，显示出来就是在假装知道一个我们其实不知道的数。
 *
 * 「今日已用」单独拉一次接口：水位存在 Redis，不在账号列表的响应里。
 * 读失败时显示「不可读」而不是 0 —— 那两者的含义完全不同。
 */
const props = defineProps<{ account: Account }>()

const { t } = useI18n()

const extra = computed(() => (props.account.extra as Record<string, unknown>) || {})
const credentials = computed(() => (props.account.credentials as Record<string, unknown>) || {})

const dailyCap = computed(() => Number(extra.value[RECLAUDE_DAILY_TOKEN_CAP_EXTRA_KEY] ?? 0))

function formatTokens(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${Math.round(value / 1_000)}K`
  return String(value)
}

const formattedCap = computed(() => formatTokens(dailyCap.value))

// 今日水位。每行各拉一次：rec 账号总数是个位数（设备产线一周 1–2 台），
// 不值得为它做批量接口。
const usageLoading = ref(true)
const usageError = ref(false)
const effectiveTokens = ref(0)

onMounted(async () => {
  try {
    const snapshot = await adminAPI.accounts.getReclaudeDailyUsage(props.account.id)
    effectiveTokens.value = snapshot.effective_tokens
  } catch {
    usageError.value = true
  } finally {
    usageLoading.value = false
  }
})

const formattedUsed = computed(() => formatTokens(effectiveTokens.value))

// 越接近上限越醒目。超过上限时账号已经不可调度了，必须一眼看见。
const usageClass = computed(() => {
  if (dailyCap.value <= 0) return 'text-gray-900 dark:text-gray-100'
  const ratio = effectiveTokens.value / dailyCap.value
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
