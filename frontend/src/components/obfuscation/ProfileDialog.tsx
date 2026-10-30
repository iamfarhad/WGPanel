import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, ApiError } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import {
  compatLabels,
  emptyParams,
  useObfuscationPresets,
  type ObfuscationCompat,
  type ObfuscationParams,
  type ObfuscationProfile,
} from '../../lib/obfuscation'
import { Dialog } from '../ui/Dialog'
import { Button } from '../ui/Button'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Field } from '../ui/Field'

const numberKeys = ['jc', 'jmin', 'jmax', 's1', 's2', 's3', 's4'] as const
const headerKeys = ['h1', 'h2', 'h3', 'h4'] as const
const signatureKeys = ['i1', 'i2', 'i3', 'i4', 'i5'] as const

const CUSTOM = 'custom'

/** Create (profile === 'new') or edit an obfuscation profile. A new profile can start
 * from a preset - the server then generates fresh random parameters - or be entered by
 * hand; editing always shows the full parameter form. The server validates against
 * amneziawg-go's real limits and its message is shown as-is. */
export function ProfileDialog({
  profile,
  onClose,
  onSaved,
}: {
  profile: ObfuscationProfile | 'new'
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const presets = useObfuscationPresets().data?.presets ?? []
  const editing = profile !== 'new' ? profile : null

  const [name, setName] = useState(editing?.name ?? '')
  const [preset, setPreset] = useState(editing ? CUSTOM : 'compatible')
  const [compat, setCompat] = useState<ObfuscationCompat>(editing?.compat ?? 'awg1')
  const [params, setParams] = useState<ObfuscationParams>(editing?.params ?? emptyParams)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (editing) {
      setName(editing.name)
      setCompat(editing.compat)
      setParams(editing.params)
    }
  }, [editing])

  const showParams = editing != null || preset === CUSTOM
  const chosenPreset = presets.find((p) => p.id === preset)

  const saveMutation = useMutation({
    mutationFn: () => {
      if (editing) {
        return apiFetch(`/api/v1/obfuscation/profiles/${editing.id}`, {
          method: 'PATCH',
          body: JSON.stringify({ name, compat, params }),
        })
      }
      const body = preset === CUSTOM ? { name, compat, params } : { name, preset }
      return apiFetch('/api/v1/obfuscation/profiles', { method: 'POST', body: JSON.stringify(body) })
    },
    onSuccess: () => {
      push('success', editing ? t('{name} updated', { name }) : t('Profile created'))
      queryClient.invalidateQueries({ queryKey: ['obfuscation-profiles'] })
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
      onSaved()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : t('Failed to save profile')),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  const setNum = (k: (typeof numberKeys)[number], v: string) => setParams((p) => ({ ...p, [k]: Number(v) || 0 }))
  const setStr = (k: (typeof headerKeys)[number] | (typeof signatureKeys)[number], v: string) =>
    setParams((p) => ({ ...p, [k]: v }))
  const awg2 = compat === 'awg2'

  return (
    <Dialog
      open
      onClose={onClose}
      title={editing ? t('Edit {name}', { name: editing.name }) : t('New obfuscation profile')}
      maxWidthClassName="max-w-2xl"
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required maxLength={64} />
        </Field>

        {!editing && (
          <Field label="Start from" hint={chosenPreset ? t(chosenPreset.description) : undefined}>
            <Select value={preset} onChange={(e) => setPreset(e.target.value)}>
              {presets.map((p) => (
                <option key={p.id} value={p.id}>
                  {t(p.name)}
                </option>
              ))}
              <option value={CUSTOM}>{t('Custom parameters')}</option>
            </Select>
          </Field>
        )}
        {!showParams && (
          <p className="text-sm leading-relaxed text-muted">
            {t('Fresh random values are generated when the profile is created. Use Rotate later to regenerate them.')}
          </p>
        )}

        {showParams && (
          <>
            <Field
              label="Compatibility"
              hint="AWG 1.x works with every AmneziaWG app. AWG 2.0 adds more fields but needs a recent app version."
            >
              <Select value={compat} onChange={(e) => setCompat(e.target.value as ObfuscationCompat)}>
                {(['awg1', 'awg2'] as const).map((c) => (
                  <option key={c} value={c}>
                    {compatLabels[c]}
                  </option>
                ))}
              </Select>
            </Field>

            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              {numberKeys
                .filter((k) => awg2 || (k !== 's3' && k !== 's4'))
                .map((k) => (
                  <Field key={k} label={k.toUpperCase()}>
                    <Input
                      type="number"
                      min="0"
                      className="ltr"
                      value={params[k] || ''}
                      placeholder="0"
                      onChange={(e) => setNum(k, e.target.value)}
                    />
                  </Field>
                ))}
            </div>
            <p className="text-xs leading-relaxed text-muted">
              {t('Jc junk packets of Jmin to Jmax bytes are sent before each handshake. S1 and S2 pad the handshake messages (S1 + 56 must not equal S2).')}
            </p>

            <div className="grid grid-cols-2 gap-4">
              {headerKeys.map((k) => (
                <Field key={k} label={k.toUpperCase()}>
                  <Input
                    className="ltr font-mono"
                    value={params[k]}
                    placeholder={awg2 ? '100000-200000' : '123456789'}
                    onChange={(e) => setStr(k, e.target.value)}
                  />
                </Field>
              ))}
            </div>
            <p className="text-xs leading-relaxed text-muted">
              {awg2
                ? t('H1-H4: leave all empty, or give four non-overlapping numbers or ranges (A-B), each at least 5.')
                : t('H1-H4: leave all empty, or give four different numbers, each at least 5.')}
            </p>

            {awg2 && (
              <div className="space-y-3">
                {signatureKeys.map((k) => (
                  <Field
                    key={k}
                    label={k.toUpperCase()}
                    labelSuffix="(optional)"
                    hint={k === 'i1' ? t('Custom signature packet sent before the handshake, e.g. <b 0xc0><r 40><t>. Tags: <b 0xHEX>, <r N>, <rc N>, <rd N>, <t>.') : undefined}
                  >
                    <Input
                      className="ltr font-mono text-xs"
                      value={params[k]}
                      onChange={(e) => setStr(k, e.target.value)}
                    />
                  </Field>
                ))}
              </div>
            )}
          </>
        )}

        {editing && editing.node_count > 0 && (
          <p className="rounded-lg border border-warning/30 bg-warning-soft p-3 text-sm leading-relaxed text-warning-fg">
            {t('This profile is used by {n} node(s). Changing its parameters makes those nodes reconfigure, and clients must re-import their configs.', { n: editing.node_count })}
          </p>
        )}
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? 'Saving…' : editing ? 'Save changes' : 'Create profile'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
