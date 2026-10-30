import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Pencil, RefreshCw, Trash2, Server, TriangleAlert } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import {
  canManageObfuscation,
  compatLabels,
  useObfuscationProfiles,
  type NodeObfuscationInfo,
  type ObfuscationProfile,
} from '../lib/obfuscation'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Select } from '../components/ui/Select'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { RowActions } from '../components/ui/RowActions'
import { ProfileDialog } from '../components/obfuscation/ProfileDialog'

interface NodeRow {
  id: string
  name: string
  node_group: string
  obfuscation?: NodeObfuscationInfo
}

/** AmneziaWG obfuscation profiles: create, edit, rotate and assign to node groups.
 * Assigning a single node is done in the node's edit dialog (Nodes page). */
export function ObfuscationPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const canManage = canManageObfuscation(user?.role)

  const profilesQuery = useObfuscationProfiles()
  const profiles = profilesQuery.data?.profiles ?? []
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: NodeRow[] | null }>('/api/v1/nodes'),
  })
  const nodes = useMemo(() => nodesQuery.data?.nodes ?? [], [nodesQuery.data])

  const [editing, setEditing] = useState<ObfuscationProfile | 'new' | null>(null)
  const [rotateTarget, setRotateTarget] = useState<ObfuscationProfile | null>(null)
  const [groupTarget, setGroupTarget] = useState<ObfuscationProfile | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ObfuscationProfile | null>(null)

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['obfuscation-profiles'] })
    queryClient.invalidateQueries({ queryKey: ['nodes'] })
  }

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/obfuscation/profiles/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', t('Profile deleted'))
      refresh()
      setDeleteTarget(null)
    },
    onError: (err) => {
      push('error', err instanceof ApiError ? err.message : t('Failed to delete profile'))
      setDeleteTarget(null)
    },
  })

  // Per-profile counts of nodes that really run it vs. are still waiting.
  const applied = useMemo(() => {
    const m = new Map<string, number>()
    for (const n of nodes) {
      if (n.obfuscation?.applied && n.obfuscation.profile_id) {
        m.set(n.obfuscation.profile_id, (m.get(n.obfuscation.profile_id) ?? 0) + 1)
      }
    }
    return m
  }, [nodes])

  return (
    <div>
      <PageHeader
        title="Obfuscation"
        description="AmneziaWG profiles that make a node's traffic harder to fingerprint. Assign a profile to a node (or a whole node group) and re-import the configs on the clients."
        action={
          canManage && (
            <Button onClick={() => setEditing('new')}>
              <Plus className="h-4 w-4" />
              New profile
            </Button>
          )
        }
      />

      <Card className="overflow-hidden">
        {profilesQuery.isLoading && <TableSkeleton cols={5} />}
        {profilesQuery.isError && <ErrorState title="Could not load profiles." onRetry={() => profilesQuery.refetch()} />}
        {!profilesQuery.isLoading && !profilesQuery.isError && profiles.length === 0 && (
          <EmptyState
            illustration="setup"
            title="No obfuscation profiles yet"
            description="Plain WireGuard can be blocked by deep packet inspection. Create a profile from a preset and assign it to a node."
            action={
              canManage && (
                <Button onClick={() => setEditing('new')}>
                  <Plus className="h-4 w-4" />
                  New profile
                </Button>
              )
            }
          />
        )}
        {profiles.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Compatibility</Th>
                <Th>Revision</Th>
                <Th>Nodes</Th>
                <Th>Updated</Th>
                {canManage && <Th className="text-end">Actions</Th>}
              </tr>
            </THead>
            <tbody>
              {profiles.map((p) => (
                <Tr key={p.id}>
                  <Td className="font-medium text-fg">
                    {p.name}
                    {p.stock_client_compatible && (
                      <span className="ms-2">
                        <Badge tone="green">Stock clients OK</Badge>
                      </span>
                    )}
                  </Td>
                  <Td>
                    <Badge tone="blue">{compatLabels[p.compat]}</Badge>
                  </Td>
                  <Td className="text-muted tabular-nums">{p.revision}</Td>
                  <Td className="text-muted tabular-nums">
                    {p.node_count === 0
                      ? '—'
                      : t('{applied} of {total} running', { applied: applied.get(p.id) ?? 0, total: p.node_count })}
                  </Td>
                  <Td className="whitespace-nowrap text-muted">{new Date(p.updated_at).toLocaleDateString()}</Td>
                  {canManage && (
                    <Td className="text-end">
                      <RowActions
                        actions={[
                          { label: 'Edit', icon: Pencil, onSelect: () => setEditing(p) },
                          { label: 'Rotate parameters', icon: RefreshCw, onSelect: () => setRotateTarget(p) },
                          { label: 'Assign to node group', icon: Server, onSelect: () => setGroupTarget(p) },
                          { label: 'Delete', icon: Trash2, danger: true, onSelect: () => setDeleteTarget(p) },
                        ]}
                      />
                    </Td>
                  )}
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      {editing && (
        <ProfileDialog
          profile={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            refresh()
            setEditing(null)
          }}
        />
      )}
      {rotateTarget && (
        <RotateDialog
          profile={rotateTarget}
          onClose={() => setRotateTarget(null)}
          onDone={() => {
            refresh()
            setRotateTarget(null)
          }}
        />
      )}
      {groupTarget && (
        <AssignGroupDialog
          profile={groupTarget}
          groups={[...new Set(nodes.map((n) => n.node_group))].sort()}
          onClose={() => setGroupTarget(null)}
          onDone={() => {
            refresh()
            setGroupTarget(null)
          }}
        />
      )}
      <ConfirmDialog
        open={deleteTarget != null}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        title="Delete profile?"
        description="The profile is removed. A profile that is still assigned to nodes cannot be deleted - unassign it first."
        confirmLabel="Delete"
        danger
        submitting={deleteMutation.isPending}
      />
    </div>
  )
}

function RotateDialog({
  profile,
  onClose,
  onDone,
}: {
  profile: ObfuscationProfile
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const [cps, setCps] = useState('keep')

  const rotate = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/obfuscation/profiles/${profile.id}/rotate`, {
        method: 'POST',
        body: JSON.stringify(profile.compat === 'awg2' ? { cps } : {}),
      }),
    onSuccess: () => {
      push('success', t('{name} rotated', { name: profile.name }))
      onDone()
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : t('Failed to rotate profile')),
  })

  return (
    <Dialog open onClose={onClose} title={t('Rotate {name}', { name: profile.name })}>
      <div className="space-y-4">
        <p className="flex gap-2 rounded-lg border border-warning/30 bg-warning-soft p-3 text-sm leading-relaxed text-warning-fg">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
          <span>
            {t('Fresh random parameters replace the current ones. Nodes using this profile reconfigure on their next heartbeat, and every client config issued for them stops working until the user imports the new config.')}
          </span>
        </p>
        {profile.compat === 'awg2' && (
          <Field label="Signature packets (I1-I5)">
            <Select value={cps} onChange={(e) => setCps(e.target.value)}>
              <option value="keep">{t('Keep the current ones')}</option>
              <option value="quic">{t('Replace I1 with a QUIC-like packet')}</option>
              <option value="dns">{t('Replace I1 with a DNS-like packet')}</option>
              <option value="none">{t('Remove them')}</option>
            </Select>
          </Field>
        )}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="secondary" onClick={onClose} disabled={rotate.isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={() => rotate.mutate()} disabled={rotate.isPending}>
            <RefreshCw className="h-4 w-4" />
            {rotate.isPending ? 'Working…' : 'Rotate'}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function AssignGroupDialog({
  profile,
  groups,
  onClose,
  onDone,
}: {
  profile: ObfuscationProfile
  groups: string[]
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const [group, setGroup] = useState(groups[0] ?? '')
  const [clear, setClear] = useState(false)

  const assign = useMutation({
    mutationFn: () =>
      apiFetch<{ nodes_updated: number }>('/api/v1/obfuscation/assign-group', {
        method: 'POST',
        body: JSON.stringify({ node_group: group, profile_id: clear ? null : profile.id }),
      }),
    onSuccess: (r) => {
      push('success', t('{n} node(s) updated', { n: r.nodes_updated }))
      onDone()
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : t('Failed to assign profile')),
  })

  return (
    <Dialog open onClose={onClose} title={t('Assign {name} to a node group', { name: profile.name })}>
      <div className="space-y-4">
        <Field label="Node group">
          <Select value={group} onChange={(e) => setGroup(e.target.value)}>
            {groups.map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
          </Select>
        </Field>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-fg">
          <input type="checkbox" checked={clear} onChange={(e) => setClear(e.target.checked)} />
          {t('Remove the profile from this group instead (back to plain WireGuard)')}
        </label>
        <p className="text-xs leading-relaxed text-muted">
          {t('Every node in the group switches on its next heartbeat. Clients need an AmneziaWG app and a re-imported config.')}
        </p>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="secondary" onClick={onClose} disabled={assign.isPending}>
            Cancel
          </Button>
          <Button onClick={() => assign.mutate()} disabled={assign.isPending || !group}>
            {assign.isPending ? 'Working…' : clear ? 'Remove' : 'Assign'}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
