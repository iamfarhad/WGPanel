import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Clock, Copy } from 'lucide-react'
import { useI18n } from '../../lib/i18n'
import { ltr } from '../../lib/bidi'
import {
  METHOD_LABELS,
  publicFetch,
  quotaText,
  salesErrorMessage,
  uploadReceipt,
  type PayMethod,
} from '../../lib/sales'
import { useToman } from '../../lib/salesHooks'
import { Button } from '../ui/Button'
import { Card } from '../ui/Card'
import { Input } from '../ui/Input'
import { CardInstructions, ReceiptForm, type CardInfo } from './ReceiptForm'

interface Plan {
  id: string
  name: string
  price_toman: number
  data_quota_bytes: number | null
  duration_days: number | null
  device_limit: number | null
}

interface PublicOrder {
  ref: string
  kind: string
  status: string
  method: PayMethod
  amount_toman: number
  plan_name: string
  review_note?: string
  card?: CardInfo
  redirect_url?: string
  receipt_submitted: boolean
  subscription_path?: string
}

interface Shop {
  enabled: boolean
  plans: Plan[]
  methods: PayMethod[]
  orders: PublicOrder[]
  support_contact: string | null
}

const OPEN = ['pending', 'awaiting_review', 'paid']

/** The public buying flow shared by the subscription page (renew THIS account:
 * base = /api/v1/sub/{token}) and /buy (new account: base = /api/v1/sub/buy, needs a
 * contact). Everything the buyer sees - plans, prices, methods, order state - comes from
 * the server; the page never sends an amount. */
