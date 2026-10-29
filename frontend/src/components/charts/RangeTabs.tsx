/** Date-range presets as one segmented control - the first control in the report's
 * single filter row. */
export function RangeTabs<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
  label: string
}) {
  return (
    <div className="flex items-center gap-1 rounded-control bg-inset p-1" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
          className={`h-8 cursor-pointer rounded-lg px-3 text-sm font-medium transition-colors duration-150 ${
            value === o.value ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-muted hover:text-fg'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
