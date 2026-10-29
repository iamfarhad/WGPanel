import type { InputHTMLAttributes } from 'react'

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={`h-10 w-full rounded-control border border-edge bg-surface px-3 text-sm text-fg transition-[border-color,box-shadow] duration-150 placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none disabled:cursor-not-allowed disabled:bg-inset disabled:text-muted ${className}`}
      {...props}
    />
  )
}
