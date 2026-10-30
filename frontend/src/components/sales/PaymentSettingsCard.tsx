import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Plus, Trash2 } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import { parseToman, salesErrorMessage } from '../../lib/sales'
import { Button } from '../ui/Button'
import { Card, CardHeader } from '../ui/Card'
import { ErrorState } from '../ui/ErrorState'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Skeleton } from '../ui/Skeleton'

interface CardBody {
  number: string
  holder: string
  bank: string
  enabled: boolean
}

interface PaymentSettings {
  card: { enabled: boolean; cards: CardBody[] }
  zarinpal: { enabled: boolean; sandbox: boolean; has_merchant_id: boolean }
  nowpayments: { enabled: boolean; sandbox: boolean; has_api_key: boolean; has_ipn_secret: boolean; toman_per_usd: number }
  callback_base_url: string
  public_buy_enabled: boolean
  effective_callback_base: string
  zarinpal_callback_url: string
  nowpayments_ipn_url: string
}

function Toggle({ checked, onChange, title, description }: { checked: boolean; onChange: (v: boolean) => void; title: string; description?: string }) {
  const { t } = useI18n()
  return (
    <label className="flex cursor-pointer items-start gap-2.5 text-sm text-fg">
      <input type="checkbox" className="mt-0.5" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>
        <span className="font-medium">{t(title)}</span>
        {description && <span className="mt-0.5 block text-xs leading-relaxed text-muted">{t(description)}</span>}
      </span>
    </label>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const { t } = useI18n()
  return (
    <section className="space-y-4 border-t border-edge px-5 py-5 first:border-0">
      <h3 className="text-sm font-semibold text-fg">{t(title)}</h3>
      {children}
    </section>
  )
}

/** Super-admin payment configuration: the three methods, the card list, gateway
 * credentials (write-only - the server only reports whether they are set) and the public
 * purchase switch. */
export function PaymentSettingsCard() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['payment-settings'], queryFn: () => apiFetch<PaymentSettings>('/api/v1/payments/settings') })
  const data = query.data

  const [cardEnabled, setCardEnabled] = useState(false)
  const [cards, setCards] = useState<CardBody[]>([])
  const [zpEnabled, setZpEnabled] = useState(false)
  const [zpSandbox, setZpSandbox] = useState(false)
  const [zpMerchant, setZpMerchant] = useState('')
  const [npEnabled, setNpEnabled] = useState(false)
  const [npSandbox, setNpSandbox] = useState(false)
  const [npKey, setNpKey] = useState('')
  const [npSecret, setNpSecret] = useState('')
  const [npRate, setNpRate] = useState('')
  const [callbackBase, setCallbackBase] = useState('')
  const [publicBuy, setPublicBuy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!data) return
    setCardEnabled(data.card.enabled)
    setCards(data.card.cards)
    setZpEnabled(data.zarinpal.enabled)
    setZpSandbox(data.zarinpal.sandbox)
    setZpMerchant('')
    setNpEnabled(data.nowpayments.enabled)
    setNpSandbox(data.nowpayments.sandbox)
    setNpKey('')
    setNpSecret('')
    setNpRate(data.nowpayments.toman_per_usd ? String(data.nowpayments.toman_per_usd) : '')
    setCallbackBase(data.callback_base_url)
    setPublicBuy(data.public_buy_enabled)
  }, [data])

  const save = useMutation({
    mutationFn: (rate: number) =>
      apiFetch<PaymentSettings>('/api/v1/payments/settings', {
        method: 'PUT',
        body: JSON.stringify({
          card: { enabled: cardEnabled, cards },
          zarinpal: { enabled: zpEnabled, sandbox: zpSandbox, merchant_id: zpMerchant.trim() },
          nowpayments: { enabled: npEnabled, sandbox: npSandbox, api_key: npKey.trim(), ipn_secret: npSecret.trim(), toman_per_usd: rate },
          callback_base_url: callbackBase.trim(),
          public_buy_enabled: publicBuy,
        }),
      }),
    onSuccess: (saved) => {
      push('success', t('Payment settings saved'))
      queryClient.setQueryData(['payment-settings'], saved)
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Failed to save payment settings')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    const rate = npRate.trim() === '' ? 0 : parseToman(npRate)
    if (Number.isNaN(rate)) {
      setError(t('Enter the dollar rate as a whole number of Toman.'))
      return
    }
    setError(null)
    save.mutate(rate)
  }

  function copy(text: string) {
    navigator.clipboard.writeText(text).then(
      () => push('success', t('Copied')),
      () => undefined,
    )
  }

  const updateCard = (i: number, patch: Partial<CardBody>) => setCards((cs) => cs.map((c, j) => (j === i ? { ...c, ...patch } : c)))

  return (
    <Card>
      <CardHeader title="Payment methods" description="How buyers and resellers pay. Secrets are stored encrypted and never shown again." />
      {query.isLoading && <Skeleton className="m-5 h-40" />}
      {query.isError && <ErrorState title="Could not load payment settings." onRetry={() => query.refetch()} />}
      {data && (
        <form onSubmit={submit}>
          <Section title="Card to card">
            <Toggle
              checked={cardEnabled}
              onChange={setCardEnabled}
              title="Accept card-to-card payments"
              description="The buyer transfers to one of your cards, then sends the tracking code and a receipt photo. You approve or reject it on the Payments page."
            />
            <div className="space-y-3">
              {cards.map((c, i) => (
                <div key={i} className="grid grid-cols-1 gap-3 rounded-lg border border-edge p-3 sm:grid-cols-[1.4fr_1fr_1fr_auto]">
                  <Input dir="ltr" inputMode="numeric" value={c.number} onChange={(e) => updateCard(i, { number: e.target.value })} placeholder="6037 9970 1234 5678" aria-label={t('Card number')} />
                  <Input value={c.holder} onChange={(e) => updateCard(i, { holder: e.target.value })} placeholder={t('Card holder')} aria-label={t('Card holder')} maxLength={80} />
                  <Input value={c.bank} onChange={(e) => updateCard(i, { bank: e.target.value })} placeholder={t('Bank')} aria-label={t('Bank')} maxLength={40} />
                  <div className="flex items-center gap-2">
                    <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted">
                      <input type="checkbox" checked={c.enabled} onChange={(e) => updateCard(i, { enabled: e.target.checked })} />
                      {t('Enabled')}
                    </label>
                    <Button type="button" variant="ghost" size="icon" aria-label={t('Remove card')} onClick={() => setCards((cs) => cs.filter((_, j) => j !== i))}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              ))}
              {cards.length < 10 && (
                <Button type="button" variant="secondary" size="sm" onClick={() => setCards((cs) => [...cs, { number: '', holder: '', bank: '', enabled: true }])}>
                  <Plus className="h-4 w-4" />
                  Add card
                </Button>
              )}
            </div>
          </Section>

          <Section title="ZarinPal">
            <Toggle checked={zpEnabled} onChange={setZpEnabled} title="Accept ZarinPal payments" description="Online payment with the Iranian bank gateway. Amounts are verified by the panel with ZarinPal before anything is delivered." />
            <Field label="Merchant ID" hint={data.zarinpal.has_merchant_id ? 'A merchant ID is saved. Leave empty to keep it.' : 'The 36-character merchant ID from your ZarinPal dashboard.'}>
              <Input dir="ltr" value={zpMerchant} onChange={(e) => setZpMerchant(e.target.value)} placeholder={data.zarinpal.has_merchant_id ? '••••••••-••••-••••-••••-••••••••••••' : 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'} autoComplete="off" />
            </Field>
            <Toggle checked={zpSandbox} onChange={setZpSandbox} title="Sandbox (testing) mode" />
          </Section>

          <Section title="Crypto (NowPayments)">
            <Toggle checked={npEnabled} onChange={setNpEnabled} title="Accept crypto payments" description="Invoices are created at NowPayments in US dollars; a payment is accepted when NowPayments reports it finished." />
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label="API key" hint={data.nowpayments.has_api_key ? 'Saved. Leave empty to keep it.' : undefined}>
                <Input dir="ltr" type="password" value={npKey} onChange={(e) => setNpKey(e.target.value)} placeholder={data.nowpayments.has_api_key ? '••••••••' : ''} autoComplete="off" />
              </Field>
              <Field label="IPN secret" hint={data.nowpayments.has_ipn_secret ? 'Saved. Leave empty to keep it.' : undefined}>
                <Input dir="ltr" type="password" value={npSecret} onChange={(e) => setNpSecret(e.target.value)} placeholder={data.nowpayments.has_ipn_secret ? '••••••••' : ''} autoComplete="off" />
              </Field>
            </div>
            <Field label="Toman per US dollar" hint="Prices are converted to dollars at this rate when an invoice is created.">
              <Input dir="ltr" inputMode="numeric" value={npRate} onChange={(e) => setNpRate(e.target.value)} placeholder="600000" />
            </Field>
            <Toggle checked={npSandbox} onChange={setNpSandbox} title="Sandbox (testing) mode" />
            {data.nowpayments_ipn_url && (
              <div className="rounded-lg bg-inset p-3 text-xs text-muted">
                <p className="mb-1">{t('Set this as the IPN callback URL in your NowPayments dashboard:')}</p>
                <div className="flex items-center gap-2">
                  <code className="ltr min-w-0 flex-1 break-all font-mono text-fg">{data.nowpayments_ipn_url}</code>
                  <Button type="button" variant="ghost" size="icon" aria-label={t('Copy')} onClick={() => copy(data.nowpayments_ipn_url)}>
                    <Copy className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            )}
          </Section>

          <Section title="Public purchase and callbacks">
            <Toggle checked={publicBuy} onChange={setPublicBuy} title="Open the public purchase page (/buy)" description="Anyone can buy a new account there. Off by default." />
            <Field
              label="Callback address"
              hint={
                data.effective_callback_base
                  ? t('Gateways return buyers to this address. Empty means {base}.', { base: data.effective_callback_base })
                  : 'Set a panel or subscription domain first, or enter an https address here - gateways need a public address.'
              }
            >
              <Input dir="ltr" value={callbackBase} onChange={(e) => setCallbackBase(e.target.value)} placeholder="https://pay.example.com" />
            </Field>
          </Section>

          <div className="flex items-center justify-end gap-3 border-t border-edge px-5 py-4">
            {error && <p className="me-auto text-sm text-danger-fg">{error}</p>}
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? t('Saving…') : t('Save payment settings')}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
