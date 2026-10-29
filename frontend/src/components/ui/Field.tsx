import type { ReactNode } from 'react'
import { useI18n } from '../../lib/i18n'

/** Standard form field: label above, control, optional hint below. Keeps
 * every dialog/form in the panel visually identical without repeating the
 * label/hint markup everywhere. */
export function Field({
  label,
  labelSuffix,
  hint,
  htmlFor,
  children,
}: {
  label: string
  /** Lighter inline addition after the label text, e.g. "(optional)". */
  labelSuffix?: ReactNode
  hint?: ReactNode
  htmlFor?: string
  children: ReactNode
}) {
  const { t } = useI18n()
  return (
    <div>
      <label htmlFor={htmlFor} className="mb-1.5 block text-sm font-medium text-fg">
        {t(label)}
        {labelSuffix && (
          <span className="ms-1 font-normal text-faint">{typeof labelSuffix === 'string' ? t(labelSuffix) : labelSuffix}</span>
        )}
      </label>
      {children}
      {hint && <p className="mt-1.5 text-xs leading-relaxed text-muted">{typeof hint === 'string' ? t(hint) : hint}</p>}
    </div>
  )
}
