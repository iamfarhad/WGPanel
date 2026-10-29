import { Dialog } from './Dialog'
import { Button } from './Button'
import { useI18n } from '../../lib/i18n'

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmLabel,
  danger = false,
  submitting = false,
}: {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  description: string
  confirmLabel?: string
  danger?: boolean
  submitting?: boolean
}) {
  const { t } = useI18n()
  return (
    <Dialog open={open} onClose={onClose} title={t(title)}>
      <p className="text-sm leading-relaxed text-muted">{t(description)}</p>
      <div className="mt-6 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose} disabled={submitting}>
          {t('Cancel')}
        </Button>
        <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={submitting}>
          {submitting ? t('Working…') : t(confirmLabel ?? 'Confirm')}
        </Button>
      </div>
    </Dialog>
  )
}
