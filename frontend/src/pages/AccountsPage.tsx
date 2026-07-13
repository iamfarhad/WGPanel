import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Users, Download, Ban, CheckCircle2, RotateCcw, Trash2, Server, Copy, FileDown } from 'lucide-react'
import QRCode from 'qrcode'
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'
import { apiFetch, ApiError } from '../lib/api'
import { getAccessToken } from '../lib/tokenStore'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Select } from '../components/ui/Select'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge, statusTone } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'

interface Node {
  id: string
  name: string
  node_group: string
}

interface AccountPeer {
  node_id: string
  node_name: string
  assigned_ip: string
  online: boolean
  last_handshake_at: string | null
}

interface UsageSample {
  bucket: string
  rx_bytes: number
  tx_bytes: number
}

interface Account {
  id: string
  external_ref: string | null
  label: string
  public_key: string
  peers: AccountPeer[]
  data_quota_bytes: number | null
  data_used_bytes: number
  expiry_at: string | null
  device_limit: number | null
  status: string
  suspend_reason: string | null
  created_at: string
  updated_at: string
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

// Mirrors the backend's peerOnlineWindow (180s) purely for display wording - the
// authoritative online/offline bit itself always comes from the server's `online`
// field, never recomputed here from last_handshake_at.
function formatLastSeen(lastHandshakeAt: string | null): string {
  if (!lastHandshakeAt) return 'never connected'
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(lastHandshakeAt).getTime()) / 1000))
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

