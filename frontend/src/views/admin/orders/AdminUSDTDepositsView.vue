<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="card p-4">
        <div class="flex flex-wrap items-center gap-3">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.settings.payment.usdtDeposits') }}
            </h2>
            <p class="mt-0.5 max-w-3xl text-xs leading-relaxed text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.payment.usdtDepositsDesc') }}
            </p>
          </div>
          <div class="flex flex-1 items-center justify-end gap-2">
            <Select v-model="statusFilter" :options="statusOptions" class="w-40" @change="fetchDeposits" />
            <button class="btn btn-secondary" :disabled="loading" @click="fetchDeposits">
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>
      </div>

      <div class="card overflow-x-auto p-0">
        <table class="w-full text-sm">
          <thead class="border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-600 dark:text-gray-400">
            <tr>
              <th class="px-4 py-3">{{ t('payment.usdtAdmin.txHash') }}</th>
              <th class="px-4 py-3">{{ t('payment.usdtAdmin.from') }}</th>
              <th class="px-4 py-3 text-right">{{ t('payment.usdtAdmin.amount') }}</th>
              <th class="px-4 py-3">{{ t('payment.usdtAdmin.blockTime') }}</th>
              <th class="px-4 py-3">{{ t('payment.usdtAdmin.status') }}</th>
              <th class="px-4 py-3">{{ t('payment.usdtAdmin.order') }}</th>
              <th class="px-4 py-3 text-right">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="7" class="px-4 py-10 text-center text-gray-400">{{ t('common.loading') }}</td>
            </tr>
            <tr v-else-if="deposits.length === 0">
              <td colspan="7" class="px-4 py-10 text-center text-gray-400">{{ t('payment.usdtAdmin.empty') }}</td>
            </tr>
            <tr
              v-for="deposit in deposits"
              v-else
              :key="deposit.id"
              class="border-b border-gray-100 last:border-0 dark:border-dark-700"
            >
              <td class="px-4 py-3">
                <a
                  :href="`https://tronscan.org/#/transaction/${deposit.tx_hash}`"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="font-mono text-xs text-primary-600 hover:underline dark:text-primary-400"
                >{{ shorten(deposit.tx_hash) }}</a>
              </td>
              <td class="px-4 py-3 font-mono text-xs text-gray-600 dark:text-gray-300">
                {{ shorten(deposit.from_address) }}
              </td>
              <td class="px-4 py-3 text-right font-semibold tabular-nums text-gray-900 dark:text-white">
                {{ deposit.amount_usdt }}
              </td>
              <td class="px-4 py-3 text-xs text-gray-500 dark:text-gray-400">
                {{ formatTime(deposit.block_timestamp) }}
              </td>
              <td class="px-4 py-3">
                <span :class="['rounded-md px-2 py-0.5 text-xs font-medium', statusClass(deposit.status)]">
                  {{ t(`payment.usdtAdmin.statuses.${deposit.status}`) }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-gray-600 dark:text-gray-300">
                {{ deposit.matched_order_id ? `#${deposit.matched_order_id}` : '—' }}
              </td>
              <td class="px-4 py-3 text-right">
                <div v-if="deposit.status === 'UNMATCHED'" class="flex items-center justify-end gap-2">
                  <button class="btn btn-secondary px-2 py-1 text-xs" @click="openBind(deposit)">
                    {{ t('payment.usdtAdmin.bind') }}
                  </button>
                  <button class="btn btn-secondary px-2 py-1 text-xs" @click="handleIgnore(deposit)">
                    {{ t('payment.usdtAdmin.ignore') }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <Pagination
        v-if="total > 0"
        :page="page"
        :total="total"
        :page-size="pageSize"
        @update:page="(value: number) => { page = value; fetchDeposits() }"
        @update:pageSize="(value: number) => { pageSize = value; page = 1; fetchDeposits() }"
      />
    </div>

    <!-- Manual binding -->
    <BaseDialog :show="!!bindTarget" :title="t('payment.usdtAdmin.bindTitle')" @close="bindTarget = null">
      <div v-if="bindTarget" class="space-y-4">
        <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
          <p class="font-mono text-xs break-all text-gray-600 dark:text-gray-300">{{ bindTarget.tx_hash }}</p>
          <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ bindTarget.amount_usdt }} USDT</p>
        </div>
        <div>
          <label class="mb-1 block text-sm text-gray-700 dark:text-gray-300">
            {{ t('payment.usdtAdmin.orderId') }}
          </label>
          <input v-model.number="bindOrderId" type="number" class="input" />
        </div>
        <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="bindForce" type="checkbox" class="mt-0.5" />
          <span>
            {{ t('payment.usdtAdmin.forceLabel') }}
            <!-- Forcing skips the amount check, which is the only thing standing
                 between an operator's typo and crediting the wrong customer. -->
            <span class="block text-xs text-amber-600 dark:text-amber-400">
              {{ t('payment.usdtAdmin.forceHint') }}
            </span>
          </span>
        </label>
        <div class="flex justify-end gap-2">
          <button class="btn btn-secondary" @click="bindTarget = null">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="submitting || !bindOrderId" @click="handleBind">
            {{ submitting ? t('common.processing') : t('common.confirm') }}
          </button>
        </div>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { adminUSDTAPI, type USDTDeposit } from '@/api/admin/payment'

const { t } = useI18n()
const appStore = useAppStore()

const deposits = ref<USDTDeposit[]>([])
const loading = ref(false)
const submitting = ref(false)
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
// Unmatched first: those are the ones needing a human.
const statusFilter = ref('UNMATCHED')

const bindTarget = ref<USDTDeposit | null>(null)
const bindOrderId = ref<number | null>(null)
const bindForce = ref(false)

const statusOptions = computed(() => [
  { value: 'UNMATCHED', label: t('payment.usdtAdmin.statuses.UNMATCHED') },
  { value: 'MATCHED', label: t('payment.usdtAdmin.statuses.MATCHED') },
  { value: 'IGNORED', label: t('payment.usdtAdmin.statuses.IGNORED') },
  { value: '', label: t('payment.usdtAdmin.allStatuses') },
])

function shorten(value: string): string {
  if (!value || value.length <= 16) return value || '—'
  return `${value.slice(0, 8)}…${value.slice(-6)}`
}

function formatTime(value: string): string {
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? new Date(parsed).toLocaleString() : value
}

function statusClass(status: string): string {
  if (status === 'MATCHED') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (status === 'IGNORED') return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
  return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
}

async function fetchDeposits() {
  loading.value = true
  try {
    const res = await adminUSDTAPI.listDeposits({
      status: statusFilter.value || undefined,
      page: page.value,
      page_size: pageSize.value,
    })
    deposits.value = res.data?.items ?? []
    total.value = res.data?.total ?? 0
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

function openBind(deposit: USDTDeposit) {
  bindTarget.value = deposit
  bindOrderId.value = null
  bindForce.value = false
}

async function handleBind() {
  if (!bindTarget.value || !bindOrderId.value) return
  submitting.value = true
  try {
    await adminUSDTAPI.bindDeposit(bindTarget.value.id, bindOrderId.value, bindForce.value)
    appStore.showSuccess(t('payment.usdtAdmin.bindSuccess'))
    bindTarget.value = null
    await fetchDeposits()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    submitting.value = false
  }
}

async function handleIgnore(deposit: USDTDeposit) {
  try {
    await adminUSDTAPI.ignoreDeposit(deposit.id, 'reviewed by admin')
    await fetchDeposits()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  }
}

onMounted(fetchDeposits)
</script>
