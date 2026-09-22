<template>
  <div
    v-if="show"
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    @click.self="$emit('close')"
  >
    <div class="w-full max-w-lg rounded-xl bg-white shadow-xl dark:bg-dark-800">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h3 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.accounts.reclaudeSelfCheck.title') }}
        </h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.reclaudeSelfCheck.description') }}
        </p>
      </div>

      <div class="space-y-3 p-6">
        <div
          v-for="step in displayedSteps"
          :key="step.name"
          class="flex items-start gap-3 rounded-lg border border-gray-100 p-3 dark:border-dark-700"
        >
          <span class="mt-0.5 text-lg leading-none">{{ stepIcon(step) }}</span>
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t(`admin.accounts.reclaudeSelfCheck.steps.${step.name}`) }}
            </div>
            <div
              v-if="step.detail"
              class="mt-0.5 break-words font-mono text-xs text-red-600 dark:text-red-400"
            >
              {{ step.detail }}
            </div>
          </div>
        </div>

        <div
          v-if="result?.bound_email"
          class="rounded-lg bg-gray-50 p-3 text-sm text-gray-700 dark:bg-dark-700 dark:text-gray-300"
        >
          {{ t('admin.accounts.reclaudeSelfCheck.boundEmail') }}: {{ result.bound_email }}
        </div>

        <div
          v-if="result && result.passed && result.activated"
          class="rounded-lg bg-green-50 p-3 text-sm text-green-800 dark:bg-green-900/20 dark:text-green-200"
        >
          {{ t('admin.accounts.reclaudeSelfCheck.activated') }}
        </div>
        <div
          v-else-if="result && !result.passed"
          class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200"
        >
          {{ t('admin.accounts.reclaudeSelfCheck.notActivated') }}
        </div>
        <div
          v-if="errorMessage"
          class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
        >
          {{ errorMessage }}
        </div>
      </div>

      <div class="flex justify-end gap-2 border-t border-gray-100 px-6 py-4 dark:border-dark-700">
        <button type="button" class="btn-secondary" @click="$emit('close')">
          {{ t('common.close') }}
        </button>
        <button type="button" class="btn-primary" :disabled="running" @click="run">
          {{ running ? t('admin.accounts.reclaudeSelfCheck.running') : t('admin.accounts.reclaudeSelfCheck.run') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import adminAPI from '@/api/admin'
import type { ReclaudeSelfCheckResult, ReclaudeSelfCheckStep } from '@/api/admin/accounts'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

/**
 * 建号后的连通性自检：网关可达 → 凭据有效 → 信封可签。
 *
 * 三步全绿后端才会把账号置为可调度；失败**不改动账号状态**，所以这个按钮
 * 可以随便点 —— 一次网络抖动不该把正在跑的账号打下线。
 */
const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; activated: [] }>()

const { t } = useI18n()

const STEP_NAMES = ['gateway_reachable', 'credential_valid', 'envelope_signable'] as const

const running = ref(false)
const result = ref<ReclaudeSelfCheckResult | null>(null)
const errorMessage = ref('')

// 未跑之前也把三步列出来，让人先看到将要发生什么。
const displayedSteps = computed<ReclaudeSelfCheckStep[]>(
  () => result.value?.steps ?? STEP_NAMES.map(name => ({ name, ok: false }))
)

function stepIcon(step: ReclaudeSelfCheckStep): string {
  if (!result.value) return '○'
  return step.ok ? '✅' : '❌'
}

async function run() {
  if (!props.account) return
  running.value = true
  errorMessage.value = ''
  try {
    result.value = await adminAPI.accounts.reclaudeSelfCheck(props.account.id)
    if (result.value.activated) {
      // 账号状态变了，让列表刷新，否则看到的还是 disabled。
      emit('activated')
    }
  } catch (error: unknown) {
    result.value = null
    errorMessage.value = extractApiErrorMessage(
      error,
      t('admin.accounts.reclaudeSelfCheck.failed')
    )
  } finally {
    running.value = false
  }
}

// 每次重新打开都从空白开始：上一次的结果可能已经过时，显示旧结果比不显示更糟。
watch(
  () => props.show,
  visible => {
    if (visible) {
      result.value = null
      errorMessage.value = ''
    }
  }
)
</script>
