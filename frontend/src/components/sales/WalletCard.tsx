import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import {
  LEDGER_LABELS,
  METHOD_LABELS,
  parseToman,
  salesErrorMessage,
  STATUS_LABELS,
  statusTone,
  uploadReceipt,
  type LedgerEntry,
  type OrderRow,
  type PayMethod,
  type WalletInfo,
} from '../../lib/sales'
import { useToman } from '../../lib/salesHooks'
import { Badge } from '../ui/Badge'
import { Button } from '../ui/Button'
import { Card, CardHeader } from '../ui/Card'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Skeleton } from '../ui/Skeleton'
import { CardInstructions, ReceiptForm, type CardInfo } from './ReceiptForm'

interface PublicOrder {
  ref: string
  status: string
  amount_toman: number
  card?: CardInfo
  redirect_url?: string
}

interface TopupResponse {
  order: OrderRow
  payment: PublicOrder
}

/** The reseller's wallet on its dashboard: balance, top-up (gateway redirect or
 * card-to-card with receipt), the recent top-up orders and the ledger. Mounted by
 * DashboardPage for reseller sessions as one element. */
export function WalletCard() {
  const { t, formatDate } = useI18n()
  const toman = useToman()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [topupOpen, setTopupOpen] = useState(false)
  const [receiptFor, setReceiptFor] = useState<OrderRow | null>(null)

  const wallet = useQuery({ queryKey: ['wallet'], queryFn: () => apiFetch<WalletInfo>('/api/v1/wallet') })
  const ledger = useQuery({
    queryKey: ['wallet-ledger'],
    queryFn: () => apiFetch<{ items: LedgerEntry[] }>('/api/v1/wallet/ledger?limit=8'),
  })
  const topups = useQuery({
    queryKey: ['wallet-topups'],
    queryFn: () => apiFetch<{ items: OrderRow[] }>('/api/v1/orders?kind=wallet_topup&limit=5'),
    refetchInterval: 20_000,
  })

  // Returning from a gateway lands on /dashboard?order=<id>: refresh and tell the user.
  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get('order')
    if (!id) return
    window.history.replaceState(null, '', window.location.pathname)
    apiFetch<OrderRow>(`/api/v1/orders/${id}`)
      .then((o) => {
        push(o.status === 'fulfilled' ? 'success' : 'error', t(o.status === 'fulfilled' ? 'Payment received - your wallet was topped up.' : 'The payment is not confirmed yet. Check the status below.'))
        queryClient.invalidateQueries({ queryKey: ['wallet'] })
        queryClient.invalidateQueries({ queryKey: ['wallet-ledger'] })
        queryClient.invalidateQueries({ queryKey: ['wallet-topups'] })
      })
      .catch(() => undefined)
    // runs once on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const w = wallet.data
  const open = (topups.data?.items ?? []).filter((o) => o.status === 'pending' && o.method === 'card_to_card' && !o.has_receipt)

  return (
    <Card>
      <CardHeader
        title="Wallet"
        description="Your balance for priced plans"
        action={
          w && w.methods.length > 0 ? (
            <Button size="sm" onClick={() => setTopupOpen(true)}>
              <Plus className="h-4 w-4" />
              Top up
            </Button>
          ) : undefined
        }
      />
      <div className="space-y-4 p-5">
        {!w ? (
          <Skeleton className="h-20" />
        ) : (
          <>
            <div className="flex items-baseline justify-between">
              <span className="text-sm text-muted">{t('Balance')}</span>
              <span className="text-2xl font-semibold tabular-nums text-fg">{toman(w.balance_toman)}</span>
            </div>
            {w.charge_wallet ? (
              <p className="text-xs leading-relaxed text-muted">
                {t('Creating or renewing an account from a priced plan takes its price from this balance.')}
                {w.discount_percent > 0 && ` ${t('Your discount: {n}%.', { n: w.discount_percent })}`}
              </p>
            ) : null}
            {w.methods.length === 0 && <p className="text-xs text-muted">{t('Online top-up is not set up yet. Ask the panel admin to credit your wallet.')}</p>}
          </>
        )}

        {(topups.data?.items.length ?? 0) > 0 && (
          <div>
            <p className="mb-1.5 text-xs font-medium text-muted">{t('Recent top-ups')}</p>
            {topups.data?.items.map((o) => (
              <div key={o.id} className="flex items-center justify-between gap-3 border-b border-edge/70 py-2 text-sm last:border-0">
                <div className="min-w-0">
                  <p className="tabular-nums text-fg">{toman(o.amount_toman)}</p>
                  <p className="text-xs text-faint">
                    {t(METHOD_LABELS[o.method])} · {formatDate(o.created_at)}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Badge dot tone={statusTone(o.status)}>
                    {t(STATUS_LABELS[o.status])}
                  </Badge>
                  {open.some((x) => x.id === o.id) && (
                    <Button size="sm" variant="secondary" onClick={() => setReceiptFor(o)}>
                      Send receipt
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}

        {(ledger.data?.items.length ?? 0) > 0 && (
          <div>
            <p className="mb-1.5 text-xs font-medium text-muted">{t('Latest movements')}</p>
            {ledger.data?.items.map((e) => (
              <div key={e.id} className="flex items-center justify-between gap-3 py-1.5 text-sm">
                <span className="min-w-0 truncate text-muted">
                  {t(LEDGER_LABELS[e.kind] ?? e.kind)} · {formatDate(e.created_at)}
                </span>
                <span className={`shrink-0 tabular-nums ${e.amount_toman < 0 ? 'text-danger-fg' : 'text-success-fg'}`}>{toman(e.amount_toman)}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      {topupOpen && w && <TopupDialog methods={w.methods} onClose={() => setTopupOpen(false)} />}
      {receiptFor && (
        <ReceiptDialog
          order={receiptFor}
          onClose={() => setReceiptFor(null)}
          onDone={() => {
            setReceiptFor(null)
            queryClient.invalidateQueries({ queryKey: ['wallet-topups'] })
          }}
        />
      )}
    </Card>
  )
}

function ReceiptDialog({ order, card, onClose, onDone }: { order: OrderRow; card?: CardInfo | null; onClose: () => void; onDone: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const [error, setError] = useState<string | null>(null)
  const send = useMutation({
    mutationFn: ({ tracking, file }: { tracking: string; file: File }) =>
      uploadReceipt<OrderRow>(`/api/v1/orders/${order.id}/receipt`, tracking, file, true),
    onSuccess: () => {
      push('success', t('Receipt sent. The admin will review it.'))
      onDone()
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Failed to send the receipt')),
  })
  const c = card ?? order.card
  return (
    <Dialog open onClose={onClose} title={t('Send the transfer receipt')}>
      <div className="space-y-4">
        {c && <CardInstructions card={c} amount={order.amount_toman} />}
        <ReceiptForm pending={send.isPending} error={error} onSubmit={(tracking, file) => send.mutate({ tracking, file })} />
      </div>
    </Dialog>
  )
}

function TopupDialog({ methods, onClose }: { methods: PayMethod[]; onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [amount, setAmount] = useState('')
  const [method, setMethod] = useState<PayMethod>(methods[0])
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState<TopupResponse | null>(null)

  const create = useMutation({
    mutationFn: (body: { amount_toman: number; method: PayMethod }) =>
      apiFetch<TopupResponse>('/api/v1/wallet/topups', { method: 'POST', body: JSON.stringify(body) }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ['wallet-topups'] })
      if (res.payment.redirect_url) {
        window.location.href = res.payment.redirect_url
        return
      }
      setCreated(res)
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Failed to start the top-up')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    const n = parseToman(amount)
    if (Number.isNaN(n) || n < 10_000 || n > 1_000_000_000) {
      setError(t('Enter an amount between 10,000 and 1,000,000,000 Toman.'))
      return
    }
    setError(null)
    create.mutate({ amount_toman: n, method })
  }

  if (created) {
    return (
      <ReceiptDialog
        order={created.order}
        card={created.payment.card}
        onClose={onClose}
        onDone={() => {
          push('success', t('Receipt sent. The admin will review it.'))
          onClose()
        }}
      />
    )
  }

  return (
    <Dialog open onClose={onClose} title={t('Top up the wallet')}>
      <form onSubmit={submit} className="space-y-4">
        <Field label="Amount (Toman)">
          <Input dir="ltr" inputMode="numeric" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="500000" autoFocus />
        </Field>
        <Field label="Payment method">
          <Select value={method} onChange={(e) => setMethod(e.target.value as PayMethod)}>
            {methods.map((m) => (
              <option key={m} value={m}>
                {t(METHOD_LABELS[m])}
              </option>
            ))}
          </Select>
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {create.isPending ? t('Please wait…') : t('Continue')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

