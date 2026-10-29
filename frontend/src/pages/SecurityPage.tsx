import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, Fingerprint, KeyRound, Plus, RefreshCw, ShieldOff, Smartphone, Trash2 } from 'lucide-react'
import QRCode from 'qrcode'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import { createPasskey, PasskeyCancelled, passkeysSupported, type CreationOptionsJSON } from '../lib/webauthn'
import { PageHeader } from '../components/ui/PageHeader'
import { Card, CardHeader } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { Skeleton } from '../components/ui/Skeleton'

interface Passkey {
  id: string
  name: string
  created_at: string
  last_used_at: string | null
}

interface MySecurity {
  totp_enabled: boolean
  totp_pending: boolean
  recovery_codes_remaining: number
  passkeys: Passkey[]
}

const QUERY_KEY = ['my-security']

export function SecurityPage() {
  const query = useQuery({ queryKey: QUERY_KEY, queryFn: () => apiFetch<MySecurity>('/api/v1/me/security') })

  return (
    <div>
      <PageHeader
        title="Security"
        description="Protect your sign-in with a second step: an authenticator app, passkeys, or both."
      />
      <div className="max-w-2xl space-y-6">
        {query.isLoading && (
          <Card className="space-y-3 p-6">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-16" />
          </Card>
        )}
        {query.isError && <ErrorState title="Could not load your security settings." onRetry={() => query.refetch()} />}
        {query.data && (
          <>
            <TotpCard data={query.data} />
            <PasskeysCard data={query.data} />
          </>
        )}
      </div>
    </div>
  )
}

function useRefresh() {
  const queryClient = useQueryClient()
  return () => queryClient.invalidateQueries({ queryKey: QUERY_KEY })
}

// ---- TOTP ----

function TotpCard({ data }: { data: MySecurity }) {
  const { t } = useI18n()
  const [setupOpen, setSetupOpen] = useState(false)
  const [disableOpen, setDisableOpen] = useState(false)
  const [regenOpen, setRegenOpen] = useState(false)

  return (
    <Card className="overflow-hidden">
      <CardHeader
        title="Authenticator app (TOTP)"
        description="A 6-digit code from an app such as Google Authenticator, Aegis or 1Password is asked for after your password."
        action={
          data.totp_enabled ? <Badge tone="green" dot>Enabled</Badge> : <Badge tone="slate">Off</Badge>
        }
      />
      <div className="space-y-4 p-6">
        {data.totp_enabled ? (
          <>
            <p className="text-sm text-muted">
              {t('Recovery codes left: {n}. Each one can be used once if you lose access to your authenticator.', {
                n: String(data.recovery_codes_remaining),
              })}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" onClick={() => setRegenOpen(true)}>
                <RefreshCw className="h-4 w-4" />
                New recovery codes
              </Button>
              <Button variant="danger" onClick={() => setDisableOpen(true)}>
                <ShieldOff className="h-4 w-4" />
                Turn off
              </Button>
            </div>
          </>
        ) : (
          <Button onClick={() => setSetupOpen(true)}>
            <Smartphone className="h-4 w-4" />
            Set up authenticator app
          </Button>
        )}
      </div>
      {setupOpen && <TotpSetupDialog onClose={() => setSetupOpen(false)} />}
      {disableOpen && <ReauthDialog kind="disable" onClose={() => setDisableOpen(false)} />}
      {regenOpen && <ReauthDialog kind="regenerate" onClose={() => setRegenOpen(false)} />}
    </Card>
  )
}

function RecoveryCodes({ codes }: { codes: string[] }) {
  const { t } = useI18n()
  const { push } = useToast()
  const text = codes.join('\n')
  function download() {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = 'jirjirak-recovery-codes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="space-y-3">
      <div className="ltr grid grid-cols-2 gap-x-6 gap-y-1.5 rounded-control border border-edge bg-inset p-4 font-mono text-sm text-fg">
        {codes.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </div>
      <div className="flex gap-2">
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() => {
            void navigator.clipboard?.writeText(text)
            push('success', t('Copied'))
          }}
        >
          <Copy className="h-3.5 w-3.5" />
          Copy
        </Button>
        <Button type="button" variant="secondary" size="sm" onClick={download}>
          <Download className="h-3.5 w-3.5" />
          Download
        </Button>
      </div>
    </div>
  )
}

function TotpSetupDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const refresh = useRefresh()
  const [secret, setSecret] = useState<string | null>(null)
  const [qr, setQr] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [recovery, setRecovery] = useState<string[] | null>(null)

  const enroll = useMutation({
    mutationFn: () => apiFetch<{ secret: string; otpauth_uri: string }>('/api/v1/me/security/totp/enroll', { method: 'POST' }),
    onSuccess: async (d) => {
      setSecret(d.secret)
      setQr(await QRCode.toDataURL(d.otpauth_uri, { width: 200, margin: 1 }))
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })
  const confirm = useMutation({
    mutationFn: () =>
      apiFetch<{ recovery_codes: string[] }>('/api/v1/me/security/totp/confirm', { method: 'POST', body: JSON.stringify({ code }) }),
    onSuccess: (d) => {
      setRecovery(d.recovery_codes)
      push('success', t('Two-factor authentication is on'))
      refresh()
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })

  // Start the enrolment as soon as the dialog opens (once).
  const { mutate: startEnroll } = enroll
  useEffect(() => {
    startEnroll()
  }, [startEnroll])

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    confirm.mutate()
  }

  if (recovery) {
    return (
      <Dialog open onClose={onClose} title="Save your recovery codes">
        <div className="space-y-4">
          <p className="text-sm leading-relaxed text-muted">
            {t('Keep these codes somewhere safe. Each works once if you lose your authenticator, and they will not be shown again.')}
          </p>
          <RecoveryCodes codes={recovery} />
          <div className="flex justify-end pt-2">
            <Button onClick={onClose}>Done</Button>
          </div>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog open onClose={onClose} title="Set up authenticator app">
      <form onSubmit={submit} className="space-y-4">
        <p className="text-sm leading-relaxed text-muted">
          {t('Scan this QR code with your authenticator app, then enter the 6-digit code it shows.')}
        </p>
        <div className="flex min-h-[200px] items-center justify-center">
          {qr ? (
            <img src={qr} alt={t('Authenticator QR code')} className="h-[200px] w-[200px] rounded-control border border-edge bg-white" />
          ) : (
            <Skeleton className="h-[200px] w-[200px]" />
          )}
        </div>
        {secret && (
          <p className="text-center text-xs text-muted">
            {t('Cannot scan? Enter this key manually:')}{' '}
            <code className="ltr inline-block select-all font-mono text-fg">{secret}</code>
          </p>
        )}
        <Field label="Verification code" htmlFor="totp-confirm">
          <Input
            id="totp-confirm"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={7}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            className="ltr text-center font-mono tracking-[0.3em]"
            required
          />
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={confirm.isPending || !secret || !code.trim()}>
            {confirm.isPending ? 'Verifying…' : 'Turn on'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** Password + code re-authentication for turning TOTP off or regenerating recovery codes. */
function ReauthDialog({ kind, onClose }: { kind: 'disable' | 'regenerate'; onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const refresh = useRefresh()
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [recovery, setRecovery] = useState<string[] | null>(null)

  const mutation = useMutation({
    mutationFn: () =>
      apiFetch<{ recovery_codes?: string[] } | undefined>(
        kind === 'disable' ? '/api/v1/me/security/totp/disable' : '/api/v1/me/security/recovery-codes',
        { method: 'POST', body: JSON.stringify({ password, code }) },
      ),
    onSuccess: (d) => {
      refresh()
      if (kind === 'disable') {
        push('success', t('Two-factor authentication is off'))
        onClose()
      } else if (d?.recovery_codes) {
        setRecovery(d.recovery_codes)
      }
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })

  if (recovery) {
    return (
      <Dialog open onClose={onClose} title="Your new recovery codes">
        <div className="space-y-4">
          <p className="text-sm leading-relaxed text-muted">{t('The old codes no longer work. Save these now - they will not be shown again.')}</p>
          <RecoveryCodes codes={recovery} />
          <div className="flex justify-end pt-2">
            <Button onClick={onClose}>Done</Button>
          </div>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog open onClose={onClose} title={kind === 'disable' ? 'Turn off two-factor authentication' : 'New recovery codes'}>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setError(null)
          mutation.mutate()
        }}
        className="space-y-4"
      >
        <p className="text-sm leading-relaxed text-muted">
          {t('Confirm with your password and a current authenticator code (or an unused recovery code).')}
        </p>
        <Field label="Password">
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </Field>
        <Field label="Authenticator or recovery code">
          <Input autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} className="ltr font-mono" required />
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant={kind === 'disable' ? 'danger' : 'primary'} disabled={mutation.isPending}>
            {kind === 'disable' ? 'Turn off' : 'Generate'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

// ---- passkeys ----

function PasskeysCard({ data }: { data: MySecurity }) {
  const { t, formatDate } = useI18n()
  const { push } = useToast()
  const refresh = useRefresh()
  const supported = passkeysSupported()
  const [addOpen, setAddOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Passkey | null>(null)

  const remove = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/me/security/passkeys/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', t('Passkey removed'))
      setDeleteTarget(null)
      refresh()
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  return (
    <Card className="overflow-hidden">
      <CardHeader
        title="Passkeys"
        description="Sign in with your fingerprint, face, screen lock or a security key. A passkey also works as your second step after a password."
        action={
          <Button size="sm" onClick={() => setAddOpen(true)} disabled={!supported}>
            <Plus className="h-4 w-4" />
            Add passkey
          </Button>
        }
      />
      {!supported && (
        <p className="border-b border-edge bg-warning-soft px-6 py-3 text-sm text-warning-fg">
          {t('This browser or connection does not support passkeys. Open the panel over HTTPS in a recent browser to use them.')}
        </p>
      )}
      {data.passkeys.length === 0 ? (
        <EmptyState illustration="security" title="No passkeys yet" description="Add one to sign in without typing a password." />
      ) : (
        <ul className="divide-y divide-edge">
          {data.passkeys.map((p) => (
            <li key={p.id} className="flex items-center gap-3 px-6 py-3.5">
              <Fingerprint className="h-5 w-5 shrink-0 text-faint" strokeWidth={1.75} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-fg">{p.name}</p>
                <p className="text-xs text-muted">
                  {t('Added {date}', { date: formatDate(p.created_at, { dateStyle: 'medium' }) })}
                  {' · '}
                  {p.last_used_at ? t('Last used {date}', { date: formatDate(p.last_used_at, { dateStyle: 'medium' }) }) : t('Never used')}
                </p>
              </div>
              <Button variant="ghost" size="icon" title={t('Remove')} onClick={() => setDeleteTarget(p)}>
                <Trash2 className="h-4 w-4" />
                <span className="sr-only">{t('Remove')}</span>
              </Button>
            </li>
          ))}
        </ul>
      )}
      {addOpen && <AddPasskeyDialog onClose={() => setAddOpen(false)} />}
      {deleteTarget && (
        <ConfirmDialog
          open
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => remove.mutate(deleteTarget.id)}
          title="Remove passkey"
          description={t('"{name}" will no longer work for signing in. This cannot be undone.', { name: deleteTarget.name })}
          confirmLabel="Remove"
          danger
          submitting={remove.isPending}
        />
      )}
    </Card>
  )
}

function AddPasskeyDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const refresh = useRefresh()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)

  const add = useMutation({
    mutationFn: async () => {
      const begin = await apiFetch<{ session_id: string; options: CreationOptionsJSON }>('/api/v1/me/security/passkeys/begin', {
        method: 'POST',
      })
      const credential = await createPasskey(begin.options)
      const label = name.trim() || t('Passkey')
      return apiFetch<Passkey>(
        `/api/v1/me/security/passkeys/finish?session=${encodeURIComponent(begin.session_id)}&name=${encodeURIComponent(label)}`,
        { method: 'POST', body: JSON.stringify(credential) },
      )
    },
    onSuccess: () => {
      push('success', t('Passkey added'))
      refresh()
      onClose()
    },
    onError: (err) => {
      if (err instanceof PasskeyCancelled) return
      setError(apiErrorMessage(err, t, 'Could not add the passkey'))
    },
  })

  return (
    <Dialog open onClose={onClose} title="Add passkey">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setError(null)
          add.mutate()
        }}
        className="space-y-4"
      >
        <Field label="Name" hint="Something that tells you which device or key this is, e.g. Work laptop.">
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} placeholder={t('Work laptop')} autoFocus />
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={add.isPending}>
            <KeyRound className="h-4 w-4" />
            {add.isPending ? 'Waiting for your device…' : 'Continue'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

