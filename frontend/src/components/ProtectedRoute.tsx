import type { ReactNode } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from '../lib/auth'
import { BrandTile } from './brand/Brand'
import { useI18n } from '../lib/i18n'

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth()
  const { t } = useI18n()

  if (isLoading) {
    // App splash: the brand tile alone, quietly pulsing - no spinner wall.
    return (
      <div className="flex h-screen flex-col items-center justify-center gap-3 bg-bg" role="status">
        <div className="animate-pulse">
          <BrandTile size="lg" />
        </div>
        <span className="sr-only">{t('Loading…')}</span>
      </div>
    )
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}