export function PublicCheckout({
  base,
  needContact,
  title,
  storageKey,
  onFulfilled,
}: {
  base: string
  needContact: boolean
  title: string
  /** /buy has no account to look an order up by: remember the order ref in this browser. */
  storageKey?: string
  onFulfilled?: (o: PublicOrder) => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()

  const shop = useQuery({ queryKey: ['shop', base], queryFn: () => publicFetch<Shop>(`${base}/shop`), retry: false })

  const [ref, setRef] = useState<string | null>(() => {
    try {
      return new URLSearchParams(window.location.search).get('order') ?? (storageKey ? localStorage.getItem(storageKey) : null)
    } catch {
      return null
    }
  })
  // On the subscription page the shop lists recent orders: resume the newest open one.
  // "Start over" dismisses that resumption for this visit, or the buyer could never pick again.
  const [dismissed, setDismissed] = useState(false)
  const resume = !ref && !needContact && !dismissed ? shop.data?.orders.find((o) => OPEN.includes(o.status))?.ref : undefined
  const activeRef = ref ?? resume ?? null

  function remember(next: string | null) {
    setRef(next)
    setDismissed(next === null)
    try {
      if (storageKey) {
        if (next) localStorage.setItem(storageKey, next)
        else localStorage.removeItem(storageKey)
      }
    } catch {
      // storage blocked - the order simply is not remembered across visits
    }
    if (!next && window.location.search.includes('order=')) window.history.replaceState(null, '', window.location.pathname)
  }

  const order = useQuery({
    queryKey: ['public-order', base, activeRef],
    queryFn: () => publicFetch<PublicOrder>(`${base}/orders/${encodeURIComponent(activeRef ?? '')}`),
    enabled: !!activeRef,
    retry: false,
    refetchInterval: (q) => (q.state.data && !OPEN.includes(q.state.data.status) ? false : 5000),
  })
  const o = order.data

  const status = o?.status
  const ordRef = o?.ref
  useEffect(() => {
    if (status === 'fulfilled' && o) {
      onFulfilled?.(o)
      queryClient.invalidateQueries({ queryKey: ['shop', base] })
    }
    // fire once per fulfilment of this order
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, ordRef])

  if (shop.isLoading) return null
  // An unknown/expired ref (404) just falls back to the plan list.
  const showOrder = !!o && !!activeRef
  if (!showOrder && (shop.isError || !shop.data?.enabled)) {
    return needContact ? <ClosedNotice supportContact={shop.data?.support_contact ?? null} /> : null
  }

  return (
    <Card className="p-5">
      <h2 className="mb-3 text-sm font-semibold text-fg">{t(title)}</h2>
      {showOrder && o ? (
        <OrderPanel base={base} order={o} onBuyAgain={() => remember(null)} onChanged={() => queryClient.invalidateQueries({ queryKey: ['public-order', base, activeRef] })} />
      ) : (
        shop.data && <PlanPicker base={base} shop={shop.data} needContact={needContact} onCreated={(r) => remember(r)} />
      )}
    </Card>
  )
}

function ClosedNotice({ supportContact }: { supportContact: string | null }) {
  const { t } = useI18n()
  return (
    <Card className="p-5 text-center">
      <p className="text-sm text-muted">{t('Buying is not open right now.')}</p>
      {supportContact && (
        <p className="mt-2 text-xs text-muted">
          {t('Need help?')} <span className="ltr font-mono text-fg">{supportContact}</span>
        </p>
      )}
    </Card>
  )
}

function PlanPicker({ base, shop, needContact, onCreated }: { base: string; shop: Shop; needContact: boolean; onCreated: (ref: string) => void }) {
  const { t } = useI18n()
  const toman = useToman()
  const [planId, setPlanId] = useState(shop.plans[0]?.id ?? '')
  const [method, setMethod] = useState<PayMethod>(shop.methods[0])
  const [contact, setContact] = useState('')
  const [error, setError] = useState<string | null>(null)

  const create = useMutation({
    mutationFn: () =>
      publicFetch<PublicOrder>(`${base}/orders`, { method: 'POST', body: JSON.stringify({ template_id: planId, method, contact: contact.trim() }) }),
    onSuccess: (res) => {
      if (res.redirect_url) {
        // Remember the order first: the buyer comes back from the gateway to this page.
        onCreated(res.ref)
        window.location.href = res.redirect_url
        return
      }
      onCreated(res.ref)
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Could not create the order')),
  })

  return (
    <div className="space-y-4">
      <div className="space-y-2" role="radiogroup" aria-label={t('Plan')}>
        {shop.plans.map((p) => (
          <label
            key={p.id}
            className={`flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3 transition-colors ${planId === p.id ? 'border-accent bg-accent-soft/40' : 'border-edge hover:bg-inset/50'}`}
          >
            <span className="flex min-w-0 items-center gap-2.5">
              <input type="radio" name="plan" checked={planId === p.id} onChange={() => setPlanId(p.id)} />
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium text-fg">{p.name}</span>
                <span className="block text-xs text-muted">
                  {ltr(quotaText(p.data_quota_bytes, t))}
                  {p.duration_days != null && ` · ${t('{n} days', { n: p.duration_days })}`}
                  {p.device_limit != null && ` · ${t('{n} devices', { n: p.device_limit })}`}
                </span>
              </span>
            </span>
            <span className="shrink-0 text-sm font-semibold tabular-nums text-fg">{toman(p.price_toman)}</span>
          </label>
        ))}
      </div>

      <div>
        <p className="mb-1.5 text-xs text-muted">{t('Payment method')}</p>
        <div className="flex flex-wrap gap-2">
          {shop.methods.map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={method === m}
              onClick={() => setMethod(m)}
              className={`cursor-pointer rounded-control border px-3 py-2 text-sm transition-colors ${method === m ? 'border-accent bg-accent-soft/40 font-medium text-fg' : 'border-edge text-muted hover:bg-inset/50'}`}
            >
              {t(METHOD_LABELS[m])}
            </button>
          ))}
        </div>
      </div>

      {needContact && (
        <div>
          <label className="mb-1.5 block text-xs text-muted" htmlFor="buy-contact">
            {t('Telegram username or phone number')}
          </label>
          <Input id="buy-contact" dir="ltr" value={contact} onChange={(e) => setContact(e.target.value)} placeholder="@username" maxLength={40} autoComplete="off" />
          <p className="mt-1 text-xs text-muted">{t('So the seller can reach you if there is a problem with the payment.')}</p>
        </div>
      )}

      {error && <p className="text-sm text-danger-fg">{error}</p>}
      <Button className="w-full" disabled={create.isPending || !planId || (needContact && !contact.trim())} onClick={() => (setError(null), create.mutate())}>
        {create.isPending ? t('Please wait…') : t('Continue to payment')}
      </Button>
    </div>
  )
}

