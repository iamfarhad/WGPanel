import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { MoreHorizontal, type LucideIcon } from 'lucide-react'

export interface RowAction {
  label: string
  icon?: LucideIcon
  onSelect: () => void
  danger?: boolean
  disabled?: boolean
}

/** Three-dot row menu for data tables. The menu renders in a portal with fixed
 * positioning: tables sit in overflow-x-auto / overflow-hidden containers that would
 * clip an absolutely positioned dropdown. It opens toward the inline end of the
 * trigger (right in LTR, left in RTL), flips upward near the viewport bottom, and
 * closes on outside click, Escape, scroll or resize. */
export function RowActions({ actions, label = 'Actions' }: { actions: RowAction[]; label?: string }) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  const close = useCallback(() => {
    setOpen(false)
    setPos(null)
  }, [])

  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) return
    const t = triggerRef.current.getBoundingClientRect()
    const m = menuRef.current.getBoundingClientRect()
    const rtl = getComputedStyle(triggerRef.current).direction === 'rtl'
    const gap = 6
    const left = rtl ? t.left : t.right - m.width
    const below = t.bottom + gap
    const top = below + m.height > window.innerHeight - 8 ? t.top - gap - m.height : below
    setPos({ top: Math.max(8, top), left: Math.min(Math.max(8, left), window.innerWidth - m.width - 8) })
    menuRef.current.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      const target = e.target as Node
      if (!menuRef.current?.contains(target) && !triggerRef.current?.contains(target)) close()
    }
    document.addEventListener('pointerdown', onPointer)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [open, close])

  function onMenuKey(e: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])
    const i = items.indexOf(document.activeElement as HTMLButtonElement)
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
      triggerRef.current?.focus()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      items[(i + 1) % items.length]?.focus()
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      items[(i - 1 + items.length) % items.length]?.focus()
    } else if (e.key === 'Tab') {
      close()
    }
  }

  if (actions.length === 0) return null

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          if (open) close()
          else setOpen(true)
        }}
        className={`inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-faint transition-colors duration-150 outline-none hover:bg-inset hover:text-fg focus-visible:ring-[3px] focus-visible:ring-ring ${
          open ? 'bg-inset text-fg' : ''
        }`}
      >
        <MoreHorizontal className="h-4 w-4" />
      </button>
      {open &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            onKeyDown={onMenuKey}
            onClick={(e) => e.stopPropagation()}
            style={{ top: pos?.top ?? -9999, left: pos?.left ?? -9999 }}
            className="animate-toast-in fixed z-50 min-w-44 rounded-card border border-edge bg-surface p-1 shadow-pop"
          >
            {actions.map((a) => (
              <button
                key={a.label}
                type="button"
                role="menuitem"
                disabled={a.disabled}
                onClick={() => {
                  close()
                  a.onSelect()
                }}
                className={`flex w-full cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-start text-sm outline-none transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-50 ${
                  a.danger
                    ? 'text-danger-fg hover:bg-danger-soft focus-visible:bg-danger-soft'
                    : 'text-fg hover:bg-inset focus-visible:bg-inset'
                }`}
              >
                {a.icon && <a.icon className={`h-4 w-4 shrink-0 ${a.danger ? '' : 'text-faint'}`} strokeWidth={1.75} />}
                {a.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  )
}
