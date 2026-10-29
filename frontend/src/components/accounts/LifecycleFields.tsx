import { useI18n } from '../../lib/i18n'
import { resetStrategies, type ResetStrategy } from '../../lib/lifecycle'
import type { LifecycleState } from '../../lib/lifecycleForm'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'

/** "Start on first use" toggle and the scheduled-reset picker, shared by the new-account,
 * bulk-create and template forms. */
export function LifecycleFields({
  value,
  onChange,
  holdDaysManaged = false,
}: {
  value: LifecycleState
  onChange: (next: LifecycleState) => void
  /** The form already has its own duration field (templates), so skip the hold-days input. */
  holdDaysManaged?: boolean
}) {
  const { t } = useI18n()
  return (
    <div className="space-y-4">
      <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
        <input
          type="checkbox"
          className="mt-0.5"
          checked={value.onHold}
          onChange={(e) => onChange({ ...value, onHold: e.target.checked })}
        />
        <span>
          <span className="font-medium">{t('Start on first use (on hold)')}</span>
          <span className="mt-0.5 block text-xs leading-relaxed text-muted">
            {t('The expiry clock starts at the first connection instead of now.')}
          </span>
        </span>
      </label>
      {value.onHold && (
        <div className="grid grid-cols-2 gap-4">
          {!holdDaysManaged && (
            <Field label="Valid for (days)" hint="Counted from the first connection.">
              <Input
                type="number"
                min="1"
                max="3650"
                required
                value={value.holdDays}
                onChange={(e) => onChange({ ...value, holdDays: e.target.value })}
              />
            </Field>
          )}
          <Field
            label="Activate anyway after (days)"
            labelSuffix="(optional)"
            hint="If it is still unused by then, the clock starts anyway."
          >
            <Input
              type="number"
              min="1"
              max="3650"
              value={value.deadlineDays}
              onChange={(e) => onChange({ ...value, deadlineDays: e.target.value })}
            />
          </Field>
        </div>
      )}
      <Field label="Reset used traffic" hint="Zeroes the used data on this schedule while keeping the quota.">
        <Select value={value.reset} onChange={(e) => onChange({ ...value, reset: e.target.value as ResetStrategy })}>
          {resetStrategies.map((r) => (
            <option key={r.value} value={r.value}>
              {t(r.label)}
            </option>
          ))}
        </Select>
      </Field>
    </div>
  )
}
