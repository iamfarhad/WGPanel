import { useEffect, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Tag, X } from 'lucide-react'
import { useI18n } from '../../lib/i18n'
import { MAX_TAGS, normalizeTag, parseTags } from '../../lib/abuse'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'

/** Small read-only chips for an account's tags; shows the first `max` and a "+n". */
export function TagChips({ tags, max = 3, onTagClick }: { tags: string[]; max?: number; onTagClick?: (tag: string) => void }) {
  if (tags.length === 0) return null
  const shown = tags.slice(0, max)
  const rest = tags.length - shown.length
  const cls = 'inline-flex items-center rounded-full bg-inset px-2 py-0.5 text-xs text-muted ring-1 ring-inset ring-edge'
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {shown.map((tag) =>
        onTagClick ? (
          <button
            key={tag}
            type="button"
            className={`${cls} cursor-pointer hover:text-fg`}
            onClick={(e) => {
              e.stopPropagation() // the row itself opens the detail dialog
              onTagClick(tag)
            }}
          >
            {tag}
          </button>
        ) : (
          <span key={tag} className={cls}>
            {tag}
          </span>
        ),
      )}
      {rest > 0 && <span className="text-xs text-faint">+{rest}</span>}
    </span>
  )
}

/** Tag editor: type a tag and press Enter or comma (paste a comma-separated list works
 * too). The chips show the normalized form that will actually be stored. */
export function TagInput({
  value,
  onChange,
  suggestions = [],
  id,
}: {
  value: string[]
  onChange: (tags: string[]) => void
  suggestions?: string[]
  id?: string
}) {
  const { t } = useI18n()
  const [draft, setDraft] = useState('')
  const listId = id ? `${id}-suggestions` : undefined

  function commit(text: string) {
    const added = parseTags(text)
    if (added.length === 0) {
      setDraft('')
      return
    }
    onChange([...new Set([...value, ...added])].sort().slice(0, MAX_TAGS))
    setDraft('')
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',' || e.key === '،') {
      e.preventDefault() // Enter must not submit the surrounding form
      commit(draft)
    } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
      onChange(value.slice(0, -1))
    }
  }

  return (
    <div>
      {value.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {value.map((tag) => (
            <span key={tag} className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2 py-0.5 text-xs text-accent-fg">
              {tag}
              <button type="button" aria-label={t('Remove tag {name}', { name: tag })} onClick={() => onChange(value.filter((x) => x !== tag))}>
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <Input
        id={id}
        value={draft}
        list={listId}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        onBlur={() => commit(draft)}
        placeholder={t('Add a tag and press Enter')}
        disabled={value.length >= MAX_TAGS}
      />
      {listId && (
        <datalist id={listId}>
          {suggestions
            .filter((s) => !value.includes(s))
            .map((s) => (
              <option key={s} value={normalizeTag(s)} />
            ))}
        </datalist>
      )}
    </div>
  )
}

/** Accounts-list tag filter: pick tags from those in use; an account must carry all of
 * the selected ones. */
export function TagFilter({ available, selected, onChange }: { available: string[]; selected: string[]; onChange: (tags: string[]) => void }) {
  const { t } = useI18n()
  if (available.length === 0 && selected.length === 0) return null
  const choices = available.filter((tag) => !selected.includes(tag))
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        value=""
        onChange={(e) => e.target.value && onChange([...selected, e.target.value])}
        aria-label={t('Filter by tag')}
        className="w-full max-w-44"
      >
        <option value="">{selected.length > 0 ? t('Add tag filter…') : t('All tags')}</option>
        {choices.map((tag) => (
          <option key={tag} value={tag}>
            {tag}
          </option>
        ))}
      </Select>
      {selected.map((tag) => (
        <button
          key={tag}
          type="button"
          onClick={() => onChange(selected.filter((x) => x !== tag))}
          className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2.5 py-1 text-xs text-accent-fg"
          title={t('Remove tag {name}', { name: tag })}
        >
          <Tag className="h-3 w-3" />
          {tag}
          <X className="h-3 w-3" />
        </button>
      ))}
    </div>
  )
}

/** Bulk add/remove tags for the selected accounts. */
export function BulkTagDialog({
  open,
  count,
  submitting,
  suggestions,
  onClose,
  onSubmit,
}: {
  open: boolean
  count: number
  submitting: boolean
  suggestions: string[]
  onClose: () => void
  onSubmit: (mode: 'add_tags' | 'remove_tags', tags: string[]) => void
}) {
  const { t } = useI18n()
  const [mode, setMode] = useState<'add_tags' | 'remove_tags'>('add_tags')
  const [tags, setTags] = useState<string[]>([])
  useEffect(() => {
    if (open) {
      setMode('add_tags')
      setTags([])
    }
  }, [open])

  function submit(e: FormEvent) {
    e.preventDefault()
    if (tags.length > 0) onSubmit(mode, tags)
  }

  return (
    <Dialog open={open} onClose={onClose} title="Tag accounts">
      <form onSubmit={submit} className="space-y-4">
        <p className="text-sm text-muted">{t('Change the tags of {n} accounts. Their other tags are left alone.', { n: count })}</p>
        <Field label="Action">
          <Select value={mode} onChange={(e) => setMode(e.target.value as 'add_tags' | 'remove_tags')}>
            <option value="add_tags">{t('Add these tags')}</option>
            <option value="remove_tags">{t('Remove these tags')}</option>
          </Select>
        </Field>
        <Field label="Tags">
          <TagInput value={tags} onChange={setTags} suggestions={suggestions} id="bulk-tags" />
        </Field>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button type="submit" disabled={submitting || tags.length === 0}>
            {submitting ? t('Working…') : t('Apply')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
