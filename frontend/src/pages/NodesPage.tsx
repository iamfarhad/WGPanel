import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Server, KeyRound, Copy, Activity, Pencil } from 'lucide-react'
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { useAuth } from '../lib/auth'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Dialog } from '../components/ui/Dialog'
import { Badge, statusTone } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'

interface Node {
  id: string
  name: string
  node_group: string
  public_endpoint: string
  wg_subnet: string
  status: string
  capacity_max_peers: number
  public_key: string | null
  created_at: string
}

interface JoinToken {
  token: string
  expires_at: string | null
  unlimited: boolean
}

interface MetricsSample {
  bucket: string
  cpu_percent: number | null
  mem_used_bytes: number | null
  mem_total_bytes: number | null
}

function formatBytesShort(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

export function NodesPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()
  const { user } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [joinTokenNode, setJoinTokenNode] = useState<Node | null>(null)
  const [joinToken, setJoinToken] = useState<JoinToken | null>(null)
  const [detailNode, setDetailNode] = useState<Node | null>(null)
  const [editNode, setEditNode] = useState<Node | null>(null)

  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const nodes = nodesQuery.data?.nodes ?? []

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<{ default_node_capacity: number }>('/api/v1/settings'),
  })

  const joinTokenMutation = useMutation({
    mutationFn: ({ nodeId, unlimited }: { nodeId: string; unlimited: boolean }) =>
      apiFetch<JoinToken>(`/api/v1/nodes/${nodeId}/join-token`, {
        method: 'POST',
        body: JSON.stringify({ unlimited }),
      }),
    onSuccess: (data) => setJoinToken(data),
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to generate join token'),
  })

  return (
    <div>
      <PageHeader
        title="Nodes"
        description="WireGuard servers this panel provisions accounts onto."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            New node
          </Button>
        }
      />

      <Card>
        {nodesQuery.isLoading && <TableSkeleton cols={5} />}
        {nodesQuery.isError && <p className="p-6 text-sm text-red-600 dark:text-red-400">Could not load nodes.</p>}
        {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 && (
          <EmptyState icon={Server} title="No nodes registered yet" description="Add a node, then run install-node.sh with its join token." />
        )}
        {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className="border-b border-slate-200 text-slate-500 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th className="px-6 py-3 font-medium">Name</th>
                <th className="px-6 py-3 font-medium">Group</th>
                <th className="px-6 py-3 font-medium">Endpoint</th>
                <th className="px-6 py-3 font-medium">Status</th>
                <th className="px-6 py-3 font-medium">Capacity</th>
                <th className="px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((node) => (
                <tr
                  key={node.id}
                  onClick={() => setDetailNode(node)}
                  className="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50"
                >
                  <td className="px-6 py-3 text-slate-900 dark:text-slate-100">{node.name}</td>
                  <td className="px-6 py-3 text-slate-500 dark:text-slate-400">{node.node_group}</td>
                  <td className="px-6 py-3 font-mono text-xs text-slate-500 dark:text-slate-400">{node.public_endpoint}</td>
                  <td className="px-6 py-3">
                    <Badge tone={statusTone(node.status)}>{node.status}</Badge>
                  </td>
                  <td className="px-6 py-3 text-slate-500 dark:text-slate-400">{node.capacity_max_peers}</td>
                  <td className="px-6 py-3">
                    <div className="flex gap-2">
                      <Button variant="secondary" onClick={(e) => { e.stopPropagation(); setDetailNode(node) }}>
                        <Activity className="mr-1 h-3.5 w-3.5" />
                        Metrics
                      </Button>
                      <Button variant="secondary" onClick={(e) => { e.stopPropagation(); setEditNode(node) }}>
                        <Pencil className="mr-1 h-3.5 w-3.5" />
                        Edit
                      </Button>
                      {user?.role === 'super_admin' && (
                        <Button
                          variant="secondary"
                          onClick={(e) => {
                            e.stopPropagation()
                            setJoinToken(null)
                            setJoinTokenNode(node)
                          }}
                        >
                          <KeyRound className="mr-1 h-3.5 w-3.5" />
                          Join token
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <CreateNodeDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        defaultCapacity={settingsQuery.data?.default_node_capacity ?? 250}
        onCreated={() => {
          queryClient.invalidateQueries({ queryKey: ['nodes'] })
          setCreateOpen(false)
        }}
      />

      {joinTokenNode && (
        <JoinTokenDialog
          node={joinTokenNode}
          token={joinToken}
          onGenerate={(unlimited) => joinTokenMutation.mutate({ nodeId: joinTokenNode.id, unlimited })}
          generating={joinTokenMutation.isPending}
          onClose={() => {
            setJoinTokenNode(null)
            setJoinToken(null)
          }}
        />
      )}

      {detailNode && <NodeDetailDialog node={detailNode} onClose={() => setDetailNode(null)} />}

      {editNode && (
        <EditNodeDialog
          node={editNode}
          onClose={() => setEditNode(null)}
          onSaved={() => {
            queryClient.invalidateQueries({ queryKey: ['nodes'] })
            setEditNode(null)
          }}
        />
      )}
    </div>
  )
}

function JoinTokenDialog({
  node,
  token,
  onGenerate,
  generating,
  onClose,
}: {
  node: Node
  token: JoinToken | null
  onGenerate: (unlimited: boolean) => void
  generating: boolean
  onClose: () => void
}) {
  const { push } = useToast()
  const [unlimited, setUnlimited] = useState(false)

  return (
    <Dialog open onClose={onClose} title={`Join token for ${node.name}`}>
      {!token && (
        <div className="space-y-4">
          <label className="flex items-start gap-2 text-sm text-slate-700 dark:text-slate-300">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={unlimited}
              onChange={(e) => setUnlimited(e.target.checked)}
            />
            <span>
              <span className="font-medium">Unlimited (reusable, never expires)</span>
              <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
                Use this only to re-register this node's agent (rebuilt server, replaced hardware) - a normal token
                is single-use and expires, and works for onboarding a brand-new node. An unlimited token keeps
                working forever, so treat it like a long-lived credential.
              </p>
            </span>
          </label>
          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button onClick={() => onGenerate(unlimited)} disabled={generating}>
              <KeyRound className="mr-2 h-4 w-4" />
              {generating ? 'Generating…' : 'Generate token'}
            </Button>
          </div>
        </div>
      )}
      {token && (
        <div className="space-y-3">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Run <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-xs dark:bg-slate-800">install-node.sh</code> on
            the server with this token.{' '}
            {token.unlimited
              ? 'This token is unlimited - it never expires and can be reused.'
              : `It expires at ${new Date(token.expires_at!).toLocaleString()}.`}
          </p>
          <p className="break-all rounded-md bg-slate-100 p-3 font-mono text-xs text-slate-800 dark:bg-slate-950 dark:text-slate-200">
            {token.token}
          </p>
          <Button
            variant="secondary"
            onClick={() => {
              navigator.clipboard.writeText(token.token)
              push('success', 'Join token copied')
            }}
          >
            <Copy className="mr-2 h-4 w-4" />
            Copy token
          </Button>
        </div>
      )}
    </Dialog>
  )
}

function EditNodeDialog({
  node,
  onClose,
  onSaved,
}: {
  node: Node
  onClose: () => void
  onSaved: () => void
}) {
  const { push } = useToast()
  const [name, setName] = useState(node.name)
  const [nodeGroup, setNodeGroup] = useState(node.node_group)
  const [publicEndpoint, setPublicEndpoint] = useState(node.public_endpoint)
  const [capacity, setCapacity] = useState(node.capacity_max_peers.toString())
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setName(node.name)
    setNodeGroup(node.node_group)
    setPublicEndpoint(node.public_endpoint)
    setCapacity(node.capacity_max_peers.toString())
    setError(null)
  }, [node])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name,
          node_group: nodeGroup,
          public_endpoint: publicEndpoint,
          capacity_max_peers: Number(capacity),
        }),
      }),
    onSuccess: () => {
      push('success', `${name} updated`)
      onSaved()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to update node'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={`Edit ${node.name}`}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Name</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Node group</label>
            <Input value={nodeGroup} onChange={(e) => setNodeGroup(e.target.value)} placeholder="default" />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Capacity (peers)</label>
            <Input type="number" min="1" value={capacity} onChange={(e) => setCapacity(e.target.value)} required />
          </div>
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Public endpoint</label>
          <Input value={publicEndpoint} onChange={(e) => setPublicEndpoint(e.target.value)} required placeholder="vpn1.example.com:51820" />
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">WireGuard subnet</label>
          <Input value={node.wg_subnet} disabled />
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            Not editable once peers may already be allocated from it.
          </p>
        </div>
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function NodeDetailDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const metricsQuery = useQuery({
    queryKey: ['node-metrics', node.id],
    queryFn: () => apiFetch<{ samples: MetricsSample[] | null }>(`/api/v1/nodes/${node.id}/metrics`),
  })
  const samples = metricsQuery.data?.samples ?? []
  const chartData = samples.map((s) => ({
    time: new Date(s.bucket).toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' }),
    'CPU %': s.cpu_percent !== null ? Number(s.cpu_percent.toFixed(1)) : null,
    'RAM %': s.mem_total_bytes ? Number(((100 * (s.mem_used_bytes ?? 0)) / s.mem_total_bytes).toFixed(1)) : null,
  }))
  const latest = samples[samples.length - 1]

  return (
    <Dialog open onClose={onClose} title={`${node.name} — health`} maxWidthClassName="max-w-2xl">
      <div className="space-y-3 text-sm">
        <div className="flex items-center justify-between gap-4">
          <span className="text-slate-500 dark:text-slate-400">Status</span>
          <Badge tone={statusTone(node.status)}>{node.status}</Badge>
        </div>
        {latest?.mem_total_bytes != null && latest.mem_used_bytes != null && (
          <div className="flex items-center justify-between gap-4">
            <span className="text-slate-500 dark:text-slate-400">Memory</span>
            <span className="text-slate-900 dark:text-slate-100">
              {formatBytesShort(latest.mem_used_bytes)} / {formatBytesShort(latest.mem_total_bytes)}
            </span>
          </div>
        )}
      </div>

      <div className="mt-4 border-t border-slate-100 pt-4 dark:border-slate-800">
        <p className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
          CPU &amp; RAM (last 24h)
        </p>
        {metricsQuery.isLoading && <p className="text-sm text-slate-500 dark:text-slate-400">Loading…</p>}
        {!metricsQuery.isLoading && chartData.length === 0 && (
          <p className="text-sm text-slate-500 dark:text-slate-400">No metrics reported yet - the node agent reports health every ~40s.</p>
        )}
        {chartData.length > 0 && (
          <div className="h-56 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartData} margin={{ top: 4, right: 8, left: 8, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="currentColor" className="text-slate-100 dark:text-slate-800" />
                <XAxis dataKey="time" tick={{ fontSize: 10 }} minTickGap={30} />
                <YAxis tick={{ fontSize: 10 }} domain={[0, 100]} unit="%" width={40} />
                <Tooltip contentStyle={{ fontSize: 12 }} />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                <Line type="monotone" dataKey="CPU %" stroke="#3b82f6" dot={false} strokeWidth={1.5} connectNulls />
                <Line type="monotone" dataKey="RAM %" stroke="#f59e0b" dot={false} strokeWidth={1.5} connectNulls />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
    </Dialog>
  )
}

function CreateNodeDialog({
  open,
  onClose,
  onCreated,
  defaultCapacity,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
  defaultCapacity: number
}) {
  const { push } = useToast()
  const [name, setName] = useState('')
  const [nodeGroup, setNodeGroup] = useState('default')
  const [publicEndpoint, setPublicEndpoint] = useState('')
  const [wgSubnet, setWgSubnet] = useState('')
  const [capacity, setCapacity] = useState(defaultCapacity.toString())
  const [error, setError] = useState<string | null>(null)

  // The dialog stays mounted (just hidden) between opens, so a plain useState
  // initializer only ever sees defaultCapacity from the very first render - before
  // the settings fetch has necessarily resolved. Re-sync on every open instead, which
  // also gives a clean form each time rather than whatever was left over.
  useEffect(() => {
    if (!open) return
    setName('')
    setNodeGroup('default')
    setPublicEndpoint('')
    setWgSubnet('')
    setCapacity(defaultCapacity.toString())
    setError(null)
  }, [open, defaultCapacity])

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/nodes', {
        method: 'POST',
        body: JSON.stringify({
          name,
          node_group: nodeGroup,
          public_endpoint: publicEndpoint,
          wg_subnet: wgSubnet,
          capacity_max_peers: Number(capacity),
        }),
      }),
    onSuccess: () => {
      push('success', `Node "${name}" created`)
      setName('')
      setNodeGroup('default')
      setPublicEndpoint('')
      setWgSubnet('')
      setCapacity('250')
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create node'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New node">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Name</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="e.g. eu-west-1" />
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Node group</label>
            <Input value={nodeGroup} onChange={(e) => setNodeGroup(e.target.value)} placeholder="default" />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Capacity (peers)</label>
            <Input type="number" min="1" value={capacity} onChange={(e) => setCapacity(e.target.value)} />
          </div>
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Public endpoint</label>
          <Input
            value={publicEndpoint}
            onChange={(e) => setPublicEndpoint(e.target.value)}
            required
            placeholder="vpn1.example.com:51820"
          />
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">WireGuard subnet</label>
          <Input value={wgSubnet} onChange={(e) => setWgSubnet(e.target.value)} required placeholder="10.66.0.0/24" />
        </div>
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create node'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
