import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Wallet } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import { useTemplates } from '../../lib/lifecycle'
import { LEDGER_LABELS, parseToman, salesErrorMessage, useTemplatePrices, type LedgerEntry } from '../../lib/sales'
import { useToman } from '../../lib/salesHooks'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Skeleton } from '../ui/Skeleton'

interface ResellerSales {
  discount_percent: number
  charge_wallet: boolean
  self_service: boolean
  overrides: { template_id: string; price_toman: number }[]
  balance_toman: number
}

interface Ledger {
  balance_toman: number
  items: LedgerEntry[]
}

/** One-line mount for the resellers table: a wallet button that opens the super admin's
 * view of that reseller's pricing, wallet balance, manual adjustment and ledger. */
export function ResellerSalesButton({ reseller }: { reseller: { id: string; username: string } }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button variant="ghost" size="icon" title={t('Pricing and wallet')} aria-label={t('Pricing and wallet')} onClick={() => setOpen(true)}>
        <Wallet className="h-4 w-4" />
      </Button>
      {open && <ResellerSalesDialog reseller={reseller} onClose={() => setOpen(false)} />}
    </>
  )
}

function ResellerSalesDialog({ reseller, onClose }: { reseller: { id: string; username: string }; onClose: () => void }) {
  const { t, formatDate } = useI18n()
  const toman = useToman()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const base = `/api/v1/resellers/${reseller.id}`

  const sales = useQuery({ queryKey: ['reseller-sales', reseller.id], queryFn: () => apiFetch<ResellerSales>(`${base}/sales`) })
  const ledger = useQuery({ queryKey: ['reseller-ledger', reseller.id], queryFn: () => apiFetch<Ledger>(`${base}/wallet?limit=20`) })
  const templates = useTemplates()
  const prices = useTemplatePrices()
  // Only global templates shared with resellers can be charged to a wallet.
  const shared = (templates.data?.templates ?? []).filter((tm) => tm.owner == null && tm.available_to_resellers)
  const baseOf = (id: string) => prices.data?.items.find((p) => p.template_id === id)?.price_toman ?? 0

  const [discount, setDiscount] = useState('0')
  const [charge, setCharge] = useState(false)
  const [selfService, setSelfService] = useState(false)
  const [overrides, setOverrides] = useState<Record<string, string>>({})
  const [pricingError, setPricingError] = useState<string | null>(null)

  useEffect(() => {
    if (!sales.data) return
    setDiscount(String(sales.data.discount_percent))
    setCharge(sales.data.charge_wallet)
    setSelfService(sales.data.self_service)
    setOverrides(Object.fromEntries(sales.data.overrides.map((o) => [o.template_id, String(o.price_toman)])))
  }, [sales.data])

  const savePricing = useMutation({
    mutationFn: (body: object) => apiFetch<ResellerSales>(`${base}/sales`, { method: 'PUT', body: JSON.stringify(body) }),
    onSuccess: (saved) => {
      push('success', t('Pricing saved'))
      queryClient.setQueryData(['reseller-sales', reseller.id], saved)
      setPricingError(null)
    },
    onError: (err) => setPricingError(salesErrorMessage(err, t, 'Failed to save pricing')),
  })

  function submitPricing(e: FormEvent) {
    e.preventDefault()
    const d = Number(discount)
    if (!Number.isInteger(d) || d < 0 || d > 100) {
      setPricingError(t('The discount must be a whole number from 0 to 100.'))
      return
    }
    const list: { template_id: string; price_toman: number }[] = []
    for (const [id, raw] of Object.entries(overrides)) {
      if (raw.trim() === '') continue
      const n = parseToman(raw)
      if (Number.isNaN(n)) {
        setPricingError(t('Enter prices as whole numbers of Toman.'))
        return
      }
      list.push({ template_id: id, price_toman: n })
    }
    setPricingError(null)
    savePricing.mutate({ discount_percent: d, charge_wallet: charge, self_service: selfService, overrides: list })
  }

  const [amount, setAmount] = useState('')
  const [direction, setDirection] = useState<'credit' | 'debit'>('credit')
  const [reason, setReason] = useState('')
  const [adjustError, setAdjustError] = useState<string | null>(null)
  const adjust = useMutation({
    mutationFn: (body: { amount_toman: number; reason: string }) =>
      apiFetch<{ balance_toman: number }>(`${base}/wallet/adjust`, { method: 'POST', body: JSON.stringify(body) }),
    onSuccess: () => {
      push('success', t('Wallet updated'))
      setAmount('')
      setReason('')
      setAdjustError(null)
      queryClient.invalidateQueries({ queryKey: ['reseller-ledger', reseller.id] })
      queryClient.invalidateQueries({ queryKey: ['reseller-sales', reseller.id] })
    },
    onError: (err) => setAdjustError(salesErrorMessage(err, t, 'Failed to update the wallet')),
  })

  function submitAdjust(e: FormEvent) {
    e.preventDefault()
    const n = parseToman(amount)
    if (Number.isNaN(n) || n <= 0) {
      setAdjustError(t('Enter a positive whole number of Toman.'))
      return
    }
    if (!reason.trim()) {
      setAdjustError(t('A reason is required.'))
      return
    }
    setAdjustError(null)
    adjust.mutate({ amount_toman: direction === 'credit' ? n : -n, reason: reason.trim() })
  }

  const balance = ledger.data?.balance_toman ?? sales.data?.balance_toman

  return (
    <Dialog open onClose={onClose} title={t('Pricing and wallet: {name}', { name: reseller.username })} maxWidthClassName="max-w-2xl">
      <div className="space-y-6">
        <div className="flex items-baseline justify-between rounded-lg bg-inset px-4 py-3">
          <span className="text-sm text-muted">{t('Wallet balance')}</span>
          <span className="text-lg font-semibold tabular-nums text-fg">{balance == null ? '…' : toman(balance)}</span>
        </div>

        <form onSubmit={submitAdjust} className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">{t('Manual credit or debit')}</h3>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[8rem_1fr]">
            <Select value={direction} onChange={(e) => setDirection(e.target.value as 'credit' | 'debit')} aria-label={t('Direction')}>
              <option value="credit">{t('Credit')}</option>
              <option value="debit">{t('Debit')}</option>
            </Select>
            <Input dir="ltr" inputMode="numeric" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder={t('Amount in Toman')} aria-label={t('Amount in Toman')} />
          </div>
          <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} placeholder={t('Reason (kept in the ledger and the audit log)')} aria-label={t('Reason')} />
          {adjustError && <p className="text-sm text-danger-fg">{adjustError}</p>}
          <div className="flex justify-end">
            <Button type="submit" disabled={adjust.isPending}>
              {adjust.isPending ? t('Saving…') : t('Apply')}
            </Button>
          </div>
        </form>

        <form onSubmit={submitPricing} className="space-y-4 border-t border-edge pt-5">
          <h3 className="text-sm font-semibold text-fg">{t('Pricing')}</h3>
          {sales.isLoading && <Skeleton className="h-24" />}
          {sales.data && (
            <>
              <Field label="Discount on priced plans (%)" hint="Taken off the price of every priced shared template when this reseller is charged.">
                <Input dir="ltr" inputMode="numeric" value={discount} onChange={(e) => setDiscount(e.target.value)} className="max-w-32" />
              </Field>
              <label className="flex cursor-pointer items-start gap-2.5 text-sm text-fg">
                <input type="checkbox" className="mt-0.5" checked={charge} onChange={(e) => setCharge(e.target.checked)} />
                <span>
                  <span className="font-medium">{t('Charge the wallet for priced templates')}</span>
                  <span className="mt-0.5 block text-xs leading-relaxed text-muted">
                    {t('Creating or renewing an account from a priced shared template takes its price from the wallet. With too little balance the request fails.')}
                  </span>
                </span>
              </label>
              <label className="flex cursor-pointer items-start gap-2.5 text-sm text-fg">
                <input type="checkbox" className="mt-0.5" checked={selfService} onChange={(e) => setSelfService(e.target.checked)} />
                <span>
                  <span className="font-medium">{t('Let its customers renew by themselves')}</span>
                  <span className="mt-0.5 block text-xs leading-relaxed text-muted">
                    {t('Customers see the reseller\'s own priced plans on their subscription page and pay you. The reseller\'s wallet is not charged for these.')}
                  </span>
                </span>
              </label>
              {shared.length > 0 && (
                <div>
                  <p className="mb-2 text-sm font-medium text-fg">{t('Price per template (overrides the discount)')}</p>
                  <div className="space-y-2">
                    {shared.map((tm) => (
                      <div key={tm.id} className="grid grid-cols-[1fr_9rem] items-center gap-3">
                        <span className="min-w-0 truncate text-sm text-fg">
                          {tm.name}
                          <span className="ms-2 text-xs text-faint">{baseOf(tm.id) > 0 ? toman(baseOf(tm.id)) : t('no price')}</span>
                        </span>
                        <Input
                          dir="ltr"
                          inputMode="numeric"
                          aria-label={t('Price for {name}', { name: tm.name })}
                          value={overrides[tm.id] ?? ''}
                          onChange={(e) => setOverrides((o) => ({ ...o, [tm.id]: e.target.value }))}
                          placeholder={t('default')}
                        />
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {pricingError && <p className="text-sm text-danger-fg">{pricingError}</p>}
              <div className="flex justify-end">
                <Button type="submit" disabled={savePricing.isPending}>
                  {savePricing.isPending ? t('Saving…') : t('Save pricing')}
                </Button>
              </div>
            </>
          )}
        </form>

        <div className="border-t border-edge pt-5">
          <h3 className="mb-2 text-sm font-semibold text-fg">{t('Ledger')}</h3>
          {ledger.isLoading && <Skeleton className="h-20" />}
          {ledger.data && ledger.data.items.length === 0 && <p className="text-sm text-muted">{t('No movements yet.')}</p>}
          {ledger.data && ledger.data.items.length > 0 && (
            <div className="max-h-64 overflow-y-auto">
              {ledger.data.items.map((e) => (
                <div key={e.id} className="flex items-start justify-between gap-3 border-b border-edge/70 py-2 text-sm last:border-0">
                  <div className="min-w-0">
                    <p className="text-fg">{t(LEDGER_LABELS[e.kind] ?? e.kind)}</p>
                    <p className="truncate text-xs text-faint">
                      {e.reason} · {formatDate(e.created_at)}
                    </p>
                  </div>
                  <div className="shrink-0 text-end tabular-nums">
                    <p className={e.amount_toman < 0 ? 'text-danger-fg' : 'text-success-fg'}>{toman(e.amount_toman)}</p>
                    <p className="text-xs text-faint">{toman(e.balance_after)}</p>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </Dialog>
  )
}
