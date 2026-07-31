const exactNumberFormatter = new Intl.NumberFormat("zh-CN");
const compactTokenFormatter = new Intl.NumberFormat("en-US", {
  notation: "compact",
  compactDisplay: "short",
  maximumFractionDigits: 1,
});

export function formatNumber(value: number) {
  return exactNumberFormatter.format(value);
}

export function formatTokenCompact(value: number) {
  return compactTokenFormatter.format(value);
}

export function formatUSD(value: number) {
  const maximumFractionDigits = value >= 1 ? 2 : value >= 0.01 ? 4 : 6;
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits,
  }).format(value);
}
