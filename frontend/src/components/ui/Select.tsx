import type { SelectHTMLAttributes } from 'react'
import { ChevronDown } from 'lucide-react'

export function Select({ className = '', ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className={`relative ${className}`}>
      <select
        className="h-10 w-full cursor-pointer appearance-none rounded-control border border-edge bg-surface py-0 ps-3 pe-9 text-sm text-fg transition-[border-color,box-shadow] duration-150 hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none [&>option]:bg-surface [&>option]:text-fg"
        {...props}
      />
      <ChevronDown className="pointer-events-none absolute end-3 top-1/2 h-4 w-4 -translate-y-1/2 text-faint" />
    </div>
  )
}
