import type { ButtonHTMLAttributes } from 'react'
import { useI18n } from '../../lib/i18n'

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'md' | 'sm' | 'icon'
}

const variants = {
  primary: 'bg-accent text-on-accent hover:bg-accent-hover active:bg-accent-active',
  secondary: 'border border-edge bg-surface text-fg hover:bg-inset hover:border-edge-strong',
  ghost: 'text-muted hover:bg-inset hover:text-fg',
  danger: 'bg-danger text-white hover:bg-danger/90 active:bg-danger/85',
}

const sizes = {
  md: 'h-10 px-4 text-sm',
  sm: 'h-8 px-3 text-xs',
  icon: 'h-8 w-8',
}

export function Button({ variant = 'primary', size = 'md', className = '', children, ...props }: ButtonProps) {
  const { t } = useI18n()
  return (
    <button
      className={`inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-control font-medium transition-[background-color,border-color,color,box-shadow] duration-150 outline-none focus-visible:ring-[3px] focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 ${variants[variant]} ${sizes[size]} ${className}`}
      {...props}
    >
      {typeof children === 'string' ? t(children) : children}
    </button>
  )
}
