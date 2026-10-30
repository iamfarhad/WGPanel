import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, HardDrive, Pencil, PauseCircle, PlayCircle, Plus, Store, Trash2, Users } from 'lucide-react'
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
import { ResellerFormDialog } from '../components/resellers/ResellerFormDialog'
import { flattenTree, formatGB, type ResellerRow } from '../lib/resellerTree'

type Reseller = ResellerRow

type ConfirmKind = 'suspend' | 'reactivate' | 'delete' | 'disable'

export function ResellersPage() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Reseller | null>(null)
  const [confirm, setConfirm] = useState<{ kind: ConfirmKind; reseller: Reseller } | null>(null)

  const resellersQuery = useQuery({
    queryKey: ['resellers'],
    queryFn: () => apiFetch<{ resellers: Reseller[] | null }>('/api/v1/resellers'),
  })
  const resellers = useMemo(() => resellersQuery.data?.resellers ?? [], [resellersQuery.data])
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['resellers'] })

  const totals = useMemo(
    () => ({
      disabled: resellers.filter((r) => r.disabled).length,
      accounts: resellers.reduce((n, r) => n + r.usage.accounts_used, 0),
      used: resellers.reduce((n, r) => n + r.usage.data_used_bytes, 0),
      allocated: resellers.reduce((n, r) => n + r.usage.data_allocated_bytes, 0),
    }),
    [resellers],
  )

  const actionMutation = useMutation({
    mutationFn: async ({ kind, reseller }: { kind: ConfirmKind; reseller: Reseller }) => {
      const base = `/api/v1/resellers/${reseller.id}`
      switch (kind) {
        case 'suspend':
          return apiFetch<{ suspended: number }>(`${base}/suspend-accounts`, { method: 'POST' })
        case 'reactivate':
          return apiFetch<{ reactivated: number; still_suspended: number }>(`${base}/reactivate-accounts`, { method: 'POST' })
        case 'delete':
          // A reseller with sub-resellers needs an explicit cascade (the server refuses otherwise).
          return apiFetch<void>(base + (reseller.children > 0 ? '?cascade=true' : ''), { method: 'DELETE' })
        case 'disable':
          return apiFetch<Reseller>(base, { method: 'PATCH', body: JSON.stringify({ disabled: !reseller.disabled }) })
      }
    },
    onSuccess: (data, { kind, reseller }) => {
      setConfirm(null)
      invalidate()
      if (kind === 'suspend') {
        push('success', t('{n} accounts suspended', { n: (data as { suspended: number }).suspended }))
      } else if (kind === 'reactivate') {
        const r = data as { reactivated: number; still_suspended: number }
        push('success', t('{n} accounts reactivated · {m} still suspended', { n: r.reactivated, m: r.still_suspended }))
      } else if (kind === 'delete') {
        push('success', t('Reseller {name} deleted', { name: reseller.username }))
      } else {
        push('success', reseller.disabled ? t('Reseller enabled') : t('Reseller disabled'))
      }
    },
    onError: (err) => {
      setConfirm(null)
      push('error', apiErrorMessage(err, t))
    },
  })

  const confirmCopy: Record<ConfirmKind, { title: string; body: string; label: string; danger: boolean }> = {
    suspend: {
      title: t('Suspend all accounts?'),
      body: t("Every active account of {name} stops working until you reactivate them. The reseller can't re-enable them."),
      label: t('Suspend accounts'),
      danger: true,
    },
    reactivate: {
      title: t('Reactivate accounts?'),
      body: t('Accounts suspended by the bulk action come back. Accounts that have since expired or used up their quota stay suspended.'),
      label: t('Reactivate'),
      danger: false,
    },
    delete: {
      title: t('Delete reseller?'),
      body: t('{name} will no longer be able to sign in. A reseller that still owns accounts cannot be deleted.'),
      // Shown instead when the reseller has sub-resellers (see confirmBody).
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

  const confirmBody = (kind: ConfirmKind, r: Reseller) =>
    kind === 'delete' && r.children > 0
      ? t('{name} and every sub-reseller below it will be deleted. This only works when none of them owns accounts.', { name: r.username })
      : confirmCopy[kind].body.replace('{name}', r.username)

  const rows = useMemo(() => flattenTree(resellers), [resellers])

  return (
    <div>
      <PageHeader
        title={t('Resellers')}
        description={t('Partners who sell WireGuard accounts under their own limits.')}
        action={
          <Button
            onClick={() => {
              setEditing(null)
              setFormOpen(true)
            }}
          >
            <Plus className="h-4 w-4" />
            {t('New reseller')}
          </Button>
        }
      />

      {resellers.length > 0 && (
        <div className="mb-6 grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-3">
          <StatCard
            icon={Store}
            label={t('Resellers')}
            value={resellers.length}
            hint={totals.disabled > 0 ? t('{n} disabled', { n: totals.disabled }) : t('All active')}
          />
          <StatCard icon={Users} label={t('Reseller accounts')} value={totals.accounts} hint={t('Across all resellers')} />
          <StatCard
            icon={HardDrive}
            label={t('Traffic used')}
            value={ltr(formatGB(totals.used))}
            hint={t('{amount} allocated', { amount: ltr(formatGB(totals.allocated)) })}
          />
        </div>
      )}

      <Card className="overflow-hidden">
        {resellersQuery.isLoading && <TableSkeleton cols={6} />}
        {resellersQuery.isError && <ErrorState title={t('Could not load resellers.')} onRetry={() => resellersQuery.refetch()} />}
        {!resellersQuery.isLoading && !resellersQuery.isError && resellers.length === 0 && (
          <EmptyState
            illustration="reseller"
            title={t('No resellers yet')}
            description={t('Add a reseller to let a partner sell accounts under their own limits.')}
            action={
              <Button onClick={() => setFormOpen(true)}>
                <Plus className="h-4 w-4" />
                {t('New reseller')}
              </Button>
            }
          />
        )}
        {resellers.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>{t('Reseller')}</Th>
                <Th>{t('Status')}</Th>
                <Th>{t('Accounts')}</Th>
                <Th>{t('Traffic credit')}</Th>
                <Th>{t('Traffic used')}</Th>
                <Th>{t('Plan expiry')}</Th>
                <Th>{t('Node groups')}</Th>
                <Th className="text-end">{t('Actions')}</Th>
              </tr>
            </THead>
            <tbody>
              {rows.map(({ reseller: r, level }) => (
                <Tr key={r.id}>
                  <Td>
                    {/* Tree view: sub-resellers sit indented under their parent. */}
                    <div style={{ paddingInlineStart: `${level * 20}px` }}>
                      <p className="font-medium text-fg">
                        {level > 0 && <span className="me-1.5 text-faint">↳</span>}
                        {r.username}
                        {r.children > 0 && (
                          <Badge className="ms-2" tone="blue">
                            {t('{n} sub-resellers', { n: r.children })}
                          </Badge>
                        )}
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
                    <UsageCell used={r.usage.accounts_used + r.reserved_accounts} max={r.max_accounts} format={String} unlimited={t('Unlimited')} />
                  </Td>
                  <Td className="text-muted">
                    <UsageCell
                      used={r.usage.data_allocated_bytes + r.reserved_data_bytes}
                      max={r.data_limit_bytes}
                      format={formatGB}
                      unlimited={t('Unlimited')}
                    />
                  </Td>
                  <Td className="text-muted tabular-nums">{ltr(formatGB(r.usage.data_used_bytes))}</Td>
                  <Td className="whitespace-nowrap">
                    <ExpiryBadge expiresAt={r.expires_at} soonDays={7} />
                  </Td>
                  <Td className="text-muted">
                    {r.node_groups && r.node_groups.length > 0 ? (
                      <span className="flex flex-wrap gap-1">
                        {r.node_groups.map((g) => (
                          <Badge key={g} tone="blue">
                            {g}
                          </Badge>
                        ))}
                      </span>
                    ) : (
                      t('All')
                    )}
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
                          ? { label: t('Enable reseller'), icon: CheckCircle2, onSelect: () => actionMutation.mutate({ kind: 'disable', reseller: r }) }
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

      <ResellerFormDialog
        open={formOpen}
        reseller={editing}
        parents={resellers}
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

function UsageCell({
  used,
  max,
  format,
  unlimited,
}: {
  used: number
  max: number | null
  format: (n: number) => string
  unlimited: string
}) {
  return (
    <div className="min-w-32">
      {/* One isolate around "used / max": isolating the numbers separately still lets
          RTL flip their order, which reads as usage exceeding the limit. */}
      <span className="tabular-nums">{ltr(`${format(used)} / ${max === null ? unlimited : format(max)}`)}</span>
      {max !== null && max > 0 && <Progress value={used} max={max} className="mt-1.5 max-w-32" />}
    </div>
  )
}
