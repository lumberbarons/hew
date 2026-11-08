export function formatCents(cents: number): string {
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(cents);
  return `${sign}$${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, "0")}`;
}

export function truncate(s: string, max: number): string {
  if (max <= 0) return "";
  return s.length <= max ? s : s.slice(0, max - 1) + "…";
}
