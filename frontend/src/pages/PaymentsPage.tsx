import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Eye } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import {
  fetchBlobUrl,
  KIND_LABELS,
  METHOD_LABELS,
  ORDER_STATUSES,
  salesErrorMessage,
  STATUS_LABELS,
  statusTone,
  type OrderRow,
} from '../lib/sales'
import { useToman } from '../lib/salesHooks'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Badge } from '../components/ui/Badge'
import { Select } from '../components/ui/Select'
import { Dialog } from '../components/ui/Dialog'
import { Field } from '../components/ui/Field'
import { Input } from '../components/ui/Input'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { PaymentSettingsCard } from '../components/sales/PaymentSettingsCard'

type Tab = 'orders' | 'settings'

/** Payments: the order list staff review (approve / reject card-to-card receipts, watch
 * gateway payments) and, for super admins, the payment settings. */
export function PaymentsPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const isSuper = user?.role === 'super_admin'
  const [tab, setTab] = useState<Tab>('orders')

  return (
    <div>
      <PageHeader title="Payments" description="Review card-to-card receipts, follow gateway payments and set up how people pay." />
      {isSuper && (
        <div className="mb-5 inline-flex gap-1 rounded-lg bg-inset p-1" role="tablist">
          {(['orders', 'settings'] as const).map((k) => (
            <button
              key={k}
              type="button"
              role="tab"
              aria-selected={tab === k}
              onClick={() => setTab(k)}
              className={`cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium transition-colors ${tab === k ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-muted hover:text-fg'}`}
            >
              {t(k === 'orders' ? 'Orders' : 'Settings')}
            </button>
          ))}
        </div>
      )}
      {tab === 'orders' || !isSuper ? <OrdersPanel /> : <PaymentSettingsCard />}
    </div>
  )
}

const PAGE_SIZES = [50, 100, 200]

