import { useQuery } from '@tanstack/react-query'
import { apiFetch, ApiError } from './api'
import { getAccessToken } from './tokenStore'
import { apiErrorMessage } from './apiErrors'
import type { Lang } from './i18n'

type T = (text: string, vars?: Record<string, string | number>) => string

export type PayMethod = 'card_to_card' | 'zarinpal' | 'nowpayments'

export const METHOD_LABELS: Record<PayMethod, string> = {
  card_to_card: 'Card to card',
  zarinpal: 'ZarinPal',
  nowpayments: 'Crypto (NowPayments)',
}

export type OrderStatus = 'pending' | 'awaiting_review' | 'paid' | 'fulfilled' | 'failed' | 'expired' | 'rejected' | 'cancelled'

export const ORDER_STATUSES: OrderStatus[] = ['pending', 'awaiting_review', 'paid', 'fulfilled', 'failed', 'expired', 'rejected', 'cancelled']

export const STATUS_LABELS: Record<OrderStatus, string> = {
  pending: 'Awaiting payment',
  awaiting_review: 'Awaiting review',
  paid: 'Paid',
  fulfilled: 'Delivered',
  failed: 'Failed',
  expired: 'Expired',
  rejected: 'Rejected',
  cancelled: 'Cancelled',
}

export const KIND_LABELS: Record<string, string> = {
  account_renew: 'Renewal',
  account_purchase: 'New account',
  wallet_topup: 'Wallet top-up',
}

export function statusTone(status: string): 'green' | 'red' | 'amber' | 'slate' | 'blue' {
  switch (status) {
    case 'fulfilled':
      return 'green'
    case 'paid':
      return 'blue'
    case 'awaiting_review':
    case 'pending':
      return 'amber'
    case 'failed':
    case 'rejected':
      return 'red'
    default:
      return 'slate'
  }
}

/** Toman with thousands separators; Persian digits and separators in the Persian UI
 * (Intl's fa-IR output), plain Latin digits in English. The unit word is added by the
 * caller via t('{amount} Toman'). */
export function formatToman(n: number, lang: Lang): string {
  return new Intl.NumberFormat(lang === 'fa' ? 'fa-IR' : 'en-US', { maximumFractionDigits: 0 }).format(n)
}

/** Parse what a person typed into a whole number of Toman: accepts Persian/Arabic
 * digits and thousands separators. Returns NaN for anything else. */
export function parseToman(raw: string): number {
  const ascii = raw
    .replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x06f0))
    .replace(/[٠-٩]/g, (d) => String(d.charCodeAt(0) - 0x0660))
    .replace(/[,٬،\s]/g, '')
  return /^\d+$/.test(ascii) ? Number(ascii) : NaN
}

export interface OrderRow {
  id: string
  kind: string
  status: OrderStatus
  method: PayMethod
  amount_toman: number
  plan_name: string
  account_id: string | null
  account_label: string | null
  reseller_id: string | null
  reseller_username: string | null
  owner: string
  contact: string
  tracking_code: string | null
  has_receipt: boolean
  card?: { number: string; holder: string; bank: string } | null
  gateway_ref: string | null
  reviewer: string
  review_note: string
  fulfil_error: string
  fulfil_attempts: number
  fulfil_in_progress: boolean
  paid_at: string | null
  fulfilled_at: string | null
  expires_at: string
  created_at: string
  updated_at: string
}

export interface TemplatePriceRow {
  template_id: string
  price_toman: number
  purchasable: boolean
}

export function useTemplatePrices() {
  return useQuery({
    queryKey: ['template-prices'],
    queryFn: () => apiFetch<{ items: TemplatePriceRow[] }>('/api/v1/template-prices'),
  })
}

export interface WalletInfo {
  balance_toman: number
  charge_wallet: boolean
  discount_percent: number
  self_service: boolean
  methods: PayMethod[]
}

export interface LedgerEntry {
  id: string
  kind: string
  amount_toman: number
  balance_after: number
  reason: string
  order_id: string | null
  actor: string
  created_at: string
}

export const LEDGER_LABELS: Record<string, string> = {
  topup: 'Top-up',
  manual_credit: 'Manual credit',
  manual_debit: 'Manual debit',
  purchase: 'Purchase',
  refund: 'Refund',
}

/** English source text per sales error code (the server message is technical English). */
const salesCodes: Record<string, string> = {
  insufficient_balance: 'The wallet balance is too low.',
  payment_method_unavailable: 'This payment method is not available.',
  payment_gateway_error: 'The payment gateway could not start the payment. Try again or pick another method.',
  receipt_too_large: 'The receipt image must be at most 2 MB.',
  receipt_type_unsupported: 'The receipt must be a JPEG, PNG or WebP image.',
  receipt_already_used: 'This tracking code or image was already used for another order.',
  invalid_tracking_code: 'The tracking code must be 4 to 32 letters, digits or dashes.',
  receipt_required: 'Attach the receipt image.',
  invalid_contact: 'Enter a Telegram username (like @name) or a phone number.',
  too_many_open_orders: 'You have unpaid orders. Finish them or wait for them to expire.',
  template_not_found: 'This plan is not available.',
  invalid_order_state: 'The order changed in the meantime - reload and check its state.',
  order_not_awaiting_payment: 'A receipt can only be sent while the order waits for payment.',
  no_wallet: 'Only reseller accounts have a wallet.',
  receipt_unavailable: 'This order has no receipt image (none was sent, or it was removed).',
}

export function salesErrorMessage(err: unknown, t: T, fallback: string): string {
  if (err instanceof ApiError && err.code && salesCodes[err.code]) return t(salesCodes[err.code])
  return apiErrorMessage(err, t, fallback)
}

async function readError(res: Response): Promise<ApiError> {
  let message = res.statusText
  let code: string | undefined
  try {
    const body = (await res.json()) as { error?: { message?: string; code?: string } }
    message = body?.error?.message ?? message
    code = body?.error?.code
  } catch {
    // not JSON - keep the status text
  }
  return new ApiError(res.status, message, code)
}

/** Multipart receipt upload (apiFetch is JSON-only). `authed` adds the admin token; the
 * public pages send none. */
export async function uploadReceipt<R>(path: string, trackingCode: string, file: File, authed: boolean): Promise<R> {
  const form = new FormData()
  form.set('tracking_code', trackingCode)
  form.set('receipt', file)
  const token = authed ? getAccessToken() : null
  const res = await fetch(path, { method: 'POST', body: form, headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) throw await readError(res)
  return (await res.json()) as R
}

/** Fetches an authenticated binary (the receipt image) as an object URL. */
export async function fetchBlobUrl(path: string): Promise<string> {
  const token = getAccessToken()
  const res = await fetch(path, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) throw await readError(res)
  return URL.createObjectURL(await res.blob())
}

/** Anonymous JSON call for the public pages (no token, no refresh dance). */
export async function publicFetch<R>(path: string, init: RequestInit = {}): Promise<R> {
  const res = await fetch(path, {
    ...init,
    headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}), ...(init.headers ?? {}) },
  })
  if (!res.ok) throw await readError(res)
  return (await res.json()) as R
}

export const MAX_RECEIPT_BYTES = 2 * 1024 * 1024

/** Decimal GB text for a plan quota. */
export function quotaText(bytes: number | null, t: T): string {
  if (bytes == null) return t('Unlimited')
  return `${Number((bytes / 1e9).toFixed(2))} GB`
}
