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