function OrdersPanel() {
  const { t, formatDate } = useI18n()
  const toman = useToman()
  const [status, setStatus] = useState<string>('awaiting_review')
  const [limit, setLimit] = useState(PAGE_SIZES[0])
  const [selected, setSelected] = useState<OrderRow | null>(null)

  const query = useQuery({
    queryKey: ['orders', status, limit],
    queryFn: () => apiFetch<{ items: OrderRow[] }>(`/api/v1/orders?limit=${limit}${status ? `&status=${status}` : ''}`),
    refetchInterval: 30_000,
  })
  const orders = query.data?.items ?? []

  return (
    <>
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-3 border-b border-edge px-5 py-3">
          <label className="text-sm text-muted" htmlFor="order-status">
            {t('Status')}
          </label>
          <Select id="order-status" className="w-56" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">{t('All')}</option>
            {ORDER_STATUSES.map((s) => (
              <option key={s} value={s}>
                {t(STATUS_LABELS[s])}
              </option>
            ))}
          </Select>
        </div>
        {query.isLoading && <TableSkeleton cols={6} />}
        {query.isError && <ErrorState title="Could not load orders." onRetry={() => query.refetch()} />}
        {!query.isLoading && !query.isError && orders.length === 0 && (
          <EmptyState illustration="success" title="No orders here" description="Orders show up when someone buys a plan or tops up a wallet." />
        )}
        {orders.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Date</Th>
                <Th>Type</Th>
                <Th>Buyer</Th>
                <Th>Amount</Th>
                <Th>Method</Th>
                <Th>Status</Th>
                <Th className="text-end">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {orders.map((o) => (
                <Tr key={o.id} interactive onClick={() => setSelected(o)}>
                  <Td className="whitespace-nowrap text-muted">{formatDate(o.created_at)}</Td>
                  <Td className="whitespace-nowrap">{t(KIND_LABELS[o.kind] ?? o.kind)}</Td>
                  <Td>
                    <BuyerCell o={o} />
                  </Td>
                  <Td className="whitespace-nowrap font-medium tabular-nums text-fg">{toman(o.amount_toman)}</Td>
                  <Td className="whitespace-nowrap text-muted">{t(METHOD_LABELS[o.method])}</Td>
                  <Td>
                    <StatusBadge o={o} />
                  </Td>
                  <Td className="text-end">
                    <Button variant="ghost" size="icon" aria-label={t('View')} onClick={() => setSelected(o)}>
                      <Eye className="h-4 w-4" />
                    </Button>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
        {orders.length >= limit && limit < PAGE_SIZES[PAGE_SIZES.length - 1] && (
          <div className="border-t border-edge p-3 text-center">
            <Button variant="secondary" size="sm" onClick={() => setLimit((l) => PAGE_SIZES[Math.min(PAGE_SIZES.indexOf(l) + 1, PAGE_SIZES.length - 1)])}>
              Show more
            </Button>
          </div>
        )}
      </Card>
      {selected && <OrderDialog orderId={selected.id} initial={selected} onClose={() => setSelected(null)} />}
    </>
  )
}

function StatusBadge({ o }: { o: OrderRow }) {
  const { t } = useI18n()
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <Badge dot tone={statusTone(o.status)}>
        {t(STATUS_LABELS[o.status])}
      </Badge>
      {o.status === 'paid' && o.fulfil_error && <Badge tone="red">Delivery failed</Badge>}
    </span>
  )
}

function BuyerCell({ o }: { o: OrderRow }) {
  const { t } = useI18n()
  const main = o.kind === 'wallet_topup' ? (o.reseller_username ?? '—') : (o.account_label ?? o.contact ?? '—')
  const sub = o.kind === 'account_purchase' ? o.contact : o.plan_name
  return (
    <div className="min-w-0">
      <p className="truncate font-medium text-fg">{main}</p>
      {sub && <p className="truncate text-xs text-faint">{o.kind === 'wallet_topup' ? t('Wallet top-up') : sub}</p>}
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  const { t } = useI18n()
  return (
    <div className="flex items-start justify-between gap-4 py-1.5 text-sm">
      <span className="shrink-0 text-muted">{t(label)}</span>
      <span className="min-w-0 break-words text-end text-fg">{children}</span>
    </div>
  )
}

function Receipt({ orderId }: { orderId: string }) {
  const { t } = useI18n()
  const query = useQuery({
    queryKey: ['order-receipt', orderId],
    queryFn: () => fetchBlobUrl(`/api/v1/orders/${orderId}/receipt`),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })
  const url = query.data
  // Object URLs hold the image in memory until revoked.
  useEffect(() => () => (url ? URL.revokeObjectURL(url) : undefined), [url])
  if (query.isLoading) return <p className="text-xs text-muted">{t('Loading…')}</p>
  if (query.isError || !url) return <p className="text-xs text-muted">{t('No receipt image available.')}</p>
  return (
    <a href={url} target="_blank" rel="noreferrer">
      <img src={url} alt={t('Receipt')} className="max-h-72 w-full rounded-lg border border-edge object-contain" />
    </a>
  )
}

function OrderDialog({ orderId, initial, onClose }: { orderId: string; initial: OrderRow; onClose: () => void }) {
  const { t, formatDate } = useI18n()
  const toman = useToman()
  const { user } = useAuth()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const canReview = user?.role === 'super_admin' || user?.role === 'operator'
  const [note, setNote] = useState('')
  const [error, setError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['order', orderId],
    queryFn: () => apiFetch<OrderRow>(`/api/v1/orders/${orderId}`),
    initialData: initial,
    refetchInterval: 10_000,
  })
  const o = query.data

  const act = useMutation({
    mutationFn: ({ action, force }: { action: 'approve' | 'reject' | 'retry' | 'cancel'; force?: boolean }) =>
      apiFetch<OrderRow>(`/api/v1/orders/${orderId}/${action}${force ? '?force=true' : ''}`, {
        method: 'POST',
        body: action === 'approve' || action === 'reject' ? JSON.stringify({ note: note.trim() }) : undefined,
      }),
    onSuccess: (saved, { action }) => {
      push('success', t(action === 'approve' ? 'Order approved' : action === 'reject' ? 'Order rejected' : action === 'retry' ? 'Delivery retried' : 'Order cancelled'))
      queryClient.setQueryData(['order', orderId], saved)
      queryClient.invalidateQueries({ queryKey: ['orders'] })
      setError(null)
    },
    onError: (err) => {
      setError(salesErrorMessage(err, t, 'The action failed'))
      queryClient.invalidateQueries({ queryKey: ['order', orderId] })
    },
  })

  const awaiting = o.status === 'awaiting_review'
  const stuckClaim = o.status === 'paid' && o.fulfil_in_progress && Date.now() - new Date(o.updated_at).getTime() > 5 * 60_000

  return (
    <Dialog open onClose={onClose} title={t('Order details')} maxWidthClassName="max-w-lg">
      <div className="divide-y divide-edge/70">
        <Row label="Status">
          <StatusBadge o={o} />
        </Row>
        <Row label="Type">{t(KIND_LABELS[o.kind] ?? o.kind)}</Row>
        <Row label="Amount">{toman(o.amount_toman)}</Row>
        <Row label="Method">{t(METHOD_LABELS[o.method])}</Row>
        {o.plan_name && <Row label="Plan">{o.plan_name}</Row>}
        {o.reseller_username && <Row label="Reseller">{o.reseller_username}</Row>}
        {o.account_label && <Row label="Account">{o.account_label}</Row>}
        {o.contact && <Row label="Contact">{<span className="ltr">{o.contact}</span>}</Row>}
        {o.tracking_code && <Row label="Tracking code">{<span className="ltr font-mono">{o.tracking_code}</span>}</Row>}
        {o.card && (
          <Row label="Paid to card">
            <span className="ltr font-mono">{o.card.number.replace(/(\d{4})(?=\d)/g, '$1 ')}</span>
          </Row>
        )}
        {o.gateway_ref && <Row label="Gateway reference">{<span className="ltr font-mono text-xs">{o.gateway_ref}</span>}</Row>}
        <Row label="Created">{formatDate(o.created_at)}</Row>
        {o.reviewer && <Row label="Reviewed by">{o.reviewer}</Row>}
        {o.review_note && <Row label="Note">{o.review_note}</Row>}
      </div>

      {o.has_receipt && (
        <div className="mt-4">
          <p className="mb-2 text-sm font-medium text-fg">{t('Receipt')}</p>
          <Receipt orderId={o.id} />
        </div>
      )}

      {o.status === 'paid' && o.fulfil_error && (
        <p className="mt-4 rounded-lg bg-danger-soft p-3 text-sm text-danger-fg">
          {t('Paid, but delivery failed: {error}. It is retried automatically; you can also retry now.', { error: o.fulfil_error })}
        </p>
      )}

      {canReview && awaiting && (
        <div className="mt-4">
          <Field label="Note to the buyer" labelSuffix="(optional)">
            <Input value={note} onChange={(e) => setNote(e.target.value)} maxLength={300} />
          </Field>
        </div>
      )}
      {error && <p className="mt-3 text-sm text-danger-fg">{error}</p>}
      {canReview && (
        <div className="mt-5 flex flex-wrap justify-end gap-2">
          {awaiting && (
            <>
              <Button variant="danger" disabled={act.isPending} onClick={() => act.mutate({ action: 'reject' })}>
                Reject
              </Button>
              <Button disabled={act.isPending} onClick={() => act.mutate({ action: 'approve' })}>
                Approve
              </Button>
            </>
          )}
          {(o.status === 'pending' || awaiting) && (
            <Button variant="secondary" disabled={act.isPending} onClick={() => act.mutate({ action: 'cancel' })}>
              Cancel order
            </Button>
          )}
          {o.status === 'paid' && (
            <Button variant="secondary" disabled={act.isPending || (o.fulfil_in_progress && !stuckClaim)} onClick={() => act.mutate({ action: 'retry', force: stuckClaim })}>
              {stuckClaim ? 'Retry (delivery was interrupted)' : 'Retry delivery'}
            </Button>
          )}
        </div>
      )}
    </Dialog>
  )
}
