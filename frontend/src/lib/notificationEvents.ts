import type { AppNotification } from './notifications'

/** Subscribable event types, in the order the settings UI lists them. Keep in sync
 * with backend/internal/events. `label` is English source text for t(). */
export const EVENT_CATALOG: { type: string; label: string; severity: 'info' | 'warning' | 'critical' }[] = [
  { type: 'node.down', label: 'Node down', severity: 'critical' },
  { type: 'node.recovered', label: 'Node recovered', severity: 'info' },
  { type: 'account.created', label: 'Account created', severity: 'info' },
  { type: 'account.deleted', label: 'Account deleted', severity: 'info' },
  { type: 'account.suspended', label: 'Account suspended', severity: 'warning' },
  { type: 'account.enabled', label: 'Account enabled', severity: 'info' },
  { type: 'account.quota_80', label: 'Account used 80% of its data', severity: 'warning' },
  { type: 'account.quota_exhausted', label: 'Account data quota exhausted', severity: 'warning' },
  { type: 'account.expiring_soon', label: 'Account expires soon', severity: 'warning' },
  { type: 'account.expired', label: 'Account expired', severity: 'warning' },
  { type: 'reseller.credit_low', label: 'Reseller data credit is low', severity: 'warning' },
  { type: 'reseller.plan_expiring', label: 'Reseller plan expires soon', severity: 'warning' },
  { type: 'backup.succeeded', label: 'Backup completed', severity: 'info' },
  { type: 'backup.failed', label: 'Backup failed', severity: 'critical' },
  { type: 'payment.awaiting_review', label: 'Payment awaiting review', severity: 'warning' },
  { type: 'payment.approved', label: 'Payment approved', severity: 'info' },
  { type: 'payment.rejected', label: 'Payment rejected', severity: 'info' },
  { type: 'order.fulfilled', label: 'Order fulfilled', severity: 'info' },
  { type: 'wallet.credited', label: 'Wallet credited', severity: 'info' },
]

export function eventLabel(type: string): string {
  return EVENT_CATALOG.find((e) => e.type === type)?.label ?? type
}

type T = (text: string, vars?: Record<string, string | number>) => string

/** Toman with separators in the language the page is currently showing. */
function tomanText(n: unknown): string {
  const fa = document.documentElement.lang === 'fa'
  return new Intl.NumberFormat(fa ? 'fa-IR' : 'en-US', { maximumFractionDigits: 0 }).format(Number(n) || 0)
}

function bytes(n: unknown): string {
  const v = Number(n) || 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)} GB`
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)} MB`
  return `${v} B`
}

/** Title and body of a notification in the UI language. The server stores English;
 * this re-renders known events from their structured data so Persian users get whole
 * natural sentences, and falls back to the stored English text for anything else. */
export function notificationText(
  n: AppNotification,
  t: T,
  formatDate: (v: string) => string,
): { title: string; body: string } {
  const d = (n.data ?? {}) as Record<string, unknown>
  const s = (k: string) => String(d[k] ?? '')
  const title = EVENT_CATALOG.some((e) => e.type === n.event) ? t(eventLabel(n.event)) : n.title
  switch (n.event) {
    case 'node.down':
      return { title, body: t('Node "{node}" stopped sending heartbeats and is now offline.', { node: s('node_name') }) }
    case 'node.recovered':
      return { title, body: t('Node "{node}" is sending heartbeats again.', { node: s('node_name') }) }
    case 'account.quota_80':
      return {
        title,
        body: t('Account "{label}" has used {used} of its {quota} data quota.', {
          label: s('label'),
          used: bytes(d.used_bytes),
          quota: bytes(d.quota_bytes),
        }),
      }
    case 'account.quota_exhausted':
      return {
        title,
        body: t('Account "{label}" has used its whole data quota ({quota}) and is suspended.', {
          label: s('label'),
          quota: bytes(d.quota_bytes),
        }),
      }
    case 'account.expiring_soon':
      return {
        title,
        body: t('Account "{label}" expires within 3 days ({date}).', {
          label: s('label'),
          date: d.expiry_at ? formatDate(s('expiry_at')) : '',
        }),
      }
    case 'account.expired':
      return { title, body: t('Account "{label}" has expired.', { label: s('label') }) }
    case 'reseller.credit_low':
      return {
        title,
        body: t('Reseller "{name}" has allocated {percent}% of its data credit.', {
          name: s('reseller_username'),
          percent: Number(d.percent) || 0,
        }),
      }
    case 'reseller.plan_expiring':
      return {
        title,
        body: t('The plan of reseller "{name}" ends on {date}.', {
          name: s('reseller_username'),
          date: d.expires_at ? formatDate(s('expires_at')) : '',
        }),
      }
    case 'backup.succeeded':
      return {
        title,
        body: t('Backup {file} ({size}) was sent to {dest}.', { file: s('filename'), size: bytes(d.size_bytes), dest: s('destination') }),
      }
    case 'backup.failed':
      return { title, body: t('Backup to {dest} failed: {error}', { dest: s('destination'), error: s('error') }) }
    case 'payment.awaiting_review':
      return {
        title,
        body: t('A payment of {amount} Toman ({who}) needs your review: open Payments to approve or reject it.', {
          amount: tomanText(d.amount_toman),
          who: s('label') || s('reseller_username') || s('contact'),
        }),
      }
    case 'wallet.credited':
      return {
        title,
        body: t('{amount} Toman was added to the wallet. Balance: {balance} Toman.', {
          amount: tomanText(d.amount_toman),
          balance: tomanText(d.balance_toman),
        }),
      }
    default:
      return { title: n.title, body: n.body }
  }
}
