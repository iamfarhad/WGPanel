import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Server, Users, ShieldAlert, Activity, HardDrive, Wifi, ArrowRight } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { Button } from '../components/ui/Button'
import { Mascot } from '../components/brand/Brand'
import { PageHeader } from '../components/ui/PageHeader'
import { StatCard } from '../components/ui/StatCard'
import { Card, CardHeader } from '../components/ui/Card'
import { Badge, statusTone } from '../components/ui/Badge'
import { Skeleton } from '../components/ui/Skeleton'
import { Progress } from '../components/ui/Progress'

interface Node {
  id: string
  name: string
  node_group: string
  status: string
  capacity_max_peers: number
}

interface AccountPeer {
  node_id: string
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

/** Compact greeting strip - the dashboard's one illustrated moment. A status line,
 * two shortcuts and the mascot; never a full-height marketing hero. */
function WelcomeBanner({
  username,
  onlineNodes,
  totalNodes,
  onlineAccounts,
}: {
  username?: string
  onlineNodes: number
  totalNodes: number
  onlineAccounts: number
}) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const status =
    totalNodes === 0
      ? t('Add your first node to start provisioning WireGuard accounts.')
      : t('{online} of {total} nodes online · {clients} connected right now.', {
          online: onlineNodes,
          total: totalNodes,
          clients: onlineAccounts,
        })
  return (
    <Card className="relative mb-6 overflow-hidden">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[linear-gradient(100deg,var(--ui-accent-soft)_0%,transparent_65%)] rtl:bg-[linear-gradient(260deg,var(--ui-accent-soft)_0%,transparent_65%)]"
      />
      <div className="relative flex items-center justify-between gap-6 px-6 py-6 sm:px-7">
        <div className="min-w-0">
          <p className="text-xs font-medium text-accent-fg">Jirjirak WireGuard</p>
          <h2 className="mt-1 text-xl font-semibold tracking-tight text-fg">
            {username ? t('Welcome back, {name}', { name: username }) : t('Welcome back')}
          </h2>
          <p className="mt-1.5 max-w-md text-sm leading-relaxed text-muted">{status}</p>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button size="sm" onClick={() => navigate('/accounts')}>
              View accounts
              <ArrowRight className="h-3.5 w-3.5 rtl:rotate-180" />
            </Button>
            <Button size="sm" variant="secondary" onClick={() => navigate('/nodes')}>
              Manage nodes
            </Button>
          </div>
        </div>
        <Mascot width={210} className="-my-6 me-2 hidden shrink-0 sm:block" />
      </div>
    </Card>
  )
}

export function DashboardPage() {
  const { t } = useI18n()
  const { user } = useAuth()
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

  // Peers provisioned per node (an account contributes one peer to each node
  // it's on), so capacity bars show real utilization rather than just the cap.
  const peersPerNode = new Map<string, number>()
  for (const account of accounts) {
    for (const peer of account.peers ?? []) {
      peersPerNode.set(peer.node_id, (peersPerNode.get(peer.node_id) ?? 0) + 1)
    }
  }

  return (
    <div>
      <PageHeader title="Dashboard" description="Overview of your WireGuard infrastructure." />

      {!isLoading && (
        <WelcomeBanner
          username={user?.username}
          onlineNodes={onlineNodes}
          totalNodes={nodes.length}
          onlineAccounts={onlineAccounts}
        />
      )}

      {isLoading ? (
        <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-[118px]" />
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-3">
          <StatCard
            icon={Wifi}
            label="Connected clients"
            value={onlineAccounts}
            hint={t('of {n} accounts', { n: accounts.length })}
            positive={onlineAccounts > 0}
          />
          <StatCard icon={Users} label="Active accounts" value={activeAccounts} hint={t('{n} total', { n: accounts.length })} />
          <StatCard
            icon={Server}
            label="Nodes online"
            value={`${onlineNodes} / ${nodes.length}`}
            hint={
              nodes.length === 0
                ? t('No nodes yet')
                : onlineNodes === nodes.length
                  ? t('All nodes online')
                  : t('{n} not online', { n: nodes.length - onlineNodes })
            }
            positive={nodes.length > 0 && onlineNodes === nodes.length}
          />
          <StatCard icon={HardDrive} label="Traffic" value={formatBytes(totalDataUsed)} hint="Transferred by all accounts" />
          <StatCard
            icon={ShieldAlert}
            label="Suspended accounts"
            value={suspendedAccounts}
            hint={suspendedAccounts > 0 ? 'Need attention' : 'None suspended'}
          />
          <StatCard icon={Activity} label="Total accounts" value={accounts.length} hint="Across every node" />
        </div>
      )}

      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader title="Node capacity" description="Peers provisioned per node" />
          <div className="p-5">
            {nodes.length === 0 ? (
              <p className="text-sm text-muted">No nodes registered yet.</p>
            ) : (
              <div className="space-y-5">
                {nodes.map((n) => {
                  const used = peersPerNode.get(n.id) ?? 0
                  return (
                    <div key={n.id}>
                      <div className="mb-2 flex items-center justify-between gap-4 text-sm">
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="truncate font-medium text-fg">{n.name}</span>
                          <Badge dot tone={statusTone(n.status)}>{n.status}</Badge>
                        </div>
                        <span className="shrink-0 text-xs text-muted tabular-nums">
                          {t('{used} / {max} peers', { used, max: n.capacity_max_peers })}
                        </span>
                      </div>
                      <Progress value={used} max={n.capacity_max_peers} />
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </Card>

        <Card>
          <CardHeader title="Recent accounts" />
          <div className="p-5">
            {accounts.length === 0 ? (
              <p className="text-sm text-muted">No accounts yet.</p>
            ) : (
              <div className="space-y-4">
                {accounts.slice(0, 6).map((a) => {
                  const isOnline = a.peers?.some((p) => p.online)
                  return (
                    <div key={a.id} className="flex items-center justify-between gap-4 text-sm">
                      <span className="inline-flex min-w-0 items-center gap-2 font-medium text-fg">
                        <span
                          className={`h-2 w-2 shrink-0 rounded-full ${isOnline ? 'bg-success' : 'bg-edge-strong'}`}
                          title={isOnline ? t('Online') : t('Offline')}
                        />
                        <span className="truncate">{a.label}</span>
                      </span>
                      <span className="flex shrink-0 items-center gap-3">
                        <span className="text-xs text-muted tabular-nums">{formatBytes(a.data_used_bytes ?? 0)}</span>
                        <Badge tone={statusTone(a.status)}>{a.status}</Badge>
                      </span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </Card>
      </div>
    </div>
  )
}
