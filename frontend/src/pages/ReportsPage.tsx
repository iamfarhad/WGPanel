import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Activity, Download, HardDrive, UserPlus, Wifi } from 'lucide-react'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { apiFetch } from '../lib/api'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { ltr } from '../lib/bidi'
import { AREA_FILL_OPACITY, chartAxis, chartColors, chartGrid } from '../lib/chartTheme'
import { PageHeader } from '../components/ui/PageHeader'
import { StatCard } from '../components/ui/StatCard'
import { Card, CardHeader } from '../components/ui/Card'
import { Badge, statusTone } from '../components/ui/Badge'
import { Button } from '../components/ui/Button'
import { Select } from '../components/ui/Select'
import { Progress } from '../components/ui/Progress'
import { Skeleton } from '../components/ui/Skeleton'
import { ErrorState } from '../components/ui/ErrorState'
import { ExpiryBadge } from '../components/ui/ExpiryBadge'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { ChartCard } from '../components/charts/ChartCard'
import { ChartTooltip } from '../components/charts/ChartTooltip'
import { HBarList, type BarTone } from '../components/charts/HBarList'
import { StackedShareBar, type SharePart } from '../components/charts/StackedShareBar'
import { RangeTabs } from '../components/charts/RangeTabs'

type RangeKey = '24h' | '7d' | '30d' | '90d' | '365d'

interface Owned {
  owner: string | null
  reseller_id: string | null
}

interface Report {
  range: { key: RangeKey; from: string; to: string; bucket: 'hour' | 'day' }
  totals: {
    rx_bytes: number
    tx_bytes: number
    active_accounts: number
    new_accounts: number
    accounts_total: number
    accounts_active: number
    accounts_suspended: number
    accounts_on_hold: number
    online_now: number
  }
  traffic_series: { ts: string; rx_bytes: number; tx_bytes: number; active_accounts: number }[]
  new_accounts_series: { ts: string; count: number }[]
  status_breakdown: { status: string; reason: string | null; count: number }[]
  quota_buckets: { bucket: string; count: number }[]
  expiry_buckets: { bucket: string; count: number }[]
  expiring: (Owned & {
    id: string
    label: string
    expiry_at: string
    status: string
    data_used_bytes: number
    data_quota_bytes: number | null
  })[]
  top_accounts: (Owned & { id: string; label: string; rx_bytes: number; tx_bytes: number })[]
  nodes: {
    id: string
    name: string
    status: string
    rx_bytes: number
    tx_bytes: number
    accounts: number
    cpu_avg: number | null
    mem_avg_percent: number | null
  }[]
  resellers?: {
    id: string
    username: string
    disabled: boolean
    expires_at: string | null
    accounts: number
    data_allocated_bytes: number
    data_limit_bytes: number | null
    rx_bytes: number
    tx_bytes: number
    // Tree: rows arrive depth-first (a parent directly above its children); subtree_*
    // roll the reseller and all its descendants up.
    parent_id: string | null
    depth: number
    children: number
    subtree_accounts: number
    subtree_rx_bytes: number
    subtree_tx_bytes: number
  }[] | null
}

// Decimal units throughout the panel: quotas and credits are entered in GB = 1e9
// bytes, so showing binary GiB would render a 10 GB quota as 9.3 GB.
function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1000)))
  const v = bytes / 1000 ** i
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

