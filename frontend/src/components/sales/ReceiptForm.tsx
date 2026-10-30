import { useState, type FormEvent } from 'react'
import { Check, Copy } from 'lucide-react'
import { useI18n } from '../../lib/i18n'
import { MAX_RECEIPT_BYTES } from '../../lib/sales'
import { useToman } from '../../lib/salesHooks'
import { Button } from '../ui/Button'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'

export interface CardInfo {
  number: string
  holder: string
  bank: string
}

function groupCard(n: string): string {
  return n.replace(/(\d{4})(?=\d)/g, '$1 ')
}

/** Where to transfer: amount, card number (copyable), holder and bank. */
export function CardInstructions({ card, amount }: { card: CardInfo; amount: number }) {
  const { t, lang } = useI18n()
  const toman = useToman()
  const [copied, setCopied] = useState<'card' | 'amount' | null>(null)
  function copy(text: string, what: 'card' | 'amount') {
    navigator.clipboard.writeText(text).then(
      () => {
        setCopied(what)
        setTimeout(() => setCopied(null), 1500)
      },
      () => undefined,
    )
  }
  return (
    <div className="space-y-3 rounded-lg border border-edge bg-inset/60 p-4 text-sm">
      <p className="text-muted">{t('Transfer exactly this amount to the card below, then send the tracking code and a photo of the receipt.')}</p>
      <div className="flex items-center justify-between gap-3">
        <span className="text-muted">{t('Amount')}</span>
        <span className="inline-flex items-center gap-2 font-semibold tabular-nums text-fg">
          {toman(amount)}
          <button type="button" className="cursor-pointer text-faint hover:text-fg" aria-label={t('Copy amount')} onClick={() => copy(String(amount), 'amount')}>
            {copied === 'amount' ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </button>
        </span>
      </div>
      <div className="flex items-center justify-between gap-3">
        <span className="text-muted">{t('Card number')}</span>
        <span className="inline-flex items-center gap-2">
          <span className="ltr font-mono text-base font-semibold tracking-wider text-fg" lang={lang === 'fa' ? 'en' : undefined}>
            {groupCard(card.number)}
          </span>
          <button type="button" className="cursor-pointer text-faint hover:text-fg" aria-label={t('Copy card number')} onClick={() => copy(card.number, 'card')}>
            {copied === 'card' ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </button>
        </span>
      </div>
      {card.holder && (
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted">{t('Card holder')}</span>
          <span className="text-fg">{card.holder}</span>
        </div>
      )}
      {card.bank && (
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted">{t('Bank')}</span>
          <span className="text-fg">{card.bank}</span>
        </div>
      )}
    </div>
  )
}

/** Tracking code + receipt image, validated client-side the same way the server does
 * (size and image type are re-checked there from the actual bytes). */
export function ReceiptForm({ onSubmit, pending, error }: { onSubmit: (tracking: string, file: File) => void; pending: boolean; error: string | null }) {
  const { t } = useI18n()
  const [tracking, setTracking] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)

  function submit(e: FormEvent) {
    e.preventDefault()
    if (!/^[A-Za-z0-9-]{4,32}$/.test(tracking.trim().replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x06f0)))) {
      setLocalError(t('The tracking code must be 4 to 32 letters, digits or dashes.'))
      return
    }
    if (!file) {
      setLocalError(t('Attach the receipt image.'))
      return
    }
    if (file.size > MAX_RECEIPT_BYTES) {
      setLocalError(t('The receipt image must be at most 2 MB.'))
      return
    }
    if (!['image/jpeg', 'image/png', 'image/webp'].includes(file.type)) {
      setLocalError(t('The receipt must be a JPEG, PNG or WebP image.'))
      return
    }
    setLocalError(null)
    onSubmit(tracking.trim().replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x06f0)), file)
  }

  return (
    <form onSubmit={submit} className="space-y-3">
      <Field label="Tracking code" hint="The reference number your bank shows after the transfer.">
        <Input dir="ltr" value={tracking} onChange={(e) => setTracking(e.target.value)} maxLength={32} autoComplete="off" />
      </Field>
      <Field label="Receipt photo" hint="JPEG, PNG or WebP, up to 2 MB.">
        <input
          type="file"
          accept="image/jpeg,image/png,image/webp"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          className="block w-full cursor-pointer text-sm text-muted file:me-3 file:cursor-pointer file:rounded-control file:border file:border-edge file:bg-surface file:px-3 file:py-2 file:text-sm file:font-medium file:text-fg"
        />
      </Field>
      {(localError || error) && <p className="text-sm text-danger-fg">{localError ?? error}</p>}
      <Button type="submit" disabled={pending} className="w-full">
        {pending ? t('Sending…') : t('Send receipt')}
      </Button>
    </form>
  )
}
