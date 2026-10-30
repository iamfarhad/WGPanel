import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, HardDrive, Network, Pencil, PauseCircle, PlayCircle, Plus, Trash2, Users } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useI18n } from '../lib/i18n'
import { ltr } from '../lib/bidi'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Badge } from '../components/ui/Badge'
import { Progress } from '../components/ui/Progress'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { StatCard } from '../components/ui/StatCard'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { RowActions } from '../components/ui/RowActions'
import { ExpiryBadge } from '../components/ui/ExpiryBadge'
import { ResellerFormDialog, type ChildRoom } from '../components/resellers/ResellerFormDialog'
import { BrandingEditor } from '../components/branding/BrandingEditor'
import { MAX_RESELLER_DEPTH, flattenTree, formatGB, type ResellerRow } from '../lib/resellerTree'

type ConfirmKind = 'suspend' | 'reactivate' | 'delete' | 'disable'

function Usage({ used, max, format }: { used: number; max: number | null; format: (n: number) => string }) {
  const { t } = useI18n()
  return (
    <div className="min-w-32">
      <span className="tabular-nums">{ltr(`${format(used)} / ${max === null ? t('Unlimited') : format(max)}`)}</span>
      {max !== null && max > 0 && <Progress value={used} max={max} className="mt-1.5 max-w-32" />}
    </div>
  )
}

/** A reseller's own corner for its sub-resellers (the agents it sells through) and its
 * branding. Everything here is limited to the signed-in reseller's subtree by the API:
 * a sub-reseller's plan is carved out of this reseller's, so the numbers on top show how
 * much is left to give. */
