import type { PlanTemplate, ResetStrategy } from './lifecycle'

export interface LifecycleState {
  onHold: boolean
  holdDays: string
  deadlineDays: string
  reset: ResetStrategy
}

export const emptyLifecycle: LifecycleState = { onHold: false, holdDays: '', deadlineDays: '', reset: 'none' }

/** Request fields for the lifecycle settings. Only what the form actually set is sent,
 * so a template the account is created from keeps supplying everything else. */
export function lifecycleBody(s: LifecycleState): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (s.onHold) {
    body.on_hold = true
    if (s.holdDays) body.hold_duration_days = Number(s.holdDays)
    if (s.deadlineDays) body.hold_deadline_days = Number(s.deadlineDays)
  }
  if (s.reset !== 'none') body.reset_strategy = s.reset
  return body
}

export function lifecycleFromTemplate(tmpl: PlanTemplate | undefined): LifecycleState {
  if (!tmpl) return emptyLifecycle
  return {
    onHold: tmpl.on_hold,
    holdDays: tmpl.on_hold && tmpl.duration_days ? String(tmpl.duration_days) : '',
    deadlineDays: tmpl.on_hold && tmpl.hold_deadline_days ? String(tmpl.hold_deadline_days) : '',
    reset: tmpl.reset_strategy,
  }
}
