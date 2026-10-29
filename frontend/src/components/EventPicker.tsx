import { EVENT_CATALOG } from '../lib/notificationEvents'
import { useI18n } from '../lib/i18n'

/** Checkbox list of subscribable events. For webhooks an empty selection means "every
 * event" (allowEmptyMeansAll); for notification channels it means none. */
export function EventPicker({
  value,
  onChange,
  only,
}: {
  value: string[]
  onChange: (next: string[]) => void
  /** Restrict the list to event types with these prefixes (reseller webhooks). */
  only?: string[]
}) {
  const { t } = useI18n()
  const events = EVENT_CATALOG.filter((e) => !only || only.some((p) => e.type.startsWith(p)))
  const toggle = (type: string) =>
    onChange(value.includes(type) ? value.filter((v) => v !== type) : [...value, type])
  return (
    <div className="grid gap-1.5 sm:grid-cols-2">
      {events.map((e) => (
        <label
          key={e.type}
          className="flex cursor-pointer items-center gap-2 rounded-control border border-edge px-3 py-2 text-sm text-fg transition-colors hover:bg-inset/60 has-[:checked]:border-accent/50 has-[:checked]:bg-accent-soft/50"
        >
          <input
            type="checkbox"
            className="h-4 w-4 accent-accent"
            checked={value.includes(e.type)}
            onChange={() => toggle(e.type)}
          />
          <span className="min-w-0">
            <span className="block truncate">{t(e.label)}</span>
            <span className="ltr block truncate font-mono text-[11px] text-faint">{e.type}</span>
          </span>
        </label>
      ))}
    </div>
  )
}
