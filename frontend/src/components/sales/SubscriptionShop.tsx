import { useQueryClient } from '@tanstack/react-query'
import { PublicCheckout } from './PublicCheckout'

/** The "renew your subscription" section of the public subscription page. One element
 * for SubscriptionPage to mount; it renders nothing when this account has nothing to
 * buy (no purchasable plan for its owner, or no payment method enabled). */
export function SubscriptionShop({ token }: { token: string }) {
  const queryClient = useQueryClient()
  return (
    <PublicCheckout
      base={`/api/v1/sub/${encodeURIComponent(token)}`}
      needContact={false}
      title="Renew your subscription"
      // The page shows expiry and traffic: refresh them as soon as the renewal lands.
      onFulfilled={() => queryClient.invalidateQueries({ queryKey: ['public-subscription', token] })}
    />
  )
}
