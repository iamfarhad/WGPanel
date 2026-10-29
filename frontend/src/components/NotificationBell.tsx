import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router-dom'
import { AlertTriangle, Bell, CheckCheck, CircleAlert, Info } from 'lucide-react'
import { useI18n } from '../lib/i18n'
import { notificationText } from '../lib/notificationEvents'
import { useNotificationMutations, useNotifications, type AppNotification } from '../lib/notifications'

export function SeverityIcon({ severity, className = 'h-4 w-4' }: { severity: AppNotification['severity']; className?: string }) {
  if (severity === 'critical') return <CircleAlert className={`${className} text-danger`} />
  if (severity === 'warning') return <AlertTriangle className={`${className} text-warning`} />
  return <Info className={`${className} text-info`} />
}

/** Bell with the unread count; opens a short list of the newest notifications. The
 * panel is portalled and fixed-positioned so the sidebar's overflow cannot clip it. */
export function NotificationBell() {
  const { t, formatDate } = useI18n()
  const { data } = useNotifications()
  const { markRead, markAllRead } = useNotificationMutations()
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<React.CSSProperties | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  const unread = data?.unread_count ?? 0
  const items = (data?.items ?? []).slice(0, 8)

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return
    const r = triggerRef.current.getBoundingClientRect()
    const rtl = getComputedStyle(triggerRef.current).direction === 'rtl'
    if (window.innerWidth < 640) {
      setPos({ top: r.bottom + 8, left: 12, right: 12 })
    } else if (r.left < window.innerWidth / 2) {
      // Bell lives in the sidebar footer: open beside it, bottom-aligned.
      const base: React.CSSProperties = { bottom: Math.max(8, window.innerHeight - r.bottom), width: 380 }
      setPos(rtl ? { ...base, right: window.innerWidth - r.left + 8 } : { ...base, left: r.right + 8 })
    } else {
      setPos({ top: r.bottom + 8, right: Math.max(8, window.innerWidth - r.right), width: 380 })
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      const target = e.target as Node
      if (!panelRef.current?.contains(target) && !triggerRef.current?.contains(target)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((o) => !o)}
        title={t('Notifications')}
        aria-label={unread > 0 ? t('Notifications, {count} unread', { count: unread }) : t('Notifications')}
        aria-expanded={open}
        className="relative flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-lg text-faint transition-colors duration-150 hover:bg-inset hover:text-fg"
      >
        <Bell className="h-4 w-4" />
        {unread > 0 && (
          <span className="absolute -end-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[10px] leading-none font-semibold text-white">
            {unread > 99 ? '99+' : unread}
          </span>
        )}
      </button>
      {open &&
        pos &&
        createPortal(
          <div
            ref={panelRef}
            role="dialog"
            aria-label={t('Notifications')}
            style={{ position: 'fixed', ...pos }}
            className="animate-dialog-in z-50 overflow-hidden rounded-card border border-edge bg-surface shadow-pop"
          >
            <div className="flex items-center justify-between border-b border-edge px-4 py-3">
              <h2 className="text-sm font-semibold text-fg">{t('Notifications')}</h2>
              <button
                type="button"
                disabled={unread === 0 || markAllRead.isPending}
                onClick={() => markAllRead.mutate()}
                className="inline-flex cursor-pointer items-center gap-1 text-xs font-medium text-accent-fg hover:underline disabled:cursor-default disabled:text-faint disabled:no-underline"
              >
                <CheckCheck className="h-3.5 w-3.5" />
                {t('Mark all as read')}
              </button>
            </div>
            <div className="max-h-96 overflow-y-auto">
              {items.length === 0 ? (
                <p className="px-4 py-8 text-center text-sm text-muted">{t('You are all caught up.')}</p>
              ) : (
                items.map((n) => {
                  const text = notificationText(n, t, (v) => formatDate(v))
                  return (
                    <button
                      key={n.id}
                      type="button"
                      onClick={() => !n.read && markRead.mutate(n.id)}
                      className={`flex w-full cursor-pointer items-start gap-3 border-b border-edge/70 px-4 py-3 text-start transition-colors last:border-0 hover:bg-inset/60 ${
                        n.read ? '' : 'bg-accent-soft/40'
                      }`}
                    >
                      <span className="mt-0.5 shrink-0">
                        <SeverityIcon severity={n.severity} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block text-sm font-medium text-fg">{text.title}</span>
                        <span className="mt-0.5 block text-xs leading-relaxed break-words text-muted">{text.body}</span>
                        <span className="mt-1 block text-[11px] text-faint">{formatDate(n.created_at)}</span>
                      </span>
                      {!n.read && <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-accent" aria-hidden />}
                    </button>
                  )
                })
              )}
            </div>
            <Link
              to="/notifications"
              onClick={() => setOpen(false)}
              className="block border-t border-edge px-4 py-2.5 text-center text-xs font-medium text-accent-fg hover:bg-inset/60"
            >
              {t('View all notifications')}
            </Link>
          </div>,
          document.body,
        )}
    </>
  )
}
