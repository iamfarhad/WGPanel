import { Badge } from '../ui/Badge'
import { Select } from '../ui/Select'
import { Field } from '../ui/Field'
import { useI18n } from '../../lib/i18n'
import { compatLabels, useObfuscationProfiles, type NodeObfuscationInfo } from '../../lib/obfuscation'

/** Obfuscation badge for the node list: AmneziaWG (applied), pending (assigned but the
 * node has not applied it yet), or nothing for a plain WireGuard node. */
export function ObfuscationBadge({ info }: { info?: NodeObfuscationInfo }) {
  const { t } = useI18n()
  if (!info) return null
  if (info.applied) {
    return (
      <Badge tone="blue" title={info.profile_name}>
        {t('AmneziaWG')}
      </Badge>
    )
  }
  if (info.pending) {
    return (
      <Badge tone="amber" title={info.error || undefined}>
        {t('AWG pending')}
      </Badge>
    )
  }
  return null
}

/** Node edit form field: pick the node's obfuscation profile. The parent saves it
 * (PUT /nodes/{id}/obfuscation) together with the rest of the form. */
export function NodeObfuscationField({
  info,
  value,
  onChange,
}: {
  info?: NodeObfuscationInfo
  value: string
  onChange: (profileId: string) => void
}) {
  const { t } = useI18n()
  const profiles = useObfuscationProfiles().data?.profiles ?? []

  let status: string | null = null
  if (info?.pending) {
    status = info.error
      ? t('Not applied yet: {error}', { error: info.error })
      : t('Assigned, waiting for the node to apply it (next heartbeat).')
    if (info.supported === false && !info.error) {
      status = t('This node cannot run AmneziaWG yet - its container needs the /dev/net/tun device and the current node image.')
    }
  } else if (info?.applied) {
    status = t('Running {profile} (revision {rev}).', {
      profile: info.profile_name ?? '',
      rev: info.applied_revision ?? 0,
    })
  }

  return (
    <Field
      label="Obfuscation profile"
      hint={
        <>
          {t('Nodes with a profile run AmneziaWG instead of plain WireGuard. Clients need an AmneziaWG app and a re-imported config.')}
          {status && <span className="mt-1 block text-fg">{status}</span>}
        </>
      }
    >
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t('None (plain WireGuard)')}</option>
        {profiles.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name} ({compatLabels[p.compat]})
          </option>
        ))}
      </Select>
    </Field>
  )
}
