import { useEffect, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, Loader2, ShieldAlert, XCircle } from 'lucide-react'
import { apiFetch, ApiError } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useI18n } from '../../lib/i18n'
import { ltr } from '../../lib/bidi'
import { Dialog } from '../ui/Dialog'
import { Button } from '../ui/Button'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'

interface JobError {
  code: string
  message: string
}

interface Job {
  job_id: string
  node_id: string
  host: string
  status: 'running' | 'succeeded' | 'failed' | 'cancelled'
  phase: string
  log: string
  error: JobError | null
}

// English source text for t(); the backend message is only shown for unknown codes.
const ERROR_TEXT: Record<string, string> = {
  connect_failed: 'The server could not be reached over SSH. Check the host, port and firewall.',
  host_key_mismatch: "The server's host key changed after you confirmed it. The install was aborted: possible interception.",
  auth_failed: 'SSH login failed: wrong username, password or key (or the key needs its passphrase).',
  sudo_required: 'This user is not root and cannot use sudo. Log in as root, or as a user with sudo (passwordless, or with the login password).',
  unsupported_os: 'Unsupported operating system: only Debian and Ubuntu are supported.',
  no_docker: 'Docker is not installed and the installer could not install it.',
  invalid_config: 'The installer rejected its settings. See the log.',
  join_token_rejected: 'The panel rejected the join token. Retry the install.',
  install_failed: 'The installer failed. See the log for details.',
  timeout: 'The installation took too long and was stopped.',
  cancelled: 'The installation was cancelled.',
  interrupted: 'The panel restarted while the installation was running.',
}

const PHASES = ['connecting', 'checking', 'uploading', 'installing'] as const
const PHASE_LABEL: Record<string, string> = {
  connecting: 'Connecting',
  checking: 'Checking the server',
  uploading: 'Uploading the installer',
  installing: 'Installing',
}

interface Props {
  open: boolean
  onClose: () => void
  /** First unused recommended subnet, pre-filled for convenience. */
  defaultSubnet: string
  defaultCapacity: number
}

const BASE = '/api/v1/node-provisioning/ssh'

