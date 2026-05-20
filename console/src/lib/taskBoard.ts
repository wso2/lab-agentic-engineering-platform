/** Returns a readable text color for a GitHub label hex (without #).
 *  Bright labels (high luminance) get darkened so text stays legible on the tinted bg. */
export function labelTextColor(hex: string): string {
  const r = parseInt(hex.slice(0, 2), 16) / 255;
  const g = parseInt(hex.slice(2, 4), 16) / 255;
  const b = parseInt(hex.slice(4, 6), 16) / 255;
  const luminance = 0.299 * r + 0.587 * g + 0.114 * b;
  if (luminance > 0.6) {
    const d = (v: number) => Math.round(parseInt(hex.slice(v, v + 2), 16) * 0.45).toString(16).padStart(2, '0');
    return `#${d(0)}${d(2)}${d(4)}`;
  }
  return `#${hex}`;
}
