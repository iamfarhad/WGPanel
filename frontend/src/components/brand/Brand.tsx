import mascotUrl from '../../assets/brand/jirjirak-mascot.webp'

/** The Jirjirak cricket symbol (brand pack: svg/jirjirak-symbol-primary.svg), inline so
 * it takes the current text color. `cutout` paints the eye and wing stripes - set it
 * to whatever the mark sits on so they read as holes. The viewBox is tightened around
 * the drawing so the mark centers in square containers. */
export function JirjirakMark({
  className = '',
  cutout = 'var(--ui-bg)',
}: {
  className?: string
  cutout?: string
}) {
  return (
    <svg viewBox="20 8 206 206" className={className} aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round">
        <path d="M132 57 C137 31 156 17 177 14" strokeWidth="14" />
        <path d="M151 62 C168 40 190 35 208 40" strokeWidth="14" />
        <path d="M111 145 C132 155 146 174 149 196" strokeWidth="15" />
        <path d="M66 157 C51 172 43 189 42 205" strokeWidth="15" />
      </g>
      <circle cx="139" cy="86" r="41" fill="currentColor" />
      <circle cx="154" cy="78" r="10" fill={cutout} />
      <path
        d="M38 154 C56 118 78 98 107 88 C126 81 151 83 173 96 C153 124 128 151 88 171 C69 181 49 178 38 154 Z"
        fill="currentColor"
      />
      <g stroke={cutout} strokeWidth="12" strokeLinecap="round">
        <path d="M72 112 L97 130" />
        <path d="M94 99 L119 118" />
      </g>
    </svg>
  )
}

const tileSizes = {
  sm: { tile: 'size-7 rounded-lg', mark: 'size-[19px]' },
  md: { tile: 'size-8 rounded-[9px]', mark: 'size-[22px]' },
  lg: { tile: 'size-11 rounded-[12px]', mark: 'size-[30px]' },
}

/** The mark on the brand-dark tile - the app icon in miniature (brand pack:
 * jirjirak-app-icon-dark). The product's compact signature in both themes. */
export function BrandTile({ size = 'md' }: { size?: keyof typeof tileSizes }) {
  const s = tileSizes[size]
  return (
    <span className={`flex shrink-0 items-center justify-center bg-brand-dark dark:ring-1 dark:ring-edge ${s.tile}`}>
      <JirjirakMark className={`${s.mark} text-accent`} cutout="var(--jirjirak-dark)" />
    </span>
  )
}

/** Jirjirak first, WireGuard as the product under it - never the other way round. */
export function BrandLockup({
  size = 'md',
  subtitle = 'WireGuard',
}: {
  size?: 'md' | 'lg'
  subtitle?: string
}) {
  return (
    <div className="flex items-center gap-2.5">
      <BrandTile size={size} />
      <div className="leading-tight">
        <p className={`font-semibold tracking-tight text-fg ${size === 'lg' ? 'text-lg' : 'text-[15px]'}`}>
          Jirjirak
        </p>
        <p className={`font-medium text-muted ${size === 'lg' ? 'text-[13px]' : 'text-xs'}`}>{subtitle}</p>
      </div>
    </div>
  )
}

/** The 3D mascot - for login-adjacent, empty, success and error moments only, never
 * cards, tables, navigation or charts. Rendered at most ~240px wide (the asset is
 * 480px for 2x screens). */
export function Mascot({ width = 200, className = '' }: { width?: number; className?: string }) {
  return (
    <img
      src={mascotUrl}
      alt=""
      aria-hidden="true"
      width={width}
      height={Math.round((width * 322) / 480)}
      loading="lazy"
      decoding="async"
      draggable={false}
      // The asset's soft halo is cut square at the crop; feathering the edges lets it
      // sit on any surface (tinted banners, dark cards) without a visible box.
      className={`pointer-events-none select-none [mask-image:radial-gradient(closest-side,#000_68%,transparent)] ${className}`}
    />
  )
}
