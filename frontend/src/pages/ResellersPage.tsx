import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, HardDrive, Pencil, PauseCircle, PlayCircle, Plus, Store, Trash2, Users } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Badge } from '../components/ui/Badge'
import { Progress } from '../components/ui/Progress'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Field } from '../components/ui/Field'
import { Input } from '../components/ui/Input'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { StatCard } from '../components/ui/StatCard'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { RowActions } from '../components/ui/RowActions'

interface Reseller {
  id: string
  username: string
  namespace: string
  disabled: boolean
  notes: string
  max_accounts: number | null
  data_limit_bytes: number | null
  node_groups: string[] | null
  max_expiry_days: number | null
  usage: { accounts_used: number; data_allocated_bytes: number; data_used_bytes: number }
  created_at: string
}

interface NodeSummary {
  node_group: string
}

// The reseller API takes limits in decimal GB (data_limit_gb) and returns bytes, like
// the accounts API's quotas - so everything here is shown in decimal GB too.
const BYTES_PER_GB = 1_000_000_000

function formatGB(bytes: number): string {
  const gb = bytes / BYTES_PER_GB
  return `${gb >= 100 ? Math.round(gb) : Number(gb.toFixed(gb >= 10 ? 1 : 2))} GB`
}

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
          return apiFetch<void>(base, { method: 'DELETE' })
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
            value={formatGB(totals.used)}
            hint={t('{amount} allocated', { amount: formatGB(totals.allocated) })}
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
                <Th>{t('Node groups')}</Th>
                <Th className="text-end">{t('Actions')}</Th>
              </tr>
            </THead>
            <tbody>
              {resellers.map((r) => (
                <Tr key={r.id}>
                  <Td>
                    <p className="font-medium text-fg">{r.username}</p>
                    {r.notes && <p className="mt-0.5 max-w-56 truncate text-xs text-faint">{r.notes}</p>}
                  </Td>
                  <Td>
                    <Badge dot tone={r.disabled ? 'slate' : 'green'}>{r.disabled ? t('disabled') : t('active')}</Badge>
                  </Td>
                  <Td className="text-muted">
                    <UsageCell used={r.usage.accounts_used} max={r.max_accounts} format={String} unlimited={t('Unlimited')} />
                  </Td>
                  <Td className="text-muted">
                    <UsageCell
                      used={r.usage.data_allocated_bytes}
                      max={r.data_limit_bytes}
                      format={formatGB}
                      unlimited={t('Unlimited')}
                    />
                  </Td>
                  <Td className="text-muted tabular-nums">{formatGB(r.usage.data_used_bytes)}</Td>
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
          description={confirmCopy[confirm.kind].body.replace('{name}', confirm.reseller.username)}
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
      <span className="tabular-nums">
        {format(used)} / {max === null ? unlimited : format(max)}
      </span>
      {max !== null && max > 0 && <Progress value={used} max={max} className="mt-1.5 max-w-32" />}
    </div>
  )
}

/** Empty input means "no limit"; the API takes null for unlimited. */
function toLimit(value: string): number | null {
  const trimmed = value.trim()
  return trimmed === '' ? null : Number(trimmed)
}

function ResellerFormDialog({
  open,
  reseller,
  onClose,
  onSaved,
}: {
  open: boolean
  reseller: Reseller | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const editing = reseller !== null
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [maxAccounts, setMaxAccounts] = useState('')
  const [dataLimitGB, setDataLimitGB] = useState('')
  const [maxExpiryDays, setMaxExpiryDays] = useState('')
  const [groups, setGroups] = useState<string[]>([])
  const [notes, setNotes] = useState('')
  const [error, setError] = useState<string | null>(null)

  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: NodeSummary[] | null }>('/api/v1/nodes'),
    enabled: open,
  })
  const availableGroups = useMemo(
    () => Array.from(new Set((nodesQuery.data?.nodes ?? []).map((n) => n.node_group))).sort(),
    [nodesQuery.data],
  )

  // The dialog stays mounted between opens; reset to the selected reseller each time.
  useEffect(() => {
    if (!open) return
    setUsername(reseller?.username ?? '')
    setPassword('')
    setMaxAccounts(reseller?.max_accounts?.toString() ?? '')
    setDataLimitGB(reseller?.data_limit_bytes != null ? String(reseller.data_limit_bytes / BYTES_PER_GB) : '')
    setMaxExpiryDays(reseller?.max_expiry_days?.toString() ?? '')
    setGroups(reseller?.node_groups ?? [])
    setNotes(reseller?.notes ?? '')
    setError(null)
  }, [open, reseller])

  const saveMutation = useMutation({
    mutationFn: () => {
      const limits = {
        max_accounts: toLimit(maxAccounts),
        data_limit_gb: toLimit(dataLimitGB),
        max_expiry_days: toLimit(maxExpiryDays),
        node_groups: groups,
        notes,
      }
      if (editing) {
        return apiFetch<Reseller>(`/api/v1/resellers/${reseller.id}`, {
          method: 'PATCH',
          body: JSON.stringify({ ...limits, ...(password ? { password } : {}) }),
        })
      }
      return apiFetch<Reseller>('/api/v1/resellers', {
        method: 'POST',
        body: JSON.stringify({ username, password, ...limits }),
      })
    },
    onSuccess: () => {
      push('success', editing ? t('Reseller updated') : t('Reseller {name} created', { name: username }))
      onSaved()
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  function toggleGroup(group: string) {
    setGroups((prev) => (prev.includes(group) ? prev.filter((g) => g !== group) : [...prev, group]))
  }

  return (
    <Dialog open={open} onClose={onClose} title={editing ? t('Edit reseller') : t('New reseller')} maxWidthClassName="max-w-lg">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('Username')}>
            <Input value={username} onChange={(e) => setUsername(e.target.value)} required disabled={editing} autoComplete="off" />
          </Field>
          <Field label={t('Password')} labelSuffix={editing ? t('(leave blank to keep)') : undefined}>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required={!editing}
              minLength={8}
              autoComplete="new-password"
            />
          </Field>
        </div>

        <div className="rounded-card border border-edge bg-inset/50 p-4">
          <p className="text-sm font-medium text-fg">{t('Limits')}</p>
          <p className="mt-0.5 text-xs text-muted">{t('Leave a field empty for no limit.')}</p>
          <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
            <Field label={t('Max accounts')}>
              <Input type="number" min="0" value={maxAccounts} onChange={(e) => setMaxAccounts(e.target.value)} placeholder={t('Unlimited')} />
            </Field>
            <Field label={t('Traffic credit (GB)')}>
              <Input type="number" min="0" step="any" value={dataLimitGB} onChange={(e) => setDataLimitGB(e.target.value)} placeholder={t('Unlimited')} />
            </Field>
            <Field label={t('Max expiry (days)')}>
              <Input type="number" min="1" value={maxExpiryDays} onChange={(e) => setMaxExpiryDays(e.target.value)} placeholder={t('Unlimited')} />
            </Field>
          </div>
          <p className="mt-3 text-xs leading-relaxed text-muted">
            {t('Traffic credit is the total data quota the reseller can hand out across its accounts. With a credit set, it cannot create unlimited accounts.')}
          </p>
        </div>

        <Field label={t('Node groups')} hint={t('None selected means all node groups.')}>
          {availableGroups.length === 0 ? (
            <p className="text-sm text-muted">{t('No node groups yet.')}</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {availableGroups.map((g) => {
                const on = groups.includes(g)
                return (
                  <button
                    key={g}
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggleGroup(g)}
                    className={`cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors duration-150 ${
                      on ? 'border-accent bg-accent/10 text-accent-fg' : 'border-edge text-muted hover:border-edge-strong hover:text-fg'
                    }`}
                  >
                    {g}
                  </button>
                )
              })}
            </div>
          )}
        </Field>

        <Field label={t('Notes')} labelSuffix={t('(optional)')}>
          <textarea
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            rows={2}
            className="w-full rounded-control border border-edge bg-surface px-3 py-2 text-sm text-fg transition-[border-color,box-shadow] duration-150 placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none"
          />
        </Field>

        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('Cancel')}
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? t('Saving…') : editing ? t('Save changes') : t('Create reseller')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
