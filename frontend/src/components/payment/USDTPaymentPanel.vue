<template>
  <div class="space-y-4">
    <!-- Terminal states -->
    <template v-if="outcome === 'success'">
      <div class="card p-6">
        <div class="flex flex-col items-center space-y-4 py-4">
          <div class="flex h-16 w-16 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30">
            <Icon name="check" size="lg" class="text-green-500" />
          </div>
          <p class="text-lg font-bold text-gray-900 dark:text-white">
            {{ orderType === 'subscription' ? t('payment.result.subscriptionSuccess') : t('payment.result.success') }}
          </p>
          <button class="btn btn-primary" @click="handleDone">{{ t('common.confirm') }}</button>
        </div>
      </div>
    </template>

    <template v-else-if="outcome === 'cancelled'">
      <div class="card p-6">
        <div class="flex flex-col items-center space-y-4 py-4">
          <p class="text-lg font-bold text-gray-900 dark:text-white">{{ t('payment.qr.cancelled') }}</p>
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('payment.usdt.cancelledDesc') }}</p>
          <button class="btn btn-primary" @click="handleDone">{{ t('common.confirm') }}</button>
        </div>
      </div>
    </template>

    <template v-else-if="outcome === 'expired'">
      <div class="card p-6">
        <div class="flex flex-col items-center space-y-4 py-4">
          <p class="text-lg font-bold text-gray-900 dark:text-white">{{ t('payment.qr.expired') }}</p>
          <!-- Crypto is irreversible and confirmation is not instant, so an
               expired order does NOT mean a late transfer is lost. Saying so
               prevents a support ticket and a panicked second payment. -->
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('payment.usdt.expiredDesc') }}</p>
          <button class="btn btn-primary" @click="handleDone">{{ t('common.confirm') }}</button>
        </div>
      </div>
    </template>

    <!-- Awaiting transfer -->
    <template v-else>
      <div class="card p-6">
        <div class="mb-4 flex items-center justify-between">
          <p class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('payment.usdt.title') }}</p>
          <span class="rounded-md border border-[#26A17B] bg-[#26A17B]/10 px-2 py-0.5 text-xs font-semibold text-[#26A17B]">
            {{ usdt.network }}
          </span>
        </div>

        <div class="flex flex-col items-center space-y-3">
          <div class="rounded-lg border-2 border-[#26A17B] bg-white p-4 dark:bg-dark-800">
            <canvas ref="qrCanvas" class="mx-auto"></canvas>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.usdt.scanHint') }}</p>

          <div class="w-full rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <p class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('payment.usdt.address') }}</p>
            <div class="flex items-center gap-2">
              <span data-test="usdt-address" class="flex-1 break-all font-mono text-sm text-gray-900 dark:text-white">
                {{ usdt.address }}
              </span>
              <button class="btn btn-secondary shrink-0 px-2 py-1 text-xs" @click="copy(usdt.address, 'address')">
                {{ copied === 'address' ? t('common.copied') : t('common.copy') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="card p-6">
        <div class="space-y-2 text-sm">
          <div class="flex justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('payment.usdt.rate') }}</span>
            <span data-test="usdt-rate" class="text-gray-900 dark:text-white">1 USDT ≈ ¥{{ displayRate }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('payment.usdt.orderAmount') }}</span>
            <span class="text-gray-900 dark:text-white">¥{{ payAmount.toFixed(2) }}</span>
          </div>
          <div class="flex items-center justify-between border-t border-gray-200 pt-3 dark:border-dark-600">
            <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.usdt.amountToSend') }}</span>
            <div class="flex items-center gap-2">
              <span data-test="usdt-amount" class="text-xl font-bold tabular-nums text-[#26A17B]">
                {{ usdt.amount_usdt }}
              </span>
              <span class="text-sm text-gray-500 dark:text-gray-400">USDT</span>
              <button class="btn btn-secondary px-2 py-1 text-xs" @click="copy(usdt.amount_usdt, 'amount')">
                {{ copied === 'amount' ? t('common.copied') : t('common.copy') }}
              </button>
            </div>
          </div>
        </div>

        <!-- Both warnings describe losses we cannot undo, so they are stated
             plainly rather than buried in fine print. -->
        <div class="mt-4 space-y-2">
          <p data-test="usdt-exact-warning" class="rounded-md bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-700 dark:bg-amber-950/30 dark:text-amber-300">
            {{ t('payment.usdt.exactAmountWarning', { amount: usdt.amount_usdt }) }}
          </p>
          <p data-test="usdt-network-warning" class="rounded-md bg-red-50 px-3 py-2 text-xs leading-relaxed text-red-700 dark:bg-red-950/30 dark:text-red-300">
            {{ t('payment.usdt.networkWarning', { network: usdt.network }) }}
          </p>
        </div>
      </div>

      <div class="card p-4 text-center">
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('payment.qr.expiresIn') }}</p>
        <p class="mt-1 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">{{ countdownDisplay }}</p>
        <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ t('payment.usdt.waitingConfirmation') }}</p>
      </div>

      <button class="btn btn-secondary w-full" :disabled="cancelling" @click="handleCancel">
        {{ cancelling ? t('common.processing') : t('payment.qr.cancelOrder') }}
      </button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { usePaymentStore } from '@/stores/payment'
