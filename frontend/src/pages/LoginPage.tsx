import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AlertCircle } from 'lucide-react'
import { useAuth } from '../lib/auth'
import { ApiError } from '../lib/api'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { BrandLockup } from '../components/brand/Brand'
import heroLight from '../assets/brand/jirjirak-hero-light.webp'
import heroDark from '../assets/brand/jirjirak-hero-dark.webp'

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await login(username, password)
      navigate('/dashboard', { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Login failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="grid min-h-screen bg-bg lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="flex flex-col px-6 py-8 sm:px-12">
        <BrandLockup />

        <div className="flex flex-1 items-center justify-center py-12">
          <div className="w-full max-w-sm">
            <h1 className="text-2xl font-semibold tracking-tight text-fg">Sign in</h1>
            <p className="mt-1.5 text-sm text-muted">Manage your WireGuard infrastructure.</p>

            <form onSubmit={handleSubmit} className="mt-8 space-y-4">
              <Field label="Username" htmlFor="username">
                <Input
                  id="username"
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoFocus
                  required
                />
              </Field>
              <Field label="Password" htmlFor="password">
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
              </Field>
              {error && (
                <div className="flex items-start gap-2 rounded-control border border-danger/25 bg-danger-soft px-3 py-2.5 text-sm text-danger-fg">
                  <AlertCircle className="mt-px h-4 w-4 shrink-0" />
                  {error}
                </div>
              )}
              <Button type="submit" className="w-full" disabled={submitting}>
                {submitting ? 'Signing in…' : 'Sign in'}
              </Button>
            </form>
          </div>
        </div>

        <p className="text-xs text-faint">Jirjirak WireGuard · Secure. Simple. Fast.</p>
      </div>

      {/* Brand visual. The hero's own wordmark sits on its far left, so it is cropped
          toward the mascot (object-position) - the lockup on the form side already
          names the product. Desktop only; phones get the form alone. */}
      <div className="relative hidden p-3 lg:block">
        <div className="relative h-full overflow-hidden rounded-surface border border-edge">
          <img src={heroLight} alt="" className="absolute inset-0 h-full w-full object-cover object-[86%_50%] dark:hidden" />
          <img src={heroDark} alt="" className="absolute inset-0 hidden h-full w-full object-cover object-[86%_50%] dark:block" />
        </div>
      </div>
    </div>
  )
}
