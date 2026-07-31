export type HealthStatus = {
  backend: boolean;
  sqlite: boolean;
  initializedAt: string;
  controlToken: string;
};

export type UsageSummary = {
  range: UsageRange;
  startUtc: string;
  endUtc: string;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheCreationInputTokens: number;
  reasoningOutputTokens: number;
  reportedTotalTokens: number;
  cacheHitRate: number;
  eventCount: number;
};

export type TimelinePoint = {
  date: string;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheCreationInputTokens: number;
  reasoningOutputTokens: number;
};

export type BreakdownItem = {
  name: string;
  totalTokens: number;
  eventCount: number;
};

export type UsageDiagnostics = {
  source: string;
  status: string;
  lastScanAt: string | null;
  lastRunMode: string;
  filesSeen: number;
  eventsSeen: number;
  storedEvents: number;
  parserVersion: number;
  message: string;
  advice: string;
};

export type DashboardData = {
  summary: UsageSummary;
  timeline: TimelinePoint[];
  models: BreakdownItem[];
  projects: BreakdownItem[];
  diagnostics: UsageDiagnostics;
};

export type ScanResult = {
  mode: string;
  filesSeen: number;
  eventsSeen: number;
  eventsStored: number;
  warnings: string[];
  finishedAt: string;
};

export type UsageRange = "Today" | "7D" | "30D" | "All";

export async function loadHealth(signal?: AbortSignal) {
  return request<HealthStatus>("/api/v1/health", { signal });
}

export async function loadDashboard(range: UsageRange, signal?: AbortSignal): Promise<DashboardData> {
  const query = encodeURIComponent(range);
  return request<DashboardData>(`/api/v1/dashboard?range=${query}`, { signal });
}

export async function scanUsage(controlToken: string, full: boolean) {
  return request<ScanResult>(`/api/v1/scan?full=${full}`, {
    method: "POST",
    headers: {
      "X-Dora-Control-Token": controlToken,
    },
  });
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    cache: "no-store",
  });
  if (!response.ok) {
    const error = await response.json().catch(() => null);
    const advice =
      error && typeof error.advice === "string" ? ` ${error.advice}` : "";
    throw new Error(`Request failed (${response.status}).${advice}`);
  }
  return (await response.json()) as T;
}