import { useAppStore } from '@/stores'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import Icon from '@/components/icons/Icon.vue'
import type { USDTPaymentInfo } from '@/types/payment'

const props = defineProps<{
  orderId: number
  payAmount: number
  expiresAt: string
  usdt: USDTPaymentInfo
  orderType?: string
}>()

type PaymentOutcome = 'success' | 'cancelled' | 'expired'

const emit = defineEmits<{ done: []; success: []; settled: [outcome: PaymentOutcome] }>()

const { t } = useI18n()
const paymentStore = usePaymentStore()
const appStore = useAppStore()

const qrCanvas = ref<HTMLCanvasElement | null>(null)
const remainingSeconds = ref(0)
const cancelling = ref(false)
const copied = ref<'address' | 'amount' | ''>('')
const outcome = ref<PaymentOutcome | null>(null)

let pollTimer: ReturnType<typeof setInterval> | null = null
let countdownTimer: ReturnType<typeof setInterval> | null = null
let copiedTimer: ReturnType<typeof setTimeout> | null = null
let pollInFlight = false

const POLL_INTERVAL_MS = 3000
const COPIED_FEEDBACK_MS = 1500

const displayRate = computed(() => {
  const parsed = Number(props.usdt.rate)
  return Number.isFinite(parsed) ? parsed.toFixed(4) : props.usdt.rate
})

const countdownDisplay = computed(() => {
  const m = Math.floor(remainingSeconds.value / 60)
  const s = remainingSeconds.value % 60
  return `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
})

function setOutcome(next: PaymentOutcome) {
  if (outcome.value === next) return
  outcome.value = next
  emit('settled', next)
}

async function copy(value: string, field: 'address' | 'amount') {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = field
    if (copiedTimer) clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => { copied.value = '' }, COPIED_FEEDBACK_MS)
  } catch {
    // Clipboard access can be denied; the value is on screen and selectable,
    // so this is not worth interrupting the payment with an error dialog.
  }
}

async function renderQR() {
  await nextTick()
  if (!qrCanvas.value || !props.usdt.address) return
  // The payload is the bare address, not a tron: URI — wallet support for
  // payment URIs is inconsistent, while every wallet can scan an address.
  await QRCode.toCanvas(qrCanvas.value, props.usdt.address, {
    width: 200,
    margin: 2,
    errorCorrectionLevel: 'M',
  })
}

function isSuccessStatus(status: string | null | undefined): boolean {
  return status === 'COMPLETED' || status === 'PAID' || status === 'RECHARGING'
}

async function pollStatus() {
  if (!props.orderId || outcome.value || pollInFlight) return
  pollInFlight = true
  try {
    const order = await paymentStore.pollOrderStatus(props.orderId)
    if (!order || outcome.value) return
    if (isSuccessStatus(order.status)) {
      cleanup()
      setOutcome('success')
      emit('success')
    } else if (order.status === 'CANCELLED') {
      cleanup()
      setOutcome('cancelled')
    } else if (order.status === 'EXPIRED' || order.status === 'FAILED') {
      cleanup()
      setOutcome('expired')
    }
  } finally {
    pollInFlight = false
  }
}

function startCountdown(seconds: number) {
  remainingSeconds.value = Math.max(0, seconds)
  if (remainingSeconds.value <= 0) {
    setOutcome('expired')
    return
  }
  countdownTimer = setInterval(() => {
    remainingSeconds.value--
    if (remainingSeconds.value <= 0) {
      setOutcome('expired')
      cleanup()
    }
  }, 1000)
}

async function handleCancel() {
  if (!props.orderId || cancelling.value) return
  cancelling.value = true
  try {
    await paymentAPI.cancelOrder(props.orderId)
    cleanup()
    setOutcome('cancelled')
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    cancelling.value = false
  }
}

function handleDone() {
  cleanup()
  emit('done')
}

function cleanup() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
  if (copiedTimer) { clearTimeout(copiedTimer); copiedTimer = null }
}

onMounted(() => {
  const expiresAt = props.expiresAt ? Date.parse(props.expiresAt) : NaN
  startCountdown(Number.isFinite(expiresAt) ? Math.floor((expiresAt - Date.now()) / 1000) : 30 * 60)
  pollTimer = setInterval(pollStatus, POLL_INTERVAL_MS)
  renderQR()
})

onUnmounted(() => cleanup())
</script>