export function SshProvisionDialog({ open, onClose, defaultSubnet, defaultCapacity }: Props) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [step, setStep] = useState<'form' | 'confirm' | 'run'>('form')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Node
  const [name, setName] = useState('')
  const [group, setGroup] = useState('default')
  const [region, setRegion] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [subnet, setSubnet] = useState(defaultSubnet)
  const [capacity, setCapacity] = useState(String(defaultCapacity))
  // SSH. The secrets live only in this component's state: they are cleared the moment
  // the job starts and never written to storage.
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('root')
  const [authKind, setAuthKind] = useState<'password' | 'key'>('password')
  const [password, setPassword] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [passphrase, setPassphrase] = useState('')

  const [fingerprint, setFingerprint] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)
  const [nodeId, setNodeId] = useState<string | null>(null)

  useEffect(() => {
    if (open) setSubnet((s) => s || defaultSubnet)
  }, [open, defaultSubnet])

  const jobQuery = useQuery({
    queryKey: ['ssh-job', jobId],
    enabled: !!jobId,
    queryFn: () => apiFetch<Job>(`${BASE}/jobs/${jobId}`),
    refetchInterval: (q) => (q.state.data && q.state.data.status !== 'running' ? false : 1500),
  })
  const job = jobQuery.data

  useEffect(() => {
    if (job && job.status !== 'running') queryClient.invalidateQueries({ queryKey: ['nodes'] })
  }, [job, queryClient])

  function reset() {
    setStep('form')
    setError(null)
    setPassword('')
    setPrivateKey('')
    setPassphrase('')
    setJobId(null)
    setFingerprint('')
  }

  function close() {
    // A running job keeps going server-side; closing only hides the dialog.
    reset()
    setNodeId(null)
    onClose()
  }

  async function probe(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      const r = await apiFetch<{ fingerprint: string }>(`${BASE}/probe`, {
        method: 'POST',
        body: JSON.stringify({ host: host.trim(), port: Number(port) || 22, username: username.trim() }),
      })
      setFingerprint(r.fingerprint)
      setStep('confirm')
    } catch (err) {
      setError(apiErrorMessage(err, t))
    } finally {
      setBusy(false)
    }
  }

  async function start() {
    setError(null)
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        host: host.trim(),
        port: Number(port) || 22,
        username: username.trim(),
        fingerprint,
        ...(authKind === 'password' ? { password } : { private_key: privateKey, passphrase }),
        ...(nodeId
          ? { node_id: nodeId }
          : {
              node: {
                name: name.trim(),
                node_group: group.trim() || 'default',
                region: region.trim(),
                public_endpoint: endpoint.trim(),
                wg_subnet: subnet.trim(),
                capacity_max_peers: Number(capacity) || 0,
              },
            }),
      }
      const j = await apiFetch<Job>(`${BASE}/jobs`, { method: 'POST', body: JSON.stringify(body) })
      setNodeId(j.node_id)
      setJobId(j.job_id)
      setStep('run')
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
    } catch (err) {
      setError(apiErrorMessage(err, t))
      if (err instanceof ApiError && err.status !== 409) setStep('form')
    } finally {
      // Credentials are no longer needed in the browser once the request is sent.
      setPassword('')
      setPrivateKey('')
      setPassphrase('')
      setBusy(false)
    }
  }

  async function cancelJob() {
    if (!jobId) return
    try {
      await apiFetch(`${BASE}/jobs/${jobId}/cancel`, { method: 'POST' })
      jobQuery.refetch()
    } catch (err) {
      setError(apiErrorMessage(err, t))
    }
  }

  const credsOk = authKind === 'password' ? password.length > 0 : privateKey.trim().length > 0
  const nodeOk = !!nodeId || (name.trim() && endpoint.trim() && subnet.trim())

  return (
    <Dialog open={open} onClose={close} title={t('Install a node over SSH')} maxWidthClassName="max-w-xl">
      {step === 'form' && (
        <form onSubmit={probe} className="space-y-4">
          <p className="text-xs leading-relaxed text-muted">
            {t(
              'The panel creates the node, then connects to the server over SSH and runs the installer for you. The login is used once, kept in memory only and discarded when the job ends.',
            )}
          </p>
          {nodeId ? (
            <p className="rounded-lg border border-edge bg-inset p-3 text-xs text-muted">
              {t('Retrying with the node created by the previous attempt.')}
            </p>
          ) : (
            <div className="grid grid-cols-2 gap-3">
              <Field label="Name">
                <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="de-frankfurt-1" />
              </Field>
              <Field label="Group">
                <Input value={group} onChange={(e) => setGroup(e.target.value)} />
              </Field>
              <Field label="Public endpoint" hint="host:port clients connect to, e.g. vpn1.example.com:51820">
                <Input dir="ltr" value={endpoint} onChange={(e) => setEndpoint(e.target.value)} required />
              </Field>
              <Field label="Region" labelSuffix="(optional)">
                <Input value={region} onChange={(e) => setRegion(e.target.value)} />
              </Field>
              <Field label="WireGuard subnet">
                <Input dir="ltr" value={subnet} onChange={(e) => setSubnet(e.target.value)} required />
              </Field>
              <Field label="Capacity">
                <Input type="number" min={1} value={capacity} onChange={(e) => setCapacity(e.target.value)} />
              </Field>
            </div>
          )}
          <div className="grid grid-cols-3 gap-3 border-t border-edge pt-4">
            <div className="col-span-2">
              <Field label="SSH host">
                <Input dir="ltr" value={host} onChange={(e) => setHost(e.target.value)} required placeholder="203.0.113.10" />
              </Field>
            </div>
            <Field label="SSH port">
              <Input dir="ltr" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
            </Field>
            <Field label="SSH username">
              <Input dir="ltr" value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
            </Field>
            <div className="col-span-2">
              <Field label="Login method">
                <Select value={authKind} onChange={(e) => setAuthKind(e.target.value as 'password' | 'key')}>
                  <option value="password">{t('Password')}</option>
                  <option value="key">{t('Private key')}</option>
                </Select>
              </Field>
            </div>
          </div>
          {authKind === 'password' ? (
            <Field label="SSH password" hint="Also used for sudo when the user is not root.">
              <Input type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          ) : (
            <>
              <Field label="Private key" hint="Paste the OpenSSH/PEM private key.">
                <textarea
                  dir="ltr"
                  rows={5}
                  spellCheck={false}
                  autoComplete="off"
                  className="w-full rounded-control border border-edge bg-surface p-3 font-mono text-xs text-fg focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none"
                  value={privateKey}
                  onChange={(e) => setPrivateKey(e.target.value)}
                />
              </Field>
              <Field label="Key passphrase" labelSuffix="(if the key is encrypted)">
                <Input type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
              </Field>
            </>
          )}
          {error && <p className="text-sm text-danger-fg">{error}</p>}
          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="secondary" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !credsOk || !nodeOk}>
              {busy ? t('Contacting the server…') : t('Check host key')}
            </Button>
          </div>
        </form>
      )}

      {step === 'confirm' && (
        <div className="space-y-4">
          <div className="flex items-start gap-3 rounded-lg border border-edge bg-inset p-3">
            <ShieldAlert className="mt-0.5 h-5 w-5 shrink-0 text-warning" />
            <p className="text-sm leading-relaxed text-muted">
              {t(
                'Confirm that this is the fingerprint of the server you mean to install on (on the server: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub). If it differs, someone may be intercepting the connection.',
              )}
            </p>
          </div>
          <p className="text-xs text-muted">{ltr(`${username}@${host}:${port}`)}</p>
          <p className="break-all rounded-lg border border-edge bg-inset p-3 font-mono text-xs text-fg" dir="ltr">
            {fingerprint}
          </p>
          {error && <p className="text-sm text-danger-fg">{error}</p>}
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={() => setStep('form')}>
              Back
            </Button>
            <Button onClick={start} disabled={busy}>
              {busy ? t('Starting…') : t('Fingerprint matches, install')}
            </Button>
          </div>
        </div>
      )}

      {step === 'run' && (
        <div className="space-y-4">
          <ol className="space-y-1.5 text-sm">
            {PHASES.map((p) => {
              const idx = PHASES.indexOf(p)
              const cur = job ? PHASES.indexOf(job.phase as (typeof PHASES)[number]) : -1
              const done = job?.status === 'succeeded' || (cur > idx && cur !== -1)
              const active = job?.status === 'running' && cur === idx
              const failedHere = job && job.status !== 'running' && job.status !== 'succeeded' && cur === idx
              return (
                <li key={p} className={`flex items-center gap-2 ${done || active ? 'text-fg' : 'text-faint'}`}>
                  {done ? (
                    <CheckCircle2 className="h-4 w-4 text-success" />
                  ) : active ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : failedHere ? (
                    <XCircle className="h-4 w-4 text-danger" />
                  ) : (
                    <span className="h-4 w-4" />
                  )}
                  {t(PHASE_LABEL[p])}
                </li>
              )
            })}
          </ol>
          <pre
            dir="ltr"
            className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-edge bg-inset p-3 font-mono text-[11px] leading-relaxed text-fg"
          >
            {job?.log || '…'}
          </pre>
          {job?.status === 'succeeded' && (
            <p className="text-sm text-success-fg">
              {t('Installed. The node registers with the panel on its own and shows as online in a few seconds.')}
            </p>
          )}
          {job && (job.status === 'failed' || job.status === 'cancelled') && (
            <p className="text-sm text-danger-fg">
              {job.error ? (ERROR_TEXT[job.error.code] ? t(ERROR_TEXT[job.error.code]) : job.error.message) : t('Failed.')}
            </p>
          )}
          {error && <p className="text-sm text-danger-fg">{error}</p>}
          <div className="flex justify-end gap-2 pt-2">
            {(!job || job.status === 'running') && (
              <Button variant="secondary" onClick={cancelJob}>
                Cancel
              </Button>
            )}
            {job && (job.status === 'failed' || job.status === 'cancelled') && (
              <Button variant="secondary" onClick={reset}>
                {t('Try again')}
              </Button>
            )}
            {job && job.status !== 'running' && <Button onClick={close}>{t('Close')}</Button>}
          </div>
        </div>
      )}
    </Dialog>
  )
}
