import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Tag } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import { parseToman, salesErrorMessage, useTemplatePrices, type TemplatePriceRow } from '../../lib/sales'
import { useToman } from '../../lib/salesHooks'
import { Badge } from '../ui/Badge'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'

/** The "Price" cell of the templates table: shows the Toman price and the "for sale"
 * flag, and (when the caller may change the template) opens the pricing dialog. Mounted
 * by TemplatesPage as one element; it owns its dialog and its query. */
export function TemplatePriceCell({ template, canChange }: { template: { id: string; name: string }; canChange: boolean }) {
  const { t } = useI18n()
  const toman = useToman()
  const prices = useTemplatePrices()
  const row = prices.data?.items.find((p) => p.template_id === template.id)
  const [open, setOpen] = useState(false)

  return (
    <div className="flex items-center gap-2 whitespace-nowrap">
      {row && row.price_toman > 0 ? <span className="tabular-nums text-fg">{toman(row.price_toman)}</span> : <span className="text-faint">—</span>}
      {row?.purchasable && <Badge tone="green">For sale</Badge>}
      {canChange && (
        <Button variant="ghost" size="icon" title={t('Set price')} aria-label={t('Set price')} onClick={() => setOpen(true)}>
          <Tag className="h-4 w-4" />
        </Button>
      )}
      {open && <TemplatePriceDialog template={template} row={row} onClose={() => setOpen(false)} />}
    </div>
  )
}

function TemplatePriceDialog({ template, row, onClose }: { template: { id: string; name: string }; row?: TemplatePriceRow; onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const [price, setPrice] = useState(row ? String(row.price_toman) : '')
  const [purchasable, setPurchasable] = useState(row?.purchasable ?? false)
  const [error, setError] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (body: { price_toman: number; purchasable: boolean }) =>
      apiFetch<TemplatePriceRow>(`/api/v1/templates/${template.id}/price`, { method: 'PUT', body: JSON.stringify(body) }),
    onSuccess: () => {
      push('success', t('Price saved'))
      queryClient.invalidateQueries({ queryKey: ['template-prices'] })
      onClose()
    },
    onError: (err) => setError(salesErrorMessage(err, t, 'Failed to save the price')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    const n = price.trim() === '' ? 0 : parseToman(price)
    if (Number.isNaN(n)) {
      setError(t('Enter the price as a whole number of Toman.'))
      return
    }
    if (purchasable && n <= 0) {
      setError(t('A plan for sale needs a price above zero.'))
      return
    }
    setError(null)
    save.mutate({ price_toman: n, purchasable })
  }

  return (
    <Dialog open onClose={onClose} title={t('Price of "{name}"', { name: template.name })}>
      <form onSubmit={submit} className="space-y-4">
        <Field
          label="Price (Toman)"
          hint={
            user?.role === 'reseller'
              ? 'What your customers pay for this plan on their subscription page.'
              : 'Retail price. Also what a reseller with a wallet is charged for this plan (before its discount).'
          }
        >
          <Input inputMode="numeric" dir="ltr" value={price} onChange={(e) => setPrice(e.target.value)} placeholder="150000" />
        </Field>
        <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
          <input type="checkbox" className="mt-0.5" checked={purchasable} onChange={(e) => setPurchasable(e.target.checked)} />
          <span>
            <span className="font-medium">{t('Customers can buy this plan')}</span>
            <span className="mt-0.5 block text-xs leading-relaxed text-muted">
              {user?.role === 'reseller'
                ? t('Shown on the subscription page of your customers (needs self-service switched on by the admin).')
                : t('Shown as a renewal option on subscription pages and on the public purchase page when it is enabled.')}
            </span>
          </span>
        </label>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? t('Saving…') : t('Save')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