export function AccountsPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()

  const [createOpen, setCreateOpen] = useState(false)
  const [detailAccount, setDetailAccount] = useState<Account | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Account | null>(null)

  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: () => apiFetch<{ accounts: Account[] | null }>('/api/v1/accounts'),
  })
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<{ default_data_quota_gb: number | null; default_device_limit: number | null }>('/api/v1/settings'),
  })

  const accounts = accountsQuery.data?.accounts ?? []
  const nodes = nodesQuery.data?.nodes ?? []

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['accounts'] })
  }

  const suspendMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}/suspend`, { method: 'POST', body: JSON.stringify({}) }),
    onSuccess: (account) => {
      push('success', `${account.label} suspended`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to suspend account'),
  })

  const enableMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}/enable`, { method: 'POST' }),
    onSuccess: (account) => {
      push('success', `${account.label} enabled`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to enable account'),
  })

  const renewMutation = useMutation({
    mutationFn: ({ id, extendDays }: { id: string; extendDays: number }) =>
      apiFetch<Account>(`/api/v1/accounts/${id}/renew`, {
        method: 'POST',
        body: JSON.stringify({ extend_days: extendDays }),
      }),
    onSuccess: (account) => {
      push('success', `${account.label} renewed`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to renew account'),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}`, { method: 'DELETE' }),
    onSuccess: (account) => {
      push('success', `${account.label} deleted`)
      invalidate()
      setDeleteTarget(null)
      setDetailAccount(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to delete account'),
  })

  return (
    <div>
      <PageHeader
        title="Accounts"
        description="WireGuard peer accounts - each one gets a peer on every eligible node, not just one."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            New account
          </Button>
        }
      />

      <Card>
        {accountsQuery.isLoading && <TableSkeleton cols={5} />}
        {accountsQuery.isError && (
          <p className="p-6 text-sm text-red-600 dark:text-red-400">Could not load accounts.</p>
        )}
        {!accountsQuery.isLoading && !accountsQuery.isError && accounts.length === 0 && (
          <EmptyState icon={Users} title="No accounts yet" description="Create the first WireGuard account to get started." />
        )}
        {!accountsQuery.isLoading && !accountsQuery.isError && accounts.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className="border-b border-slate-200 text-slate-500 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th className="px-6 py-3 font-medium">Label</th>
                <th className="px-6 py-3 font-medium">Nodes</th>
                <th className="px-6 py-3 font-medium">Connection</th>
                <th className="px-6 py-3 font-medium">Usage</th>
                <th className="px-6 py-3 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((a) => {
                const onlineCount = a.peers.filter((p) => p.online).length
                const mostRecentHandshake = a.peers.reduce<string | null>((latest, p) => {
                  if (!p.last_handshake_at) return latest
                  if (!latest || new Date(p.last_handshake_at) > new Date(latest)) return p.last_handshake_at
                  return latest
                }, null)
                return (
                  <tr
                    key={a.id}
                    onClick={() => setDetailAccount(a)}
                    className="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50"
                  >
                    <td className="px-6 py-3 text-slate-900 dark:text-slate-100">{a.label}</td>
                    <td className="px-6 py-3 text-slate-500 dark:text-slate-400">
                      <span className="inline-flex items-center gap-1">
                        <Server className="h-3.5 w-3.5" />
                        {a.peers.length === 0 ? 'none yet' : `${a.peers.length} node${a.peers.length === 1 ? '' : 's'}`}
                      </span>
                    </td>
                    <td className="px-6 py-3 text-slate-500 dark:text-slate-400">
                      {a.peers.length === 0 ? (
                        '—'
                      ) : (
                        <span className="inline-flex items-center gap-1.5">
                          <span className={`h-2 w-2 rounded-full ${onlineCount > 0 ? 'bg-emerald-500' : 'bg-slate-300 dark:bg-slate-600'}`} />
                          {onlineCount > 0
                            ? `Online on ${onlineCount}/${a.peers.length} nodes`
                            : `Offline · ${formatLastSeen(mostRecentHandshake)}`}
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-3 text-slate-500 dark:text-slate-400">
                      {formatBytes(a.data_used_bytes)}
                      {a.data_quota_bytes ? ` / ${formatBytes(a.data_quota_bytes)}` : ''}
                    </td>
                    <td className="px-6 py-3">
                      <Badge tone={statusTone(a.status)}>{a.status}</Badge>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>

      <CreateAccountDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        nodes={nodes}
        defaultQuotaGb={settingsQuery.data?.default_data_quota_gb ?? null}
        defaultDeviceLimit={settingsQuery.data?.default_device_limit ?? null}
        onCreated={() => {
          invalidate()
          setCreateOpen(false)
        }}
      />

      {detailAccount && (
        <AccountDetailDialog
          account={detailAccount}
          onClose={() => setDetailAccount(null)}
          onSuspend={() => suspendMutation.mutate(detailAccount.id)}
          onEnable={() => enableMutation.mutate(detailAccount.id)}
          onRenew={(days) => renewMutation.mutate({ id: detailAccount.id, extendDays: days })}
          onDelete={() => setDeleteTarget(detailAccount)}
          busy={suspendMutation.isPending || enableMutation.isPending || renewMutation.isPending}
        />
      )}

      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete account"
          description={`This will permanently delete "${deleteTarget.label}" and release its assigned IPs. This cannot be undone.`}
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
    </div>
  )
}

function CreateAccountDialog({
  open,
  onClose,
  nodes,
  defaultQuotaGb,
  defaultDeviceLimit,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  nodes: Node[]
  defaultQuotaGb: number | null
  defaultDeviceLimit: number | null
  onCreated: () => void
}) {
  const { push } = useToast()
  const [label, setLabel] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [quotaGb, setQuotaGb] = useState('')
  const [deviceLimit, setDeviceLimit] = useState('')
  const [duration, setDuration] = useState('none')
  const [customExpiry, setCustomExpiry] = useState('')
  const [error, setError] = useState<string | null>(null)

  // The dialog stays mounted (just hidden) between opens, so plain useState
  // initializers only ever see the settings-derived defaults from the very first
  // render, before that fetch has necessarily resolved. Re-sync on every open instead.
  useEffect(() => {
    if (!open) return
    setLabel('')
    setNodeId('')
    setQuotaGb(defaultQuotaGb?.toString() ?? '')
    setDeviceLimit(defaultDeviceLimit?.toString() ?? '')
    setDuration('none')
    setCustomExpiry('')
    setError(null)
  }, [open, defaultQuotaGb, defaultDeviceLimit])

  function resolveExpiryAt(): string | undefined {
    if (duration === 'none') return undefined
    if (duration === 'custom') {
      return customExpiry ? new Date(`${customExpiry}T23:59:59`).toISOString() : undefined
    }
    const days = Number(duration)
    return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString()
  }

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/accounts', {
        method: 'POST',
        body: JSON.stringify({
          label,
          node_id: nodeId || undefined,
          data_quota_gb: quotaGb ? Number(quotaGb) : undefined,
          device_limit: deviceLimit ? Number(deviceLimit) : undefined,
          expiry_at: resolveExpiryAt(),
        }),
      }),
    onSuccess: () => {
      push('success', `Account "${label}" created`)
      setLabel('')
      setNodeId('')
      setQuotaGb('')
      setDeviceLimit('')
      setDuration('none')
      setCustomExpiry('')
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create account'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New account">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Label</label>
          <Input value={label} onChange={(e) => setLabel(e.target.value)} required placeholder="e.g. alice-laptop" />
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Nodes</label>
          <Select value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
            <option value="">All eligible nodes (recommended)</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                Pin to just: {n.name} ({n.node_group})
              </option>
            ))}
          </Select>
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            By default this account gets a peer on every registered node, and stays synced as nodes are added later.
          </p>
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Data quota (GB)</label>
            <Input type="number" min="0" step="0.1" value={quotaGb} onChange={(e) => setQuotaGb(e.target.value)} placeholder="Unlimited" />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Device limit</label>
            <Input type="number" min="1" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder="Unlimited" />
          </div>
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Expires</label>
          <Select value={duration} onChange={(e) => setDuration(e.target.value)}>
            <option value="none">Never</option>
            <option value="7">In 7 days</option>
            <option value="30">In 30 days</option>
            <option value="90">In 90 days</option>
            <option value="180">In 180 days</option>
            <option value="365">In 1 year</option>
            <option value="custom">Custom date…</option>
          </Select>
          {duration === 'custom' && (
            <Input
              type="date"
              className="mt-2"
              value={customExpiry}
              onChange={(e) => setCustomExpiry(e.target.value)}
              min={new Date().toISOString().slice(0, 10)}
              required
            />
          )}
        </div>
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create account'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function AccountDetailDialog({
  account,
  onClose,
  onSuspend,
  onEnable,
  onRenew,
  onDelete,
  busy,
}: {
  account: Account
  onClose: () => void
  onSuspend: () => void
  onEnable: () => void
  onRenew: (days: number) => void
  onDelete: () => void
  busy: boolean
}) {
  const { push } = useToast()
  const [configText, setConfigText] = useState<string | null>(null)
  const [configNodeName, setConfigNodeName] = useState<string | null>(null)
  const [qrDataUrl, setQrDataUrl] = useState<string | null>(null)
  const [configLoading, setConfigLoading] = useState<string | null>(null)

  const usageQuery = useQuery({
    queryKey: ['account-usage', account.id],
    queryFn: () => apiFetch<{ bucket: string; samples: UsageSample[] | null }>(`/api/v1/accounts/${account.id}/usage?bucket=hour`),
  })
  const usageSamples = usageQuery.data?.samples ?? []
  const chartData = usageSamples.map((s) => ({
    time: new Date(s.bucket).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' }),
    'Downloaded (RX)': s.rx_bytes,
    'Uploaded (TX)': s.tx_bytes,
  }))

  async function loadConfig(nodeId: string, nodeName: string) {
    setConfigLoading(nodeId)
    setQrDataUrl(null)
    setConfigNodeName(nodeName)
    try {
      const res = await fetch(`/api/v1/accounts/${account.id}/config?node_id=${nodeId}`, {
        headers: { Authorization: `Bearer ${getAccessToken() ?? ''}` },
      })
      if (!res.ok) {
        // Surface the real reason (e.g. "the node this account is on has no public
        // key set yet") instead of a generic message - a node without a reported
        // WireGuard public key (agent never got a real wg interface up) is a common,
        // specific, actionable case, not a mystery failure.
        let message = 'Failed to load config'
        try {
          const body = (await res.json()) as { error?: { message?: string } }
          message = body?.error?.message ?? message
        } catch {
          // not JSON - fall back to the generic message
        }
        throw new Error(message)
      }
      const text = await res.text()
      setConfigText(text)
      // Generated entirely client-side - this config contains the account's real
      // private key, so it's never sent anywhere but rendered locally as a QR
      // code for scanning into a mobile WireGuard app.
      setQrDataUrl(await QRCode.toDataURL(text, { width: 240, margin: 1 }))
    } catch (err) {
      push('error', err instanceof Error ? err.message : 'Could not load WireGuard config')
    } finally {
      setConfigLoading(null)
    }
  }

  return (
    <Dialog open onClose={onClose} title={account.label} maxWidthClassName="max-w-2xl">
      <div className="space-y-3 text-sm">
        <Row label="Status">
          <Badge tone={statusTone(account.status)}>{account.status}</Badge>
        </Row>
        <Row label="Public key">
          <span className="font-mono text-xs">{account.public_key}</span>
        </Row>
        <Row label="Usage">
          {formatBytes(account.data_used_bytes)}
          {account.data_quota_bytes ? ` / ${formatBytes(account.data_quota_bytes)}` : ' / unlimited'}
        </Row>
        <Row label="Device limit">{account.device_limit ?? 'Unlimited'}</Row>
        <Row label="Expires">{account.expiry_at ? new Date(account.expiry_at).toLocaleString() : 'Never'}</Row>
        {account.suspend_reason && <Row label="Suspend reason">{account.suspend_reason}</Row>}
      </div>

      <div className="mt-4 border-t border-slate-100 pt-4 dark:border-slate-800">
        <p className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
          Node peers ({account.peers.length})
        </p>
        {account.peers.length === 0 ? (
          <p className="text-sm text-slate-500 dark:text-slate-400">No node peers yet - no eligible node was available when this account was created.</p>
        ) : (
          <div className="space-y-2">
            {account.peers.map((p) => (
              <div key={p.node_id} className="flex items-center justify-between rounded-md border border-slate-100 px-3 py-2 dark:border-slate-800">
                <div>
                  <p className="flex items-center gap-1.5 text-sm font-medium text-slate-900 dark:text-slate-100">
                    <span
                      className={`h-2 w-2 rounded-full ${p.online ? 'bg-emerald-500' : 'bg-slate-300 dark:bg-slate-600'}`}
                      title={p.online ? 'Online' : 'Offline'}
                    />
                    {p.node_name}
                    <span className={`text-xs font-normal ${p.online ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400 dark:text-slate-500'}`}>
                      {p.online ? 'online' : `offline · ${formatLastSeen(p.last_handshake_at)}`}
                    </span>
                  </p>
                  <p className="font-mono text-xs text-slate-500 dark:text-slate-400">{p.assigned_ip}</p>
                </div>
                <Button variant="secondary" onClick={() => loadConfig(p.node_id, p.node_name)} disabled={configLoading === p.node_id}>
                  <Download className="mr-1 h-3.5 w-3.5" />
                  {configLoading === p.node_id ? 'Loading…' : 'Config'}
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="mt-4 border-t border-slate-100 pt-4 dark:border-slate-800">
        <p className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
          Usage (last 7 days)
        </p>
        {usageQuery.isLoading && <p className="text-sm text-slate-500 dark:text-slate-400">Loading…</p>}
        {!usageQuery.isLoading && chartData.length === 0 && (
          <p className="text-sm text-slate-500 dark:text-slate-400">No traffic recorded yet.</p>
        )}
        {chartData.length > 0 && (
          <div className="h-48 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={chartData} margin={{ top: 4, right: 8, left: 8, bottom: 0 }}>
                <defs>
                  <linearGradient id="rxGradient" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.4} />
                    <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="txGradient" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#10b981" stopOpacity={0.4} />
                    <stop offset="95%" stopColor="#10b981" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="currentColor" className="text-slate-100 dark:text-slate-800" />
                <XAxis dataKey="time" tick={{ fontSize: 10 }} minTickGap={30} />
                <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => formatBytes(v)} width={60} />
                <Tooltip formatter={(v) => formatBytes(Number(v ?? 0))} contentStyle={{ fontSize: 12 }} />
                <Area type="monotone" dataKey="Downloaded (RX)" stroke="#3b82f6" fill="url(#rxGradient)" strokeWidth={1.5} />
                <Area type="monotone" dataKey="Uploaded (TX)" stroke="#10b981" fill="url(#txGradient)" strokeWidth={1.5} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      {configText !== null && (
        <div className="mt-4 space-y-3">
          <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
            {qrDataUrl && (
              <img
                src={qrDataUrl}
                alt="WireGuard config QR code - scan with the WireGuard mobile app"
                className="h-40 w-40 shrink-0 rounded-md border border-slate-200 bg-white p-2 dark:border-slate-700"
              />
            )}
            <pre className="max-h-64 flex-1 overflow-auto whitespace-pre-wrap rounded-md bg-slate-100 p-4 font-mono text-xs text-slate-800 dark:bg-slate-950 dark:text-slate-200">
              {configText}
            </pre>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              onClick={() => {
                navigator.clipboard.writeText(configText)
                push('success', 'Config copied to clipboard')
              }}
            >
              <Copy className="mr-2 h-4 w-4" />
              Copy config
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                const slug = (configNodeName ?? 'node').toLowerCase().replace(/[^a-z0-9]+/g, '-')
                const blob = new Blob([configText], { type: 'text/plain' })
                const url = URL.createObjectURL(blob)
                const link = document.createElement('a')
                link.href = url
                link.download = `${account.label}-${slug}.conf`
                link.click()
                URL.revokeObjectURL(url)
              }}
            >
              <FileDown className="mr-2 h-4 w-4" />
              Download .conf
            </Button>
          </div>
        </div>
      )}

      <div className="mt-6 flex flex-wrap gap-2 border-t border-slate-100 pt-4 dark:border-slate-800">
        <Button variant="secondary" onClick={() => onRenew(30)} disabled={busy}>
          <RotateCcw className="mr-2 h-4 w-4" />
          Renew 30d
        </Button>
        {account.status === 'suspended' ? (
          <Button variant="secondary" onClick={onEnable} disabled={busy}>
            <CheckCircle2 className="mr-2 h-4 w-4" />
            Enable
          </Button>
        ) : (
          <Button variant="secondary" onClick={onSuspend} disabled={busy}>
            <Ban className="mr-2 h-4 w-4" />
            Suspend
          </Button>
        )}
        <Button
          onClick={onDelete}
          className="bg-red-600 text-white hover:bg-red-700 dark:bg-red-600 dark:hover:bg-red-700"
        >
          <Trash2 className="mr-2 h-4 w-4" />
          Delete
        </Button>
      </div>
    </Dialog>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <span className="text-slate-500 dark:text-slate-400">{label}</span>
      <span className="text-right text-slate-900 dark:text-slate-100">{children}</span>
    </div>
  )
}
