import { useQuery } from '@tanstack/react-query'
import { Server, Users, ShieldAlert, Activity, HardDrive, Wifi } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { PageHeader } from '../components/ui/PageHeader'
import { StatCard } from '../components/ui/StatCard'
import { Card } from '../components/ui/Card'
import { Badge, statusTone } from '../components/ui/Badge'
import { Skeleton } from '../components/ui/Skeleton'

interface Node {
  id: string
  name: string
  node_group: string
  status: string
  capacity_max_peers: number
}

interface AccountPeer {
  online: boolean
}

interface Account {
  id: string
  label: string
  status: string
  data_used_bytes: number
  data_quota_bytes: number | null
  peers: AccountPeer[]
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

export function DashboardPage() {
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: () => apiFetch<{ accounts: Account[] | null }>('/api/v1/accounts'),
  })

  const nodes = nodesQuery.data?.nodes ?? []
  const accounts = accountsQuery.data?.accounts ?? []
  const isLoading = nodesQuery.isLoading || accountsQuery.isLoading

  const onlineNodes = nodes.filter((n) => n.status === 'online' || n.status === 'registered').length
  const activeAccounts = accounts.filter((a) => a.status === 'active').length
  const suspendedAccounts = accounts.filter((a) => a.status === 'suspended').length
  const totalDataUsed = accounts.reduce((sum, a) => sum + (a.data_used_bytes ?? 0), 0)
  // "Online" here means at least one of the account's peers has handshaked recently
  // (server-computed, see accountPeerResponse.online) - an account with peers on
  // several nodes is online as soon as any one of them is actively connected.
  const onlineAccounts = accounts.filter((a) => a.peers?.some((p) => p.online)).length

  return (
    <div>
      <PageHeader title="Dashboard" description="Overview of your WireGuard fleet." />

      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-6">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-6">
          <StatCard icon={Server} label="Nodes online" value={`${onlineNodes} / ${nodes.length}`} tone="blue" />
          <StatCard icon={Wifi} label="Accounts online" value={`${onlineAccounts} / ${accounts.length}`} tone="green" />
          <StatCard icon={Users} label="Active accounts" value={activeAccounts} tone="green" />
          <StatCard icon={ShieldAlert} label="Suspended accounts" value={suspendedAccounts} tone="red" />
          <StatCard icon={Activity} label="Total accounts" value={accounts.length} tone="slate" />
          <StatCard icon={HardDrive} label="Data transferred" value={formatBytes(totalDataUsed)} tone="amber" />
        </div>
      )}

      <div className="mt-8 grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Card className="p-6">
          <h2 className="mb-4 text-sm font-semibold text-slate-900 dark:text-slate-100">Node capacity</h2>
          {nodes.length === 0 ? (
            <p className="text-sm text-slate-500 dark:text-slate-400">No nodes registered yet.</p>
          ) : (
            <div className="space-y-3">
              {nodes.map((n) => (
                <div key={n.id} className="flex items-center justify-between text-sm">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-slate-900 dark:text-slate-100">{n.name}</span>
                    <Badge tone={statusTone(n.status)}>{n.status}</Badge>
                  </div>
                  <span className="text-slate-500 dark:text-slate-400">cap {n.capacity_max_peers}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
        <Card className="p-6">
          <h2 className="mb-4 text-sm font-semibold text-slate-900 dark:text-slate-100">Recent accounts</h2>
          {accounts.length === 0 ? (
            <p className="text-sm text-slate-500 dark:text-slate-400">No accounts yet.</p>
          ) : (
            <div className="space-y-3">
              {accounts.slice(0, 6).map((a) => {
                const isOnline = a.peers?.some((p) => p.online)
                return (
                  <div key={a.id} className="flex items-center justify-between text-sm">
                    <span className="inline-flex items-center gap-1.5 font-medium text-slate-900 dark:text-slate-100">
                      <span className={`h-2 w-2 rounded-full ${isOnline ? 'bg-emerald-500' : 'bg-slate-300 dark:bg-slate-600'}`} />
                      {a.label}
                    </span>
                    <Badge tone={statusTone(a.status)}>{a.status}</Badge>
                  </div>
                )
              })}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