function compact(n: number): string {
  return new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

const quotaMeta: Record<string, { label: string; tone: BarTone }> = {
  lt50: { label: 'Under 50% used', tone: 'accent' },
  '50to80': { label: '50–80% used', tone: 'accent' },
  '80to100': { label: '80–100% used', tone: 'warning' },
  exceeded: { label: 'Quota used up', tone: 'danger' },
  unlimited: { label: 'Unlimited', tone: 'muted' },
}

const expiryMeta: Record<string, { label: string; tone: BarTone }> = {
  expired: { label: 'Expired', tone: 'danger' },
  today: { label: 'Today', tone: 'warning' },
  '3d': { label: 'Within 3 days', tone: 'warning' },
  '7d': { label: 'Within 7 days', tone: 'accent' },
  '30d': { label: 'Within 30 days', tone: 'accent' },
  later: { label: 'Later', tone: 'muted' },
  never: { label: 'No expiry', tone: 'muted' },
  on_hold: { label: 'On hold (starts on first use)', tone: 'accent' },
}

function statusPart(status: string, reason: string | null): { key: string; label: string; tone: BarTone } {
  if (status === 'active') return { key: 'active', label: 'Active', tone: 'accent' }
  switch (reason) {
    case 'quota_exceeded':
      return { key: 'quota', label: 'Suspended: quota used up', tone: 'warning' }
    case 'expired':
      return { key: 'expired', label: 'Suspended: expired', tone: 'danger' }
    case 'reseller_suspended':
      return { key: 'reseller', label: 'Suspended with their reseller', tone: 'muted' }
    default:
      return { key: `other-${status}`, label: 'Suspended manually', tone: 'muted' }
  }
}

function csvEscape(v: unknown): string {
  const s = v === null || v === undefined ? '' : String(v)
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

/** One CSV with a section per dataset, so the export matches what is on screen. */
function exportCsv(report: Report) {
  const lines: string[] = []
  const section = (title: string, header: string[], rows: unknown[][]) => {
    lines.push(title, header.join(','), ...rows.map((r) => r.map(csvEscape).join(',')), '')
  }
  section('Traffic', ['time', 'download_bytes', 'upload_bytes', 'active_accounts'],
    report.traffic_series.map((p) => [p.ts, p.tx_bytes, p.rx_bytes, p.active_accounts]))
  section('New accounts', ['time', 'count'], report.new_accounts_series.map((p) => [p.ts, p.count]))
  section('Top accounts', ['label', 'owner', 'download_bytes', 'upload_bytes'],
    report.top_accounts.map((a) => [a.label, a.owner ?? 'admin', a.tx_bytes, a.rx_bytes]))
  section('Expiring within 7 days', ['label', 'owner', 'expiry_at', 'data_used_bytes', 'data_quota_bytes'],
    report.expiring.map((a) => [a.label, a.owner ?? 'admin', a.expiry_at, a.data_used_bytes, a.data_quota_bytes]))
  section('Nodes', ['name', 'status', 'download_bytes', 'upload_bytes', 'accounts', 'cpu_avg', 'mem_avg_percent'],
    report.nodes.map((n) => [n.name, n.status, n.tx_bytes, n.rx_bytes, n.accounts, n.cpu_avg, n.mem_avg_percent]))
  if (report.resellers) {
    section('Resellers', ['username', 'disabled', 'expires_at', 'accounts', 'data_allocated_bytes', 'data_limit_bytes', 'download_bytes', 'upload_bytes'],
      report.resellers.map((r) => [r.username, r.disabled, r.expires_at, r.accounts, r.data_allocated_bytes, r.data_limit_bytes, r.tx_bytes, r.rx_bytes]))
  }
  const blob = new Blob(['﻿' + lines.join('\n')], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `jirjirak-report-${report.range.key}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

export function ReportsPage() {
  const { t, formatDate } = useI18n()
  const { user } = useAuth()
  const isReseller = user?.role === 'reseller'
  const isSuperAdmin = user?.role === 'super_admin'
  const [range, setRange] = useState<RangeKey>('7d')
  const [scope, setScope] = useState('all') // 'all' | 'admin' | reseller id

  const resellersQuery = useQuery({
    queryKey: ['resellers'],
    queryFn: () => apiFetch<{ resellers: { id: string; username: string }[] | null }>('/api/v1/resellers'),
    enabled: isSuperAdmin,
  })
  const resellerNames = useMemo(
    () => new Map((resellersQuery.data?.resellers ?? []).map((r) => [r.id, r.username])),
    [resellersQuery.data],
  )

  const params = new URLSearchParams({ range })
  if (!isReseller && scope === 'admin') params.set('owner', 'admin')
  else if (!isReseller && scope !== 'all') params.set('reseller_id', scope)
  const reportQuery = useQuery({
    queryKey: ['report', range, isReseller ? 'self' : scope],
    queryFn: () => apiFetch<Report>(`/api/v1/reports?${params}`),
    placeholderData: keepPreviousData, // keep the previous frame (dimmed) while a new range loads
  })
  const report = reportQuery.data
  // Shallowest tree depth in the reseller breakdown: 1 for staff, the caller's own for a reseller.
  const resellerRootDepth = Math.min(1000, ...(report?.resellers ?? []).map((r) => r.depth))
  const busy = reportQuery.isFetching && reportQuery.isPlaceholderData

  const ownerName = (o: Owned) =>
    o.reseller_id ? (resellerNames.get(o.reseller_id) ?? t('Reseller')) : o.owner ? t('API key') : t('Panel admin')

  const tickFormat = (ts: string | number) =>
    report?.range.bucket === 'day'
      ? formatDate(ts, { month: 'short', day: 'numeric' })
      : range === '24h'
        ? formatDate(ts, { hour: '2-digit', minute: '2-digit' })
        : formatDate(ts, { month: 'short', day: 'numeric', hour: '2-digit' })
  const fullTime = (ts: string | number) =>
    formatDate(ts, report?.range.bucket === 'day' ? { dateStyle: 'medium' } : { dateStyle: 'medium', timeStyle: 'short' })

  const trafficData = (report?.traffic_series ?? []).map((p) => ({
    ts: p.ts,
    // Server-side counters: tx is what the node sent to clients (their download).
    download: p.tx_bytes,
    upload: p.rx_bytes,
    active: p.active_accounts,
  }))

  const rangeOptions: { value: RangeKey; label: string }[] = [
    { value: '24h', label: t('24 hours') },
    { value: '7d', label: t('7 days') },
    { value: '30d', label: t('30 days') },
    { value: '90d', label: t('90 days') },
    { value: '365d', label: t('1 year') },
  ]

  return (
    <div>
      <PageHeader
        title="Reports"
        description={isReseller ? 'Traffic, accounts and expiries across your accounts.' : 'Traffic, accounts, expiries and capacity across the panel.'}
      />

      {/* One filter row scopes everything below it. */}
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <RangeTabs value={range} options={rangeOptions} onChange={setRange} label={t('Time range')} />
        {!isReseller && (
          <Select value={scope} onChange={(e) => setScope(e.target.value)} aria-label={t('Scope')} className="w-full max-w-60">
            <option value="all">{t('All accounts')}</option>
            <option value="admin">{t("Panel admins' accounts")}</option>
            {[...resellerNames].map(([id, name]) => (
              <option key={id} value={id}>
                {t('Reseller: {name}', { name })}
              </option>
            ))}
          </Select>
        )}
        <Button variant="secondary" className="ms-auto" disabled={!report} onClick={() => report && exportCsv(report)}>
          <Download className="h-4 w-4" />
          {t('Export CSV')}
        </Button>
      </div>

      {reportQuery.isError && !report && (
        <Card>
          <ErrorState title="Could not load the report." onRetry={() => reportQuery.refetch()} />
        </Card>
      )}

      {!report && reportQuery.isLoading && (
        <div className="space-y-6">
          <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-[118px]" />
            ))}
          </div>
          <Skeleton className="h-80" />
        </div>
      )}

      {report && (
        <div className={`space-y-6 transition-opacity duration-200 ${busy ? 'opacity-60' : ''}`}>
          <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-4">
            <StatCard
              icon={HardDrive}
              label="Traffic"
              value={ltr(formatBytes(report.totals.rx_bytes + report.totals.tx_bytes))}
              hint={t('↓ {down} · ↑ {up}', { down: ltr(formatBytes(report.totals.tx_bytes)), up: ltr(formatBytes(report.totals.rx_bytes)) })}
            />
            <StatCard
              icon={Activity}
              label="Active accounts"
              value={compact(report.totals.active_accounts)}
              hint={
                report.totals.accounts_on_hold > 0
                  ? t('Used the VPN in this range · {n} total · {h} on hold', { n: report.totals.accounts_total, h: report.totals.accounts_on_hold })
                  : t('Used the VPN in this range · {n} total', { n: report.totals.accounts_total })
              }
            />
            <StatCard icon={UserPlus} label="New accounts" value={compact(report.totals.new_accounts)} hint={t('Created in this range')} />
            <StatCard
              icon={Wifi}
              label="Online now"
              value={compact(report.totals.online_now)}
              hint={t('Handshake in the last 3 minutes')}
              positive={report.totals.online_now > 0}
            />
          </div>

          <ChartCard
            title="Traffic"
            description="Download and upload by your clients; stacked, so the top edge is the total."
            busy={busy}
            table={
              <SeriesTable
                head={[t('Time'), t('Download'), t('Upload')]}
                rows={trafficData.map((p) => [fullTime(p.ts), ltr(formatBytes(p.download)), ltr(formatBytes(p.upload))])}
              />
            }
          >
            {/* Time runs left to right in every language; recharts is LTR-only anyway. */}
            <div dir="ltr" className="h-72">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={trafficData} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <CartesianGrid {...chartGrid} />
                  <XAxis dataKey="ts" {...chartAxis} tickFormatter={tickFormat} minTickGap={40} />
                  <YAxis {...chartAxis} tickFormatter={(v) => formatBytes(Number(v))} width={64} />
                  <Tooltip
                    cursor={{ stroke: 'var(--ui-edge-strong)', strokeWidth: 1 }}
                    content={<ChartTooltip formatValue={formatBytes} formatLabel={fullTime} />}
                  />
                  <Legend
                    wrapperStyle={{ fontSize: 12, paddingTop: 8 }}
                    iconType="plainline"
                    // Legend text stays in text ink; the line key beside it carries the series color.
                    formatter={(value) => <span style={{ color: 'var(--ui-muted)' }}>{value}</span>}
                  />
                  <Area type="monotone" dataKey="download" name={t('Download')} stackId="t" stroke={chartColors.primary} strokeWidth={2}
                    fill={chartColors.primary} fillOpacity={AREA_FILL_OPACITY} isAnimationActive={false} />
                  <Area type="monotone" dataKey="upload" name={t('Upload')} stackId="t" stroke={chartColors.secondary} strokeWidth={2}
                    fill={chartColors.secondary} fillOpacity={AREA_FILL_OPACITY} isAnimationActive={false} />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          </ChartCard>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <ChartCard
              title="Active accounts"
              description="Accounts that used the VPN in each period."
              busy={busy}
              table={<SeriesTable head={[t('Time'), t('Accounts')]} rows={trafficData.map((p) => [fullTime(p.ts), p.active])} />}
            >
              <CountBars data={trafficData.map((p) => ({ ts: p.ts, value: p.active }))} name={t('Active accounts')} tickFormat={tickFormat} fullTime={fullTime} />
            </ChartCard>
            <ChartCard
              title="New accounts"
              description="Accounts created in each period."
              busy={busy}
              table={<SeriesTable head={[t('Time'), t('Accounts')]} rows={report.new_accounts_series.map((p) => [fullTime(p.ts), p.count])} />}
            >
              <CountBars data={report.new_accounts_series.map((p) => ({ ts: p.ts, value: p.count }))} name={t('New accounts')} tickFormat={tickFormat} fullTime={fullTime} />
            </ChartCard>
          </div>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <Card>
              <CardHeader title="Account status" description="Right now" />
              <div className="p-5">
                <StackedShareBar
                  empty={t('No accounts yet.')}
                  parts={mergeStatus(report.status_breakdown).map((p) => ({ ...p, label: t(p.label) }))}
                />
              </div>
            </Card>
            <Card>
              <CardHeader title="Quota usage" description="How much of their data quota accounts have used" />
              <div className="p-5">
                <HBarList
                  empty={t('No accounts yet.')}
                  items={report.quota_buckets.map((b) => ({
                    key: b.bucket,
                    label: t(quotaMeta[b.bucket]?.label ?? b.bucket),
                    value: b.count,
                    tone: quotaMeta[b.bucket]?.tone,
                  }))}
                />
              </div>
            </Card>
            <Card>
              <CardHeader title="Expiry" description="When accounts run out" />
              <div className="p-5">
                <HBarList
                  empty={t('No accounts yet.')}
                  items={report.expiry_buckets.map((b) => ({
                    key: b.bucket,
                    label: t(expiryMeta[b.bucket]?.label ?? b.bucket),
                    value: b.count,
                    tone: expiryMeta[b.bucket]?.tone,
                  }))}
                />
              </div>
            </Card>
          </div>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card>
              <CardHeader title="Top accounts" description="By traffic in this range" />
              <div className="p-5">
                <HBarList
                  empty={t('No traffic in this range.')}
                  items={report.top_accounts.map((a) => ({
                    key: a.id,
                    label: a.label,
                    sub: isReseller ? undefined : ownerName(a),
                    value: a.rx_bytes + a.tx_bytes,
                    display: ltr(formatBytes(a.rx_bytes + a.tx_bytes)),
                  }))}
                />
              </div>
            </Card>
            <Card className="overflow-hidden">
              <CardHeader title="Expiring soon" description="Active accounts that expire within 7 days" />
              {report.expiring.length === 0 ? (
                <p className="p-5 text-sm text-muted">{t('Nothing expires in the next 7 days.')}</p>
              ) : (
                <Table>
                  <THead>
                    <tr>
                      <Th>Account</Th>
                      <Th>Expiry</Th>
                      <Th>Usage</Th>
                    </tr>
                  </THead>
                  <tbody>
                    {report.expiring.map((a) => (
                      <Tr key={a.id}>
                        <Td>
                          <p className="font-medium text-fg">{a.label}</p>
                          {!isReseller && <p className="text-xs text-faint">{ownerName(a)}</p>}
                        </Td>
                        <Td className="whitespace-nowrap">
                          <ExpiryBadge expiresAt={a.expiry_at} />
                        </Td>
                        <Td className="text-muted">
                          <span className="ltr tabular-nums text-xs">
                            {formatBytes(a.data_used_bytes)}
                            {a.data_quota_bytes ? ` / ${formatBytes(a.data_quota_bytes)}` : ''}
                          </span>
                        </Td>
                      </Tr>
                    ))}
                  </tbody>
                </Table>
              )}
            </Card>
          </div>

          <NodesTable nodes={report.nodes} showHealth={!isReseller} />

          {report.resellers && report.resellers.length > 0 && (
            <Card className="overflow-hidden">
              <CardHeader
                title="Resellers"
                description={
                  isReseller
                    ? 'You and your sub-resellers: each row is the reseller itself, and the totals under a name include everyone below it'
                    : 'Plan usage and traffic in this range - select one to scope the report'
                }
              />
              <Table>
                <THead>
                  <tr>
                    <Th>Reseller</Th>
                    <Th>Plan expiry</Th>
                    <Th>Accounts</Th>
                    <Th>Traffic credit</Th>
                    <Th>Traffic</Th>
                  </tr>
                </THead>
                <tbody>
                  {report.resellers.map((r) => (
                    <Tr key={r.id} interactive={!isReseller} onClick={isReseller ? undefined : () => setScope(r.id)}>
                      <Td>
                        {/* Rolled up under the parent: sub-resellers are indented below it. */}
                        <span
                          className="inline-block font-medium text-fg"
                          style={{ paddingInlineStart: `${(r.depth - resellerRootDepth) * 18}px` }}
                        >
                          {r.depth > resellerRootDepth && <span className="me-1.5 text-faint">↳</span>}
                          {r.username}
                        </span>
                        {r.disabled && (
                          <Badge className="ms-2" tone="slate">
                            disabled
                          </Badge>
                        )}
                      </Td>
                      <Td className="whitespace-nowrap">
                        <ExpiryBadge expiresAt={r.expires_at} soonDays={7} />
                      </Td>
                      <Td className="text-muted tabular-nums">{r.accounts}</Td>
                      <Td className="min-w-40 text-muted">
                        <span className="ltr tabular-nums text-xs">
                          {formatBytes(r.data_allocated_bytes)} / {r.data_limit_bytes === null ? t('Unlimited') : formatBytes(r.data_limit_bytes)}
                        </span>
                        {r.data_limit_bytes !== null && r.data_limit_bytes > 0 && (
                          <Progress value={r.data_allocated_bytes} max={r.data_limit_bytes} className="mt-1.5 max-w-36" />
                        )}
                      </Td>
                      <Td className="text-muted tabular-nums">
                        {ltr(formatBytes(r.rx_bytes + r.tx_bytes))}
                        {r.children > 0 && (
                          <span className="block text-xs text-faint">
                            {t('with sub-resellers: {amount} · {n} accounts', {
                              amount: ltr(formatBytes(r.subtree_rx_bytes + r.subtree_tx_bytes)),
                              n: r.subtree_accounts,
                            })}
                          </span>
                        )}
                      </Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
            </Card>
          )}
        </div>
      )}
    </div>
  )
}

/** Merges suspended rows with the same display bucket (e.g. several manual reasons). */
function mergeStatus(rows: Report['status_breakdown']): SharePart[] {
  const parts = new Map<string, SharePart>()
  for (const row of rows) {
    const meta = statusPart(row.status, row.reason)
    const existing = parts.get(meta.key)
    if (existing) existing.value += row.count
    else parts.set(meta.key, { ...meta, value: row.count })
  }
  const order = ['active', 'quota', 'expired', 'reseller']
  return [...parts.values()].sort((a, b) => {
    const ia = order.indexOf(a.key)
    const ib = order.indexOf(b.key)
    return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
  })
}

/** Single-series columns (no legend - the card title names the series). */
function CountBars({
  data,
  name,
  tickFormat,
  fullTime,
}: {
  data: { ts: string; value: number }[]
  name: string
  tickFormat: (ts: string | number) => string
  fullTime: (ts: string | number) => string
}) {
  return (
    <div dir="ltr" className="h-56">
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barCategoryGap="24%">
          <CartesianGrid {...chartGrid} />
          <XAxis dataKey="ts" {...chartAxis} tickFormatter={tickFormat} minTickGap={32} />
          <YAxis {...chartAxis} allowDecimals={false} width={40} tickFormatter={(v) => compact(Number(v))} />
          <Tooltip
            cursor={{ fill: 'var(--ui-inset)' }}
            content={<ChartTooltip formatValue={(v) => String(v)} formatLabel={fullTime} />}
          />
          <Bar dataKey="value" name={name} fill={chartColors.primary} maxBarSize={24} radius={[4, 4, 0, 0]} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  )
}

function SeriesTable({ head, rows }: { head: string[]; rows: (string | number)[][] }) {
  return (
    <table className="w-full text-start text-sm">
      <thead className="sticky top-0 bg-surface text-xs text-muted">
        <tr>
          {head.map((h) => (
            <th key={h} className="py-1.5 pe-4 text-start font-medium">
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i} className="border-t border-edge/70">
            {r.map((c, j) => (
              <td key={j} className="py-1.5 pe-4 text-muted tabular-nums">
                {c}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function NodesTable({ nodes, showHealth }: { nodes: Report['nodes']; showHealth: boolean }) {
  const { t } = useI18n()
  const max = Math.max(1, ...nodes.map((n) => n.rx_bytes + n.tx_bytes))
  const hasHealth = showHealth && nodes.some((n) => n.cpu_avg !== null || n.mem_avg_percent !== null)
  return (
    <Card className="overflow-hidden">
      <CardHeader title="Nodes" description={hasHealth ? 'Traffic, accounts and average load in this range' : 'Traffic and accounts in this range'} />
      {nodes.length === 0 ? (
        <p className="p-5 text-sm text-muted">{t('No nodes registered yet.')}</p>
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Node</Th>
              <Th>Status</Th>
              <Th>Traffic</Th>
              <Th>Accounts</Th>
              {hasHealth && <Th>CPU</Th>}
              {hasHealth && <Th>RAM</Th>}
            </tr>
          </THead>
          <tbody>
            {nodes.map((n) => {
              const traffic = n.rx_bytes + n.tx_bytes
              return (
                <Tr key={n.id}>
                  <Td className="font-medium text-fg">{n.name}</Td>
                  <Td>
                    <Badge dot tone={statusTone(n.status)}>
                      {n.status}
                    </Badge>
                  </Td>
                  <Td className="min-w-44">
                    <div className="flex items-center gap-3">
                      <div className="h-2 flex-1 rounded-e-full bg-inset">
                        <div className="h-full rounded-e-full bg-[var(--ui-chart-1)]" style={{ width: `${(traffic / max) * 100}%` }} />
                      </div>
                      <span className="w-20 shrink-0 text-xs text-muted tabular-nums">{ltr(formatBytes(traffic))}</span>
                    </div>
                  </Td>
                  <Td className="text-muted tabular-nums">{n.accounts}</Td>
                  {hasHealth && <Td className="min-w-32">{n.cpu_avg === null ? '—' : <LoadMeter value={n.cpu_avg} />}</Td>}
                  {hasHealth && <Td className="min-w-32">{n.mem_avg_percent === null ? '—' : <LoadMeter value={n.mem_avg_percent} />}</Td>}
                </Tr>
              )
            })}
          </tbody>
        </Table>
      )}
    </Card>
  )
}

function LoadMeter({ value }: { value: number }) {
  return (
    <div className="flex items-center gap-2">
      <Progress value={value} max={100} className="max-w-24" />
      <span className="w-10 text-xs text-muted tabular-nums">{Math.round(value)}%</span>
    </div>
  )
}
