import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AlertCircle, ArrowLeft, Fingerprint, KeyRound, Smartphone } from 'lucide-react'
import { useAuth, type MfaChallenge } from '../lib/auth'
import { ApiError } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { PasskeyCancelled, passkeysSupported } from '../lib/webauthn'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { BrandLockup, Illustration } from '../components/brand/Brand'
import { LanguageToggle } from '../components/ui/LanguageToggle'
import { useI18n } from '../lib/i18n'
import heroLight from '../assets/brand/jirjirak-hero-light.webp'
import heroDark from '../assets/brand/jirjirak-hero-dark.webp'

type MfaMode = 'totp' | 'recovery' | 'passkey'

export function LoginPage() {
  const { login, completeMfa, completeMfaWithPasskey, loginWithPasskey } = useAuth()
  const { t } = useI18n()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [challenge, setChallenge] = useState<MfaChallenge | null>(null)
  const [mode, setMode] = useState<MfaMode>('totp')
  const [code, setCode] = useState('')
  const canPasskey = passkeysSupported()

  // Lockouts (429) say how long to wait; everything else uses the shared code messages.
  function describe(err: unknown, fallback: string): string {
    if (err instanceof ApiError && err.code === 'too_many_attempts' && err.retryAfter) {
      return t('Too many failed attempts. Try again in {minutes} min.', { minutes: String(Math.max(1, Math.ceil(err.retryAfter / 60))) })
    }
    return apiErrorMessage(err, t, fallback)
  }

  async function run(fn: () => Promise<void>, fallback: string) {
    setError(null)
    setSubmitting(true)
    try {
      await fn()
      navigate('/dashboard', { replace: true })
    } catch (err) {
      if (err instanceof PasskeyCancelled) return
      if (err instanceof ApiError && err.code === 'mfa_token_invalid') setChallenge(null)
      setError(describe(err, fallback))
    } finally {
      setSubmitting(false)
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const next = await login(username, password)
      if (next) {
        setChallenge(next)
        setCode('')
        // Prefer the authenticator code; fall back to whatever the account has.
        setMode(next.methods.includes('totp') ? 'totp' : 'passkey')
      } else {
        navigate('/dashboard', { replace: true })
      }
    } catch (err) {
      setError(describe(err, 'Login failed'))
    } finally {
      setSubmitting(false)
    }
  }

  function handleMfaSubmit(e: FormEvent) {
    e.preventDefault()
    if (!challenge) return
    if (mode === 'passkey') {
      void run(() => completeMfaWithPasskey(challenge.mfaToken), 'Verification failed')
    } else {
      void run(() => completeMfa(mode, challenge.mfaToken, code), 'Verification failed')
    }
  }

  function backToPassword() {
    setChallenge(null)
    setCode('')
    setError(null)
    setPassword('')
  }

  const errorBox = error && (
    <div className="flex items-start gap-2 rounded-control border border-danger/25 bg-danger-soft px-3 py-2.5 text-sm text-danger-fg">
      <AlertCircle className="mt-px h-4 w-4 shrink-0" />
      {error}
    </div>
  )

  const modeTabs: { id: MfaMode; label: string; icon: typeof Smartphone }[] = [
    ...(challenge?.methods.includes('totp') ? [{ id: 'totp' as const, label: 'Authenticator code', icon: Smartphone }] : []),
    ...(challenge?.methods.includes('passkey') && canPasskey ? [{ id: 'passkey' as const, label: 'Passkey', icon: Fingerprint }] : []),
    ...(challenge?.methods.includes('recovery') ? [{ id: 'recovery' as const, label: 'Recovery code', icon: KeyRound }] : []),
  ]

  return (
    <div className="grid min-h-screen bg-bg lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="flex flex-col px-6 py-8 sm:px-12">
        <div className="flex items-center justify-between gap-4">
          <BrandLockup />
          <LanguageToggle className="w-24" />
        </div>

        <div className="flex flex-1 items-center justify-center py-12">
          <div className="w-full max-w-sm">
            {/* Phones get no visual panel, so the mascot greets them here instead. */}
            <Illustration name="wave" size={132} className="mx-auto mb-6 lg:hidden" />
            {challenge ? (
              <>
                <h1 className="text-2xl font-semibold tracking-tight text-fg">{t('Two-step verification')}</h1>
                <p className="mt-1.5 text-sm text-muted">{t('Confirm it is you to finish signing in.')}</p>

                {modeTabs.length > 1 && (
                  <div className="mt-6 flex gap-1 rounded-lg bg-inset p-1" role="tablist">
                    {modeTabs.map((m) => (
                      <button
                        key={m.id}
                        type="button"
                        role="tab"
                        aria-selected={mode === m.id}
                        onClick={() => {
                          setMode(m.id)
                          setError(null)
                          setCode('')
                        }}
                        className={`flex flex-1 cursor-pointer items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium transition-colors ${
                          mode === m.id ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-faint hover:text-muted'
                        }`}
                      >
                        <m.icon className="h-3.5 w-3.5" />
                        {t(m.label)}
                      </button>
                    ))}
                  </div>
                )}

                <form onSubmit={handleMfaSubmit} className="mt-6 space-y-4">
                  {mode === 'totp' && (
                    <Field label="Authenticator code" htmlFor="mfa-code" hint="Enter the 6-digit code from your authenticator app.">
                      <Input
                        id="mfa-code"
                        inputMode="numeric"
                        autoComplete="one-time-code"
                        pattern="[0-9 ]{6,7}"
                        maxLength={7}
                        value={code}
                        onChange={(e) => setCode(e.target.value)}
                        className="ltr text-center font-mono tracking-[0.3em]"
                        autoFocus
                        required
                      />
                    </Field>
                  )}
                  {mode === 'recovery' && (
                    <Field label="Recovery code" htmlFor="mfa-recovery" hint="Each recovery code works once. Use one if you lost access to your authenticator.">
                      <Input
                        id="mfa-recovery"
                        autoComplete="off"
                        value={code}
                        onChange={(e) => setCode(e.target.value)}
                        placeholder="xxxxx-xxxxx"
                        className="ltr font-mono"
                        autoFocus
                        required
                      />
                    </Field>
                  )}
                  {mode === 'passkey' && (
                    <p className="rounded-control border border-edge bg-inset px-3 py-3 text-sm text-muted">
                      {t('Use your passkey (fingerprint, face, screen lock or security key) to continue.')}
                    </p>
                  )}
                  {errorBox}
                  <Button type="submit" className="w-full" disabled={submitting || (mode !== 'passkey' && !code.trim())}>
                    {submitting ? t('Verifying…') : mode === 'passkey' ? t('Use passkey') : t('Verify')}
                  </Button>
                  <button
                    type="button"
                    onClick={backToPassword}
                    className="flex w-full cursor-pointer items-center justify-center gap-1.5 text-sm text-muted hover:text-fg"
                  >
                    <ArrowLeft className="h-3.5 w-3.5 rtl:rotate-180" />
                    {t('Back to sign in')}
                  </button>
                </form>
              </>
            ) : (
              <>
                <h1 className="text-2xl font-semibold tracking-tight text-fg">{t('Sign in')}</h1>
                <p className="mt-1.5 text-sm text-muted">{t('Manage your WireGuard infrastructure.')}</p>

                <form onSubmit={handleSubmit} className="mt-8 space-y-4">
                  <Field label={t('Username')} htmlFor="username">
                    <Input
                      id="username"
                      autoComplete="username webauthn"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      autoFocus
                      required
                    />
                  </Field>
                  <Field label={t('Password')} htmlFor="password">
                    <Input
                      id="password"
                      type="password"
                      autoComplete="current-password"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      required
                    />
                  </Field>
                  {errorBox}
                  <Button type="submit" className="w-full" disabled={submitting}>
                    {submitting ? t('Signing in…') : t('Sign in')}
                  </Button>
                </form>

                {canPasskey && (
                  <>
                    <div className="my-5 flex items-center gap-3 text-xs text-faint">
                      <span className="h-px flex-1 bg-edge" />
                      {t('or')}
                      <span className="h-px flex-1 bg-edge" />
                    </div>
                    <Button
                      type="button"
                      variant="secondary"
                      className="w-full"
                      disabled={submitting}
                      onClick={() => void run(loginWithPasskey, 'Passkey sign-in failed')}
                    >
                      <Fingerprint className="h-4 w-4" />
                      {t('Sign in with a passkey')}
                    </Button>
                  </>
                )}
              </>
            )}
          </div>
        </div>

        <p className="text-xs text-faint">{t('Jirjirak WireGuard · Secure. Simple. Fast.')}</p>
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
