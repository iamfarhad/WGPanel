import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import QRCode from 'qrcode'
import { Apple, Check, Copy, Download, Laptop, MonitorSmartphone, Smartphone } from 'lucide-react'
import { useI18n, readStoredLang } from '../lib/i18n'
import { ltr } from '../lib/bidi'
import { BrandLockup, Illustration } from '../components/brand/Brand'
import { LanguageToggle } from '../components/ui/LanguageToggle'
import { Button } from '../components/ui/Button'
import { Badge } from '../components/ui/Badge'
import { Card } from '../components/ui/Card'
import { Progress } from '../components/ui/Progress'
import { Select } from '../components/ui/Select'
import { expiryInfo } from '../lib/expiry'
import { SubscriptionShop } from '../components/sales/SubscriptionShop'

interface SubNode {
  node_id: string
  name: string
  region: string
  online: boolean
  recommended: boolean
  config_path: string
}

interface SubInfo {
  label: string
  status: 'active' | 'suspended'
  status_reason: 'expired' | 'quota_exceeded' | null
  data_quota_bytes: number | null
  data_used_bytes: number
  expiry_at: string | null
  days_left: number | null
  device_limit: number | null
  devices_active: number
  support_contact: string | null
  config_path: string
  nodes: SubNode[]
}

class SubError extends Error {
  status: number
  constructor(status: number) {
    super(`HTTP ${status}`)
    this.status = status
  }
}

async function fetchInfo(token: string): Promise<SubInfo> {
  const res = await fetch(`/api/v1/sub/${encodeURIComponent(token)}/info`, { headers: { Accept: 'application/json' } })
  if (!res.ok) throw new SubError(res.status)
  return (await res.json()) as SubInfo
}

/** Decimal units, matching how quotas are set (1 GB = 1e9 bytes). */
function formatBytes(bytes: number): string {
  if (bytes <= 0) return ltr('0 GB')
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1000)))
  const v = bytes / 1000 ** i
  return ltr(`${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`)
}

const APPS = [
  { id: 'android', name: 'Android', icon: Smartphone, href: 'https://play.google.com/store/apps/details?id=com.wireguard.android' },
  { id: 'ios', name: 'iPhone / iPad', icon: Apple, href: 'https://apps.apple.com/app/wireguard/id1441195209' },
  { id: 'windows', name: 'Windows', icon: Laptop, href: 'https://download.wireguard.com/windows-client/wireguard-installer.exe' },
  { id: 'macos', name: 'macOS', icon: MonitorSmartphone, href: 'https://apps.apple.com/app/wireguard/id1451685025' },
] as const

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  const { t } = useI18n()
  return (
    <Card className="p-5">
      <h2 className="mb-3 text-sm font-semibold text-fg">{t(title)}</h2>
      {children}
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  const { t } = useI18n()
  return (
    <div>
      <p className="text-xs text-muted">{t(label)}</p>
      <p className="mt-0.5 text-sm font-medium text-fg tabular-nums">{value}</p>
    </div>
  )
}