function OrderPanel({ base, order, onBuyAgain, onChanged }: { base: string; order: PublicOrder; onBuyAgain: () => void; onChanged: () => void }) {
  const { t } = useI18n()
  const toman = useToman()
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const send = useMutation({
    mutationFn: ({ tracking, file }: { tracking: string; file: File }) =>
      uploadReceipt<PublicOrder>(`${base}/orders/${encodeURIComponent(order.ref)}/receipt`, tracking, file, false),
    onSuccess: () => {
      setError(null)
      onChanged()
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Failed to send the receipt')),
  })

  const summary = (
    <p className="text-sm text-muted">
      {order.plan_name} · <span className="font-medium tabular-nums text-fg">{toman(order.amount_toman)}</span>
    </p>
  )

  if (order.status === 'fulfilled') {
    const link = order.subscription_path ? `${window.location.origin}${order.subscription_path}` : null
    return (
      <div className="space-y-3">
        <div className="flex items-start gap-2.5 rounded-lg bg-success-soft p-3 text-sm text-success-fg">
          <Check className="mt-0.5 h-4 w-4 shrink-0" />
          {order.kind === 'account_purchase' ? t('Payment confirmed. Your account is ready.') : t('Payment confirmed. Your subscription was renewed.')}
        </div>
        {link && (
          <div className="space-y-2">
            <a href={order.subscription_path} className="inline-flex h-10 w-full cursor-pointer items-center justify-center rounded-control bg-accent px-4 text-sm font-medium text-on-accent outline-none transition-colors hover:bg-accent-hover focus-visible:ring-[3px] focus-visible:ring-ring">
              {t('Open my subscription')}
            </a>
            <button
              type="button"
              className="flex w-full cursor-pointer items-center justify-center gap-1.5 text-xs text-muted hover:text-fg"
              onClick={() => navigator.clipboard.writeText(link).then(() => (setCopied(true), setTimeout(() => setCopied(false), 1500)), () => undefined)}
            >
              {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              {copied ? t('Link copied') : t('Copy the link and keep it somewhere safe')}
            </button>
          </div>
        )}
        <Button variant="ghost" size="sm" onClick={onBuyAgain}>
          {t('Buy another')}
        </Button>
      </div>
    )
  }

  if (order.status === 'paid') {
    return (
      <div className="space-y-2">
        {summary}
        <p className="flex items-center gap-2 text-sm text-muted">
          <Clock className="h-4 w-4" />
          {t('Payment received. Activating, this takes a few seconds...')}
        </p>
      </div>
    )
  }

  if (order.status === 'awaiting_review') {
    return (
      <div className="space-y-2">
        {summary}
        <p className="flex items-start gap-2 rounded-lg bg-warning-soft p-3 text-sm text-warning-fg">
          <Clock className="mt-0.5 h-4 w-4 shrink-0" />
          {t('We received your payment and it is being checked. This page updates by itself; you can also come back later.')}
        </p>
      </div>
    )
  }

  if (order.status === 'pending') {
    return (
      <div className="space-y-4">
        {summary}
        {order.method === 'card_to_card' && order.card ? (
          <>
            <CardInstructions card={order.card} amount={order.amount_toman} />
            <ReceiptForm pending={send.isPending} error={error} onSubmit={(tracking, file) => send.mutate({ tracking, file })} />
          </>
        ) : order.redirect_url ? (
          <>
            <p className="text-sm text-muted">{t('Complete the payment on the gateway page. When you come back, this page confirms it.')}</p>
            <a href={order.redirect_url} className="inline-flex h-10 w-full cursor-pointer items-center justify-center rounded-control bg-accent px-4 text-sm font-medium text-on-accent outline-none transition-colors hover:bg-accent-hover focus-visible:ring-[3px] focus-visible:ring-ring">
              {t('Go to the payment page')}
            </a>
          </>
        ) : null}
        <Button variant="ghost" size="sm" onClick={onBuyAgain}>
          {t('Start over')}
        </Button>
      </div>
    )
  }

  // rejected | failed | expired | cancelled
  return (
    <div className="space-y-3">
      {summary}
      <p className="rounded-lg bg-danger-soft p-3 text-sm text-danger-fg">
        {order.status === 'rejected'
          ? t('Your payment was not accepted.') + (order.review_note ? ` ${order.review_note}` : '')
          : order.status === 'expired'
            ? t('This order expired before it was paid.')
            : t('This payment did not go through.')}
      </p>
      <Button onClick={onBuyAgain}>{t('Try again')}</Button>
    </div>
  )
}
