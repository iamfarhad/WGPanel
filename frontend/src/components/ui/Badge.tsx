import type { HTMLAttributes } from 'react'

type Tone = 'green' | 'red' | 'amber' | 'slate' | 'blue'

const tones: Record<Tone, string> = {
  green: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
  red: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
  amber: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  slate: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300',
  blue: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
}

export function Badge({
  tone = 'slate',
  className = '',
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: Tone }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${tones[tone]} ${className}`}
      {...props}
    />
  )
}

/** Maps the various status strings this app uses (account/node/api-key) to a tone. */
export function statusTone(status: string): Tone {
  switch (status) {
    case 'active':
    case 'online':
    case 'registered':
      return 'green'
    case 'suspended':
    case 'offline':
    case 'revoked':
      return 'red'
    case 'pending':
      return 'amber'
    default:
      return 'slate'
  }
}