export function SubscriptionPage() {
  const { token = '' } = useParams()
  const { t, lang, setLang, formatDate } = useI18n()

  // This page's audience is mostly Persian speakers: default to Persian unless the
  // visitor already chose a language (the toggle persists their choice as usual).
  useEffect(() => {
    let stored: string | null = null
    try {
      stored = localStorage.getItem('wgpanel-lang')
    } catch {
      // storage blocked - treat as no stored choice
    }
    if (!stored && readStoredLang() !== 'fa') setLang('fa')
    // run once on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const infoQuery = useQuery({
    queryKey: ['public-subscription', token],
    queryFn: () => fetchInfo(token),
    retry: (count, err) => !(err instanceof SubError) && count < 2,
    refetchInterval: 60_000,
  })
  const info = infoQuery.data

  const [nodeId, setNodeId] = useState('')
  const configPath = info
    ? (info.nodes.find((n) => n.node_id === nodeId)?.config_path ?? info.nodes.find((n) => n.recommended)?.config_path ?? info.config_path)
    : ''

  const [qr, setQr] = useState<string | null>(null)
  const [qrFailed, setQrFailed] = useState(false)
  const active = info?.status === 'active'
  useEffect(() => {
    if (!active || !configPath) return
    let cancelled = false
    setQr(null)
    setQrFailed(false)
    fetch(configPath, { headers: { Accept: 'application/octet-stream' } })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      // Rendered locally: the config holds the account's private key and goes nowhere else.
      .then((text) => QRCode.toDataURL(text, { width: 280, margin: 1 }))
      .then((url) => !cancelled && setQr(url))
      .catch(() => !cancelled && setQrFailed(true))
    return () => {
      cancelled = true
    }
  }, [active, configPath])

  const [copied, setCopied] = useState(false)
  function copyLink() {
    navigator.clipboard
      .writeText(`${window.location.origin}/api/v1/sub/${encodeURIComponent(token)}`)
      .then(() => {
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      })
      .catch(() => undefined)
  }

  const header = (
    <header className="mx-auto flex w-full max-w-xl items-center justify-between gap-3 px-4 pt-5">
      <BrandLockup />
      <LanguageToggle />
    </header>
  )

  if (infoQuery.isLoading) {
    return (
      <div className="min-h-dvh bg-bg">
        {header}
        <p className="mt-16 text-center text-sm text-muted">{t('Loading…')}</p>
      </div>
    )
  }

  if (infoQuery.isError || !info) {
    const status = infoQuery.error instanceof SubError ? infoQuery.error.status : 0
    return (
      <div className="min-h-dvh bg-bg">
        {header}
        <main className="mx-auto max-w-xl px-4 py-12 text-center">
          <Illustration name="error" size={160} className="mx-auto" />
          <h1 className="mt-4 text-lg font-semibold text-fg">
            {status === 429 ? t('Too many requests') : status === 404 ? t('This link is not valid') : t('Could not load your subscription')}
          </h1>
          <p className="mt-2 text-sm leading-relaxed text-muted">
            {status === 429
              ? t('Please wait a minute and reload the page.')
              : status === 404
                ? t('Check that you copied the whole link, or ask your provider for a new one.')
                : t('Check your internet connection and try again.')}
          </p>
          {status !== 404 && (
            <Button className="mt-5" onClick={() => infoQuery.refetch()}>
              Try again
            </Button>
          )}
        </main>
      </div>
    )
  }

  const quota = info.data_quota_bytes
  const used = info.data_used_bytes
  const pct = quota && quota > 0 ? Math.min(100, (used / quota) * 100) : 0
  const exp = expiryInfo(info.expiry_at, 7)
  const daysText =
    exp.state === 'none'
      ? t('No expiry')
      : exp.state === 'expired'
        ? t('Expired')
        : exp.state === 'today'
          ? t('Expires today')
          : t('{n} days left', { n: exp.days })

  const statusLabel = active
    ? t('Active')
    : info.status_reason === 'expired'
      ? t('Expired')
      : info.status_reason === 'quota_exceeded'
        ? t('Data used up')
        : t('Suspended')

  return (
    <div className="min-h-dvh bg-bg pb-12">
      {header}
      <main className="mx-auto mt-5 w-full max-w-xl space-y-4 px-4">
        <Card className="p-5">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="text-xs text-muted">{t('Your subscription')}</p>
              <h1 className="mt-0.5 truncate text-lg font-semibold text-fg">{info.label}</h1>
            </div>
            <Badge dot tone={active ? 'green' : 'amber'}>
              {statusLabel}
            </Badge>
          </div>

          {!active && (
            <div className="mt-4 flex items-center gap-3 rounded-lg bg-warning-soft p-3 text-warning-fg">
              <Illustration name="offline" size={56} className="shrink-0" />
              <p className="text-sm leading-relaxed">
                {info.status_reason === 'expired'
                  ? t('This subscription has expired. Contact your provider to renew it.')
                  : info.status_reason === 'quota_exceeded'
                    ? t('The data allowance is used up. Contact your provider to add more.')
                    : t('This subscription is suspended. Contact your provider.')}
              </p>
            </div>
          )}

          <div className="mt-5">
            <div className="mb-1.5 flex items-baseline justify-between gap-2 text-sm">
              <span className="font-medium text-fg">{t('Traffic')}</span>
              <span className="text-muted tabular-nums">
                {quota == null ? t('{used} used - unlimited', { used: formatBytes(used) }) : ltr(`${formatBytes(used)} / ${formatBytes(quota)}`)}
              </span>
            </div>
            {quota != null ? <Progress value={used} max={quota} /> : <Progress value={0} max={1} />}
            {quota != null && (
              <p className="mt-1.5 text-xs text-muted">{t('{pct}% used', { pct: Math.round(pct) })}</p>
            )}
          </div>

          <div className="mt-5 grid grid-cols-2 gap-4">
            <Stat label="Time left" value={daysText} />
            <Stat
              label="Expires on"
              value={info.expiry_at ? formatDate(info.expiry_at, { year: 'numeric', month: 'long', day: 'numeric' }) : '—'}
            />
            <Stat
              label="Devices"
              value={
                info.device_limit != null
                  ? t('{n} of {limit} in use', { n: info.devices_active, limit: info.device_limit })
                  : t('{n} in use', { n: info.devices_active })
              }
            />
          </div>
        </Card>

        <SubscriptionShop token={token} />

        {active && (
          <Section title="Connect">
            {info.nodes.length > 1 && (
              <div className="mb-4">
                <label className="mb-1.5 block text-xs text-muted" htmlFor="sub-node">
                  {t('Server')}
                </label>
                <Select id="sub-node" value={nodeId || info.nodes.find((n) => n.recommended)?.node_id || ''} onChange={(e) => setNodeId(e.target.value)}>
                  {info.nodes.map((n) => (
                    <option key={n.node_id} value={n.node_id}>
                      {n.name}
                      {n.region ? ` (${n.region})` : ''}
                      {n.recommended ? ` - ${t('recommended')}` : ''}
                    </option>
                  ))}
                </Select>
              </div>
            )}
            <div className="flex flex-col items-center gap-4">
              <div className="flex h-[216px] w-[216px] items-center justify-center rounded-xl border border-edge bg-white p-2">
                {qr ? (
                  <img src={qr} alt={t('QR code for the WireGuard app')} width={200} height={200} />
                ) : qrFailed ? (
                  <p className="px-3 text-center text-xs text-slate-600">{t('QR code unavailable - use the download button.')}</p>
                ) : (
                  <p className="text-xs text-slate-500">{t('Loading…')}</p>
                )}
              </div>
              <p className="text-center text-xs leading-relaxed text-muted">
                {t('On a phone, open the WireGuard app, tap + and choose Scan QR code. On a computer, download the file and import it.')}
              </p>
              <div className="flex w-full flex-col gap-2 sm:flex-row">
                <a
                  href={configPath}
                  download
                  className="inline-flex h-10 flex-1 cursor-pointer items-center justify-center gap-1.5 rounded-control bg-accent px-4 text-sm font-medium text-on-accent outline-none transition-colors hover:bg-accent-hover focus-visible:ring-[3px] focus-visible:ring-ring"
                >
                  <Download className="h-4 w-4" />
                  {t('Download config file')}
                </a>
                <Button variant="secondary" className="flex-1" onClick={copyLink}>
                  {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                  {copied ? t('Link copied') : t('Copy subscription link')}
                </Button>
              </div>
            </div>
          </Section>
        )}

        <Section title="1. Install the WireGuard app">
          <div className="grid grid-cols-2 gap-2">
            {APPS.map((a) => (
              <a
                key={a.id}
                href={a.href}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-2 rounded-lg border border-edge px-3 py-3 text-sm font-medium text-fg outline-none transition-colors hover:border-edge-strong hover:bg-inset focus-visible:ring-[3px] focus-visible:ring-ring"
              >
                <a.icon className="h-4 w-4 shrink-0 text-accent" />
                <span className="truncate">{t(a.name)}</span>
              </a>
            ))}
          </div>
          <p className="mt-3 text-xs text-muted">
            {t('Only install the official WireGuard app from the links above.')}
          </p>
        </Section>

        <Section title="2. Add your configuration">
          <ol className="list-decimal space-y-2 ps-5 text-sm leading-relaxed text-muted marker:text-faint">
            <li>{t('Open the WireGuard app and tap the + button (Add tunnel).')}</li>
            <li>{t('Scan the QR code above, or choose Import from file and pick the downloaded config.')}</li>
            <li>{t('Give the tunnel any name and turn it on. When the key icon appears, you are connected.')}</li>
          </ol>
          <p className="mt-3 text-xs leading-relaxed text-muted">
            {t('Keep this page private: anyone with this link can use your subscription.')}
          </p>
        </Section>

        {info.support_contact && (
          <p className="text-center text-xs text-muted">
            {t('Need help?')} <span className="ltr font-mono text-fg">{info.support_contact}</span>
          </p>
        )}
        <p className="text-center text-[11px] text-faint" lang={lang}>
          Jirjirak WireGuard
        </p>
      </main>
    </div>
  )
}
