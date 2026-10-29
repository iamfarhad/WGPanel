import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from 'react'

/** Data-table primitives shared by every list page. Wraps the table in a
 * horizontal scroll container so wide tables never break the page layout. */
export function Table({ className = '', ...props }: HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="overflow-x-auto">
      <table className={`w-full text-start text-sm ${className}`} {...props} />
    </div>
  )
}

export function THead({ children }: { children: ReactNode }) {
  return <thead className="border-b border-edge bg-inset/50 text-xs text-muted">{children}</thead>
}

export function Th({ className = '', ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={`px-5 py-2.5 text-start font-medium ${className}`} {...props} />
}

export function Tr({
  interactive = false,
  className = '',
  ...props
}: HTMLAttributes<HTMLTableRowElement> & { interactive?: boolean }) {
  return (
    <tr
      className={`border-b border-edge/70 transition-colors duration-150 last:border-0 ${
        interactive ? 'cursor-pointer hover:bg-inset/50' : ''
      } ${className}`}
      {...props}
    />
  )
}

export function Td({ className = '', ...props }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={`px-5 py-3 ${className}`} {...props} />
}