export function MyResellersPage() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<ResellerRow | null>(null)
  const [confirm, setConfirm] = useState<{ kind: ConfirmKind; reseller: ResellerRow } | null>(null)

  const meQuery = useQuery({ queryKey: ['reseller-me'], queryFn: () => apiFetch<ResellerRow>('/api/v1/reseller/me') })
  const childrenQuery = useQuery({
    queryKey: ['my-children'],
    queryFn: () => apiFetch<{ resellers: ResellerRow[] | null }>('/api/v1/reseller/children'),
  })
  const me = meQuery.data
  const children = useMemo(() => childrenQuery.data?.resellers ?? [], [childrenQuery.data])
  const rows = useMemo(() => flattenTree(children), [children])
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['my-children'] })
    queryClient.invalidateQueries({ queryKey: ['reseller-me'] })
  }

  // What is left of this reseller's own plan after its accounts and what it handed down.
  const room: ChildRoom | undefined = me
    ? {
        accounts: me.max_accounts === null ? null : Math.max(0, me.max_accounts - me.usage.accounts_used - me.reserved_accounts),
        bytes:
          me.data_limit_bytes === null
            ? null
            : Math.max(0, me.data_limit_bytes - me.usage.data_allocated_bytes - me.reserved_data_bytes),
        expiresAt: me.effective_expires_at,
        nodeGroups: me.node_groups ?? [],
      }
    : undefined
  const canCreate = me ? me.depth < MAX_RESELLER_DEPTH : false

  const actionMutation = useMutation({
    mutationFn: async ({ kind, reseller }: { kind: ConfirmKind; reseller: ResellerRow }) => {
      const base = `/api/v1/reseller/children/${reseller.id}`
      switch (kind) {
        case 'suspend':
          return apiFetch<{ suspended: number }>(`${base}/suspend-accounts`, { method: 'POST' })
        case 'reactivate':
          return apiFetch<{ reactivated: number; still_suspended: number }>(`${base}/reactivate-accounts`, { method: 'POST' })
        case 'delete':
          return apiFetch<void>(base + (reseller.children > 0 ? '?cascade=true' : ''), { method: 'DELETE' })
        case 'disable':
          return apiFetch<ResellerRow>(base, { method: 'PATCH', body: JSON.stringify({ disabled: !reseller.disabled }) })
      }
    },
    onSuccess: (data, { kind, reseller }) => {
      setConfirm(null)
      invalidate()
      if (kind === 'suspend') push('success', t('{n} accounts suspended', { n: (data as { suspended: number }).suspended }))
      else if (kind === 'reactivate') {
        const r = data as { reactivated: number; still_suspended: number }
        push('success', t('{n} accounts reactivated · {m} still suspended', { n: r.reactivated, m: r.still_suspended }))
      } else if (kind === 'delete') push('success', t('Reseller {name} deleted', { name: reseller.username }))
      else push('success', reseller.disabled ? t('Reseller enabled') : t('Reseller disabled'))
    },
    onError: (err) => {
      setConfirm(null)
      push('error', apiErrorMessage(err, t))
    },
  })

  const confirmCopy: Record<ConfirmKind, { title: string; body: string; label: string; danger: boolean }> = {
    suspend: {
      title: t('Suspend all accounts?'),
      body: t("Every active account of {name} and of its sub-resellers stops working until you reactivate them. They can't re-enable them."),
      label: t('Suspend accounts'),
      danger: true,
    },
    reactivate: {
      title: t('Reactivate accounts?'),
      body: t('Accounts you suspended this way come back. Accounts that have since expired or used up their quota stay suspended.'),
      label: t('Reactivate'),
      danger: false,
    },
    delete: {
      title: t('Delete reseller?'),
      body: t('{name} will no longer be able to sign in. A reseller that still owns accounts cannot be deleted.'),
      label: t('Delete'),
      danger: true,
    },
    disable: {
      title: t('Disable reseller?'),
      body: t('{name} is signed out on the next request and cannot sign in. Their accounts keep working - suspend them separately if needed.'),
      label: t('Disable'),
      danger: true,
    },
  }
  const confirmBody = (kind: ConfirmKind, r: ResellerRow) =>
    kind === 'delete' && r.children > 0
      ? t('{name} and every sub-reseller below it will be deleted. This only works when none of them owns accounts.', { name: r.username })
      : confirmCopy[kind].body.replace('{name}', r.username)

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('My resellers')}
        description={t('Sub-resellers you sell through. Their plans come out of yours.')}
        action={
          <Button
            disabled={!canCreate}
            onClick={() => {
              setEditing(null)
              setFormOpen(true)
            }}
          >
            <Plus className="h-4 w-4" />
            {t('New sub-reseller')}
          </Button>
        }
      />

      {me && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3 sm:gap-4">
          <StatCard
            icon={Network}
            label={t('Sub-resellers')}
            value={children.length}
            hint={canCreate ? t('You can create up to {n} levels below you', { n: MAX_RESELLER_DEPTH - me.depth }) : t('Deepest level - no more sub-resellers')}
          />
          <StatCard
            icon={Users}
            label={t('Accounts left to give')}
            value={room?.accounts === null ? t('Unlimited') : (room?.accounts ?? 0)}
            hint={t('{n} reserved for sub-resellers', { n: me.reserved_accounts })}
          />
          <StatCard
            icon={HardDrive}
            label={t('Traffic credit left to give')}
            value={room?.bytes === null || room === undefined ? t('Unlimited') : ltr(formatGB(room.bytes))}
            hint={t('{amount} reserved for sub-resellers', { amount: ltr(formatGB(me.reserved_data_bytes)) })}
          />
        </div>
      )}

      <Card className="overflow-hidden">
        {childrenQuery.isLoading && <TableSkeleton cols={6} />}
        {childrenQuery.isError && <ErrorState title={t('Could not load resellers.')} onRetry={() => childrenQuery.refetch()} />}
        {!childrenQuery.isLoading && !childrenQuery.isError && children.length === 0 && (
          <EmptyState
            illustration="reseller"
            title={t('No sub-resellers yet')}
            description={t('Add a sub-reseller to let an agent of yours sell accounts out of your plan.')}
            action={
              canCreate ? (
                <Button onClick={() => setFormOpen(true)}>
                  <Plus className="h-4 w-4" />
                  {t('New sub-reseller')}
                </Button>
              ) : undefined
            }
          />
        )}
        {rows.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>{t('Reseller')}</Th>
                <Th>{t('Status')}</Th>
                <Th>{t('Accounts')}</Th>
                <Th>{t('Traffic credit')}</Th>
                <Th>{t('Plan expiry')}</Th>
                <Th className="text-end">{t('Actions')}</Th>
              </tr>
            </THead>
            <tbody>
              {rows.map(({ reseller: r, level }) => (
                <Tr key={r.id}>
                  <Td>
                    <div style={{ paddingInlineStart: `${level * 20}px` }}>
                      <p className="font-medium text-fg">
                        {level > 0 && <span className="me-1.5 text-faint">↳</span>}
                        {r.username}
                      </p>
                      {r.notes && <p className="mt-0.5 max-w-56 truncate text-xs text-faint">{r.notes}</p>}
                    </div>
                  </Td>
                  <Td>
                    <Badge dot tone={r.disabled ? 'slate' : r.effective_disabled ? 'amber' : 'green'}>
                      {r.disabled ? t('disabled') : r.effective_disabled ? t('parent disabled') : t('active')}
                    </Badge>
                  </Td>
                  <Td className="text-muted">
                    <Usage used={r.usage.accounts_used + r.reserved_accounts} max={r.max_accounts} format={String} />
                  </Td>
                  <Td className="text-muted">
                    <Usage used={r.usage.data_allocated_bytes + r.reserved_data_bytes} max={r.data_limit_bytes} format={formatGB} />
                  </Td>
                  <Td className="whitespace-nowrap">
                    <ExpiryBadge expiresAt={r.expires_at} soonDays={7} />
                  </Td>
                  <Td className="text-end">
                    <RowActions
                      actions={[
                        {
                          label: t('Edit'),
                          icon: Pencil,
                          onSelect: () => {
                            setEditing(r)
                            setFormOpen(true)
                          },
                        },
                        { label: t('Suspend all accounts'), icon: PauseCircle, onSelect: () => setConfirm({ kind: 'suspend', reseller: r }) },
                        { label: t('Reactivate accounts'), icon: PlayCircle, onSelect: () => setConfirm({ kind: 'reactivate', reseller: r }) },
                        r.disabled
                          ? // A reseller the owner disabled can only be enabled by the owner.
                            {
                              label: t('Enable reseller'),
                              icon: CheckCircle2,
                              onSelect: () => actionMutation.mutate({ kind: 'disable', reseller: r }),
                              disabled: r.disabled_by_staff,
                            }
                          : { label: t('Disable reseller'), icon: Ban, onSelect: () => setConfirm({ kind: 'disable', reseller: r }), danger: true },
                        { label: t('Delete'), icon: Trash2, onSelect: () => setConfirm({ kind: 'delete', reseller: r }), danger: true },
                      ]}
                    />
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <BrandingEditor
        endpoint="/api/v1/reseller/branding"
        title="Branding"
        description="How your customers' subscription page looks: your name, logo, support link and a notice. Sub-resellers without their own branding inherit it."
      />

      <ResellerFormDialog
        open={formOpen}
        mode="child"
        room={room}
        reseller={editing}
        onClose={() => setFormOpen(false)}
        onSaved={() => {
          setFormOpen(false)
          invalidate()
        }}
      />

      {confirm && (
        <ConfirmDialog
          open
          onClose={() => setConfirm(null)}
          onConfirm={() => actionMutation.mutate(confirm)}
          title={confirmCopy[confirm.kind].title}
          description={confirmBody(confirm.kind, confirm.reseller)}
          confirmLabel={confirmCopy[confirm.kind].label}
          danger={confirmCopy[confirm.kind].danger}
          submitting={actionMutation.isPending}
        />
      )}
    </div>
  )
}
