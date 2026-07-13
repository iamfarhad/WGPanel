import type { ReactNode } from 'react'
import { X } from 'lucide-react'
import { Card } from './Card'

interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  children: ReactNode
  /** Tailwind max-width class - defaults to max-w-md. Widen it (e.g. max-w-2xl) for
   * content-heavy dialogs like the account/node detail views (peer list + chart +
   * QR/config side-by-side) that get uncomfortably cramped at the default width. */
  maxWidthClassName?: string
}

export function Dialog({ open, onClose, title, children, maxWidthClassName = 'max-w-md' }: DialogProps) {
  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center overflow-y-auto bg-black/40 p-4"
      onClick={onClose}
    >
      {/* flex-col + max-h caps the dialog to the viewport and keeps the header
          (title/close button) always reachable - previously an unbounded Card grew
          taller than the viewport with nothing scrollable, pushing the header and
          the content's own bottom action buttons off-screen with no way to reach
          either (see the account detail dialog with 5 node peers + chart + QR). */}
      <Card
        className={`flex max-h-[85vh] w-full flex-col ${maxWidthClassName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-slate-100 p-6 pb-4 dark:border-slate-800">
          <h2 className="text-lg font-semibold text-slate-900 dark:text-slate-100">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-300"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-6 pt-4">{children}</div>
      </Card>
    </div>
  )
}
