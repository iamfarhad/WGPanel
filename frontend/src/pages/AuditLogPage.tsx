import { Fragment, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ScrollText, ChevronDown, ChevronRight } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Select } from '../components/ui/Select'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'

interface AuditLogEntry {
  id: number
  actor: string
  action: string
  target: string | null
  detail: Record<string, unknown> | null
  ip_address: string | null
  created_at: string
}

export function AuditLogPage() {
  const [limit, setLimit] = useState(50)
  const [expanded, setExpanded] = useState<number | null>(null)

  const logQuery = useQuery({
    queryKey: ['audit-log', limit],
    queryFn: () => apiFetch<{ entries: AuditLogEntry[] | null }>(`/api/v1/audit-log?limit=${limit}`),
  })
  const entries = logQuery.data?.entries ?? []

  return (
    <div>
      <PageHeader
        title="Audit Log"
        description="Every admin and API-key action recorded against this panel."
        action={
          <Select value={limit} onChange={(e) => setLimit(Number(e.target.value))} className="w-40">
            <option value={20}>Last 20</option>
            <option value={50}>Last 50</option>
            <option value={100}>Last 100</option>
            <option value={200}>Last 200</option>
          </Select>
        }
      />

      <Card>
        {logQuery.isLoading && <TableSkeleton cols={4} />}
        {logQuery.isError && <p className="p-6 text-sm text-red-600 dark:text-red-400">Could not load audit log.</p>}
        {!logQuery.isLoading && !logQuery.isError && entries.length === 0 && (
          <EmptyState icon={ScrollText} title="No audit entries yet" />
        )}
        {!logQuery.isLoading && !logQuery.isError && entries.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className="border-b border-slate-200 text-slate-500 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th className="w-8 px-6 py-3" />
                <th className="px-6 py-3 font-medium">Time</th>
                <th className="px-6 py-3 font-medium">Actor</th>
                <th className="px-6 py-3 font-medium">Action</th>
                <th className="px-6 py-3 font-medium">Target</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <Fragment key={entry.id}>
                  <tr
                    onClick={() => setExpanded(expanded === entry.id ? null : entry.id)}
                    className="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50"
                  >
                    <td className="px-6 py-3 text-slate-400">
                      {expanded === entry.id ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                    </td>
                    <td className="whitespace-nowrap px-6 py-3 text-slate-500 dark:text-slate-400">
                      {new Date(entry.created_at).toLocaleString()}
                    </td>
                    <td className="px-6 py-3 text-slate-900 dark:text-slate-100">{entry.actor}</td>
                    <td className="px-6 py-3 font-mono text-xs text-slate-600 dark:text-slate-300">{entry.action}</td>
                    <td className="px-6 py-3 text-slate-500 dark:text-slate-400">{entry.target ?? '—'}</td>
                  </tr>
                  {expanded === entry.id && (
                    <tr className="border-b border-slate-100 bg-slate-50 dark:border-slate-800 dark:bg-slate-800/30">
                      <td colSpan={5} className="px-6 py-4">
                        <div className="grid grid-cols-2 gap-4 text-xs">
                          <div>
                            <p className="mb-1 font-medium text-slate-500 dark:text-slate-400">IP address</p>
                            <p className="font-mono text-slate-700 dark:text-slate-300">{entry.ip_address ?? 'unknown'}</p>
                          </div>
                          <div>
                            <p className="mb-1 font-medium text-slate-500 dark:text-slate-400">Detail</p>
                            <pre className="whitespace-pre-wrap font-mono text-slate-700 dark:text-slate-300">
                              {entry.detail ? JSON.stringify(entry.detail, null, 2) : '—'}
                            </pre>
                          </div>
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  )
}
