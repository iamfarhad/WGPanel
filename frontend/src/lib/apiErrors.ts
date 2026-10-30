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
  reseller_has_children: 'This reseller still has sub-resellers. Delete them first.',
  reseller_too_deep: 'Sub-resellers cannot go any deeper than this.',
  child_account_allocation_exceeded: 'That is more accounts than you have left to give.',
  child_credit_allocation_exceeded: 'That is more traffic credit than you have left to give.',
  child_limit_required: 'Set an account limit and a traffic credit - unlimited is not possible under a limited plan.',
  child_expiry_too_long: 'A sub-reseller’s plan cannot end after yours.',
  child_expiry_required: 'Set a plan end date - your own plan expires.',
  child_node_group_not_allowed: 'Pick only node groups you have yourself.',
  reseller_carve_conflict: 'That is below what is already handed to the sub-resellers. Reduce theirs first.',
  reseller_locked_by_admin: 'An administrator disabled this reseller; only they can enable it.',
  parent_unavailable: 'The parent reseller is disabled or its plan has expired.',
  invalid_logo: 'The logo must be a PNG, JPEG or WebP image under 64 KB.',
  template_not_found: 'That template no longer exists, or you do not have access to it.',
  duplicate_id: 'This account is listed more than once.',
  reseller_reset_not_allowed: 'Your plan has a traffic credit, so usage cannot be reset. Renew with extra data instead.',
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
  endpoint_not_found: 'That endpoint no longer exists on this node.',
  endpoint_label_taken: 'This node already has an endpoint with that label.',
  endpoint_check_disabled: 'Health checks are turned off for this endpoint.',
  rate_limited: 'Too many requests - wait a moment and try again.',
  invalid_cursor: 'The page position is no longer valid - reload the list.',
  webhook_url_blocked: 'That address points to a private or internal network and is not allowed for webhooks.',
  webhook_not_found: 'That webhook no longer exists.',
  delivery_not_found: 'That delivery no longer exists.',
  channel_not_found: 'That notification channel no longer exists.',
  channel_test_failed: 'The test message could not be delivered.',
  notification_not_found: 'That notification no longer exists.',
  backup_not_configured: 'Set a backup password and enable a destination first.',
  backup_in_progress: 'A backup is already running.',
  node_in_maintenance: 'That node is in maintenance and takes no new accounts.',
  panel_address_unknown: 'Set the panel domain in Settings first: the node needs an address to reach the panel.',
  node_already_registered: 'This node is already installed.',
  too_many_jobs: 'Too many installations are running. Wait for one to finish.',
}

export function apiErrorMessage(err: unknown, t: (text: string) => string, fallback = 'Something went wrong'): string {
  if (err instanceof ApiError) {
    const known = err.code ? codeMessages[err.code] : undefined
    return known ? t(known) : err.message
  }
  return t(fallback)
}

/** Same lookup for a code/message pair that did not arrive as a thrown ApiError (a bulk
 * response reports one error per item). */
export function apiCodeMessage(code: string | undefined, message: string, t: (text: string) => string): string {
  const known = code ? codeMessages[code] : undefined
  return known ? t(known) : message
}
