import { ShieldAlert, Users } from 'lucide-react'
import { useI18n } from '../../lib/i18n'
import type { AccountAbuseExtras } from '../../lib/abuse'
import { Badge } from '../ui/Badge'

/** Row badges for the abuse signals an account list response carries. */
export function AbuseBadges({ possiblyShared, torrentHits24h }: { possiblyShared: boolean; torrentHits24h: number }) {
  const { t } = useI18n()
  return (
    <>
      {possiblyShared && (
        <Badge tone="amber" title={t('Active from several different networks at once. Open the Devices tab for details.')}>
          <Users className="h-3 w-3" />
          {t('possibly shared')}
        </Badge>
      )}
      {torrentHits24h > 0 && (
        <Badge tone="red" title={t('Blocked BitTorrent packets in the last 24 hours')}>
          <ShieldAlert className="h-3 w-3" />
          {t('torrent ×{n}', { n: torrentHits24h })}
        </Badge>
      )}
    </>
  )
}

/** Devices-tab section: why the account is flagged as possibly shared (networks, IPs,
 * nodes, last seen), the device-limit counting mode, and torrent history. The wording
 * stays honest: both signals are heuristics. */
export function AbuseDetails({ extras }: { extras: AccountAbuseExtras | undefined }) {
  const { t, formatDate } = useI18n()
  if (!extras) return null
  const { sharing, torrent } = extras
  return (
    <div className="mt-4 space-y-4">
      {extras.device_limit_mode && (
        <p className="text-xs leading-relaxed text-muted">
          {extras.device_limit_mode === 'ip'
            ? t('The device limit counts distinct source IPs seen in the last {n} minutes (ports are ignored).', { n: extras.device_window_minutes ?? 5 })
            : t('The device limit counts distinct ip:port endpoints seen in the last {n} minutes.', { n: extras.device_window_minutes ?? 5 })}
        </p>
      )}
      {sharing && sharing.suspected && (
        <div className="rounded-lg border border-warning/30 bg-warning-soft/40 p-3 text-sm">
          <p className="flex items-center gap-1.5 font-medium text-warning-fg">
            <Users className="h-4 w-4" />
            {t('Possibly shared')}
          </p>
          <p className="mt-1 text-xs leading-relaxed text-muted">
            {t(
              'Active from {n} or more different networks within {m} minutes. This is a heuristic: roaming between mobile networks can look like sharing, and users behind carrier-grade NAT can hide it.',
              { n: sharing.min_networks, m: sharing.window_minutes },
            )}
          </p>
        </div>
      )}
      {sharing && sharing.networks.length > 0 && (sharing.suspected || sharing.networks.length > 1) && (
        <div>
          <p className="mb-1.5 text-xs font-semibold tracking-wider text-faint uppercase">{t('Networks in the last {m} minutes', { m: sharing.window_minutes })}</p>
          <div className="space-y-1.5">
            {sharing.networks.map((n) => (
              <div key={n.network} className="rounded-lg border border-edge px-3 py-2 text-xs">
                <div className="flex items-center justify-between gap-3">
                  <span className="ltr font-mono text-fg">{n.network}</span>
                  <span className="text-muted">{formatDate(n.last_seen_at, { dateStyle: 'short', timeStyle: 'short' })}</span>
                </div>
                <p className="ltr mt-1 break-all font-mono text-muted">{n.ips.join(', ')}</p>
                <p className="mt-0.5 text-muted">{t('via {name}', { name: n.nodes.join(', ') })}</p>
              </div>
            ))}
          </div>
        </div>
      )}
      {torrent && torrent.hits_30d > 0 && (
        <div>
          <p className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold tracking-wider text-faint uppercase">
            <ShieldAlert className="h-3.5 w-3.5" />
            {t('Torrent attempts')}
          </p>
          <p className="text-xs leading-relaxed text-muted">
            {t('{a} blocked packets in the last 24 hours, {b} in the last 30 days. Counts are packets, not attempts: one blocked connection is retried several times.', {
              a: torrent.hits_24h,
              b: torrent.hits_30d,
            })}
          </p>
          <div className="mt-2 space-y-1">
            {torrent.recent.slice(0, 5).map((r, i) => (
              <div key={`${r.at}-${i}`} className="flex items-center justify-between gap-3 rounded-lg border border-edge px-3 py-1.5 text-xs">
                <span className="text-muted">{formatDate(r.at, { dateStyle: 'short', timeStyle: 'short' })}</span>
                <span className="text-muted">
                  {t('via {name}', { name: r.node_name })} · <span className="tabular-nums text-fg">{r.hits}</span>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
