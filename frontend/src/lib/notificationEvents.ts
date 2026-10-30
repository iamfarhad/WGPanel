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
  { type: 'reseller.child_created', label: 'Sub-reseller created', severity: 'info' },
  { type: 'reseller.child_updated', label: 'Sub-reseller updated', severity: 'info' },
  { type: 'reseller.child_deleted', label: 'Sub-reseller deleted', severity: 'info' },
  { type: 'backup.succeeded', label: 'Backup completed', severity: 'info' },
  { type: 'backup.failed', label: 'Backup failed', severity: 'critical' },
  { type: 'node.maintenance_started', label: 'Node maintenance started', severity: 'info' },
  { type: 'node.maintenance_ended', label: 'Node maintenance ended', severity: 'info' },
  { type: 'security.refresh_reuse', label: 'Session token reuse detected', severity: 'critical' },
  { type: 'account.sharing_suspected', label: 'Account may be shared', severity: 'warning' },
  { type: 'account.torrent_detected', label: 'Torrent traffic blocked', severity: 'warning' },
]

export function eventLabel(type: string): string {
  return EVENT_CATALOG.find((e) => e.type === type)?.label ?? type
}

type T = (text: string, vars?: Record<string, string | number>) => string

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
    case 'node.maintenance_started':
      return { title, body: t('Node "{node}" is in maintenance: no new peers or steering until it ends.', { node: s('node_name') }) }
    case 'node.maintenance_ended':
      return { title, body: t('Node "{node}" is back in rotation.', { node: s('node_name') }) }
    case 'security.refresh_reuse':
      return {
        title,
        body: t('A refresh token of admin "{admin}" was replayed after rotation. All their sessions from that login were revoked.', { admin: s('admin') }),
      }
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
    case 'account.sharing_suspected':
      return {
        title,
        body: t('Account "{label}" is active from {n} different networks at once, so it may be shared.', {
          label: s('label'),
          n: Number(d.networks) || 0,
        }),
      }
    case 'account.torrent_detected':
      return {
        title,
        body:
          t('Account "{label}" tried to use BitTorrent ({n} blocked packets).', { label: s('label'), n: Number(d.hits) || 0 }) +
          (d.suspended === true ? ` ${t('The account was suspended.')}` : ''),
      }
    default:
      return { title: n.title, body: n.body }
  }
}
