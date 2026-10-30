import { useI18n } from '../../lib/i18n'
import { routingModes, type RoutingMode, type RoutingPrecision, type RoutingState } from '../../lib/routing'
import { Field } from '../ui/Field'
import { Select } from '../ui/Select'

const textareaClass =
  'w-full rounded-control border border-edge bg-surface px-3 py-2 font-mono text-xs text-fg transition-[border-color,box-shadow] duration-150 placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none ltr'

/** Split-tunnel section shared by the new-account, bulk-create, account-edit and template
 * forms: the routing mode, the excluded CIDRs for "custom", and the list precision. */
export function RoutingFields({
  value,
  onChange,
  defaultLabel = 'Default (full tunnel)',
}: {
  value: RoutingState
  onChange: (next: RoutingState) => void
  /** What the empty choice means in this form ("Same as template" on create forms). */
  defaultLabel?: string
}) {
  const { t } = useI18n()
  const split = value.mode === 'bypass_iran' || value.mode === 'custom'
  return (
    <div className="space-y-4">
      <Field
        label="Routing"
        hint="Iran direct sends Iranian and local addresses straight out of the user's own connection: Iranian bank and government sites keep working and that traffic does not use the quota. Everything else still goes through the tunnel (DNS too)."
      >
        <Select value={value.mode} onChange={(e) => onChange({ ...value, mode: e.target.value as RoutingMode | '' })}>
          <option value="">{t(defaultLabel)}</option>
          {routingModes.map((m) => (
            <option key={m.value} value={m.value}>
              {t(m.label)}
            </option>
          ))}
        </Select>
      </Field>
      {value.mode === 'custom' && (
        <Field
          label="Excluded IPv4 ranges"
          hint="One CIDR per line, e.g. 203.0.113.0/24. These bypass the tunnel; everything else goes through it."
        >
          <textarea
            className={textareaClass}
            rows={4}
            required
            spellCheck={false}
            value={value.cidrs}
            onChange={(e) => onChange({ ...value, cidrs: e.target.value })}
            placeholder={'203.0.113.0/24\n198.51.100.0/24'}
          />
        </Field>
      )}
      {split && (
        <Field
          label="List precision"
          hint="Compact keeps the client config near 1000 routes so apps import it quickly; a few foreign addresses may go direct and the smallest excluded ranges may use the tunnel. Exact lists every range (about 8000 for Iran direct) - slow to import on some phones and Windows."
        >
          <Select value={value.precision} onChange={(e) => onChange({ ...value, precision: e.target.value as RoutingPrecision | '' })}>
            <option value="">{t('Compact (default)')}</option>
            <option value="exact">{t('Exact')}</option>
          </Select>
        </Field>
      )}
    </div>
  )
}
