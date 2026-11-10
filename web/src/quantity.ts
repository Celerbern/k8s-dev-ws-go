// Helpers for parsing and comparing kubernetes resource quantities
// (e.g. "250m", "4608Mi", "8") in usage meters.

const SUFFIXES: Record<string, number> = {
  m: 1 / 1000,
  k: 1000,
  M: 1000 ** 2,
  G: 1000 ** 3,
  T: 1000 ** 4,
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
}

/** parseQuantity converts a quantity string to a plain number, or null. */
export function parseQuantity(s: string | undefined): number | null {
  if (!s) return null
  const m = s.trim().match(/^([\d.]+)([A-Za-z]+)?$/)
  if (!m) return null
  const v = parseFloat(m[1])
  if (Number.isNaN(v)) return null
  if (!m[2]) return v
  const mult = SUFFIXES[m[2]]
  if (mult === undefined) return null
  return v * mult
}

/** qtyPct returns the used/quota percentage (0-100), or null when unknown. */
export function qtyPct(used: string | undefined, quota: string | undefined): number | null {
  const u = parseQuantity(used)
  const q = parseQuantity(quota)
  if (u === null || q === null || q <= 0) return null
  return Math.min(100, Math.round((u / q) * 100))
}

/** numPct returns the used/max percentage (0-100), or null when unbounded. */
export function numPct(used: number, max: number): number | null {
  if (max <= 0) return null
  return Math.min(100, Math.round((used / max) * 100))
}

/** meterClass picks a color class for a percentage. */
export function meterClass(pct: number | null): string {
  if (pct === null) return ''
  if (pct >= 90) return 'full'
  if (pct >= 70) return 'warn'
  return ''
}