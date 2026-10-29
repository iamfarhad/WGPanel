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
  too_many_attempts: 'Too many failed attempts. Try again later.',
  invalid_mfa_code: 'That code is not valid. Check it and try again.',
  mfa_token_invalid: 'The verification step expired. Sign in again.',
  ip_not_allowed: 'Admin access is not permitted from your IP address.',
  allowlist_excludes_you: 'That list would block your own IP address. Add it first.',
  invalid_passkey: 'The passkey could not be verified.',
  passkeys_unavailable: 'Passkeys need the panel to be opened over HTTPS on its configured domain.',
  passkey_session_expired: 'The passkey step expired. Try again.',
  passkey_verification_failed: 'The passkey could not be verified.',
  passkey_exists: 'This passkey is already registered.',
  passkey_limit: 'You have reached the maximum number of passkeys.',
  passkey_not_found: 'That passkey no longer exists.',
  no_passkeys: 'This account has no passkeys.',
  totp_already_enabled: 'Two-factor authentication is already enabled.',
  totp_not_pending: 'There is no pending two-factor setup. Start again.',
  totp_not_enabled: 'Two-factor authentication is not enabled.',
}

export function apiErrorMessage(err: unknown, t: (text: string) => string, fallback = 'Something went wrong'): string {
  if (err instanceof ApiError) {
    const known = err.code ? codeMessages[err.code] : undefined
    return known ? t(known) : err.message
  }
  return t(fallback)
}
