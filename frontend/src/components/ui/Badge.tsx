import type { HTMLAttributes } from 'react'
import { useI18n } from '../../lib/i18n'

type Tone = 'green' | 'red' | 'amber' | 'slate' | 'blue'

const tones: Record<Tone, string> = {
  green: 'bg-success-soft text-success-fg ring-success/25',
  red: 'bg-danger-soft text-danger-fg ring-danger/25',
  amber: 'bg-warning-soft text-warning-fg ring-warning/30',
  slate: 'bg-inset text-muted ring-edge',
  blue: 'bg-info-soft text-info-fg ring-info/25',
}

const dots: Record<Tone, string> = {
  green: 'bg-success',
  red: 'bg-danger',
  amber: 'bg-warning',
  slate: 'bg-faint',
  blue: 'bg-info',
}

export function Badge({
  tone = 'slate',
  dot = false,
  children,
  className = '',
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: Tone; dot?: boolean }) {
  const { t } = useI18n()
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${tones[tone]} ${className}`}
      {...props}
    >
      {dot && <span className={`h-1.5 w-1.5 rounded-full ${dots[tone]}`} />}
      {typeof children === 'string' ? t(children) : children}
    </span>
  )
}

/** Maps the status strings this app uses (account/node/api-key) to a tone: online
 * green, offline gray, needs-attention amber, terminal red. */
export function statusTone(status: string): Tone {
  switch (status) {
    case 'active':
    case 'online':
    case 'registered':
      return 'green'
    case 'pending':
    case 'suspended':
      return 'amber'
    case 'revoked':
    case 'deleted':
      return 'red'
    case 'offline':
    default:
      return 'slate'
  }
}
