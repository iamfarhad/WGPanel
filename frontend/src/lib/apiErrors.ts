import { ApiError } from './api'

/** User-facing text for API error codes whose server message is English-only and
 * technical. Values are English source text for t(); unknown codes fall back to the
 * server's own message. */
const codeMessages: Record<string, string> = {
  reseller_account_limit_reached: 'You have reached your account limit.',
  reseller_data_credit_exceeded: 'This would exceed your traffic credit.',
  reseller_unlimited_not_allowed: 'Set a data quota - unlimited accounts are not available on your plan.',
  reseller_expiry_too_long: 'This expiry is later than your plan allows.',
  reseller_expiry_required: 'Set an expiry date - your plan requires one.',
  reseller_node_group_not_allowed: 'That node is not available on your plan.',
  reseller_disabled: 'Your reseller account is disabled.',
  reseller_expired: 'Your reseller plan has expired.',
  account_disabled: 'This account is disabled.',
  account_suspended_by_admin: 'An administrator suspended this account; only they can re-enable it.',
  reseller_has_accounts: 'This reseller still owns accounts. Delete or move them first.',
  username_taken: 'That username is already taken.',
}

export function apiErrorMessage(err: unknown, t: (text: string) => string, fallback = 'Something went wrong'): string {
  if (err instanceof ApiError) {
    const known = err.code ? codeMessages[err.code] : undefined
    return known ? t(known) : err.message
  }
  return t(fallback)
}
