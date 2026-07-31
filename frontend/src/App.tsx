import { useEffect, useState } from "react";
import {
  type BreakdownItem,
  type DashboardData,
  type DoraSettings,
  type HealthStatus,
  type QuotaData,
  type QuotaItem,
  type TimelinePoint,
  type UsageRange,
  loadDashboard,
  loadHealth,
  loadQuotas,
  loadSettings,
  refreshCodexQuota,
  scanUsage,
  updateSettings,
} from "./api";

type LoadState<T> =
  | { kind: "loading" }
  | { kind: "ready"; value: T }
  | { kind: "error"; message: string };

const ranges: UsageRange[] = ["Today", "7D", "30D", "All"];

function App() {
  const [view, setView] = useState<"dashboard" | "diagnostics">("dashboard");
  const [range, setRange] = useState<UsageRange>("7D");
  const [health, setHealth] = useState<LoadState<HealthStatus>>({ kind: "loading" });
  const [dashboard, setDashboard] = useState<LoadState<DashboardData>>({ kind: "loading" });
  const [quota, setQuota] = useState<LoadState<QuotaData>>({ kind: "loading" });
  const [settings, setSettings] = useState<LoadState<DoraSettings>>({ kind: "loading" });
  const [refreshVersion, setRefreshVersion] = useState(0);
  const [refreshing, setRefreshing] = useState<"incremental" | "full" | null>(null);
  const [refreshMessage, setRefreshMessage] = useState("");
  const [quotaBusy, setQuotaBusy] = useState(false);
  const [quotaMessage, setQuotaMessage] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    loadHealth(controller.signal)
      .then((value) => setHealth({ kind: "ready", value }))
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setHealth({ kind: "error", message: errorMessage(error) });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([loadQuotas(controller.signal), loadSettings(controller.signal)])
      .then(([quotaValue, settingsValue]) => {
        setQuota({ kind: "ready", value: quotaValue });
        setSettings({ kind: "ready", value: settingsValue });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          const message = errorMessage(error);
          setQuota({ kind: "error", message });
          setSettings({ kind: "error", message });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setDashboard({ kind: "loading" });
    loadDashboard(range, controller.signal)
      .then((value) => setDashboard({ kind: "ready", value }))
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setDashboard({ kind: "error", message: errorMessage(error) });
        }
      });
    return () => controller.abort();
  }, [range, refreshVersion]);

  async function refresh(full: boolean) {
    if (health.kind !== "ready") {
      return;
    }
    setRefreshing(full ? "full" : "incremental");
    setRefreshMessage("");
    try {
      const result = await scanUsage(health.value.controlToken, full);
      const imported =
        result.mode === "full"
          ? `${formatNumber(result.eventsSeen)} records verified`
          : result.eventsSeen > 0
          ? `${formatNumber(result.eventsSeen)} new records parsed`
          : "Already up to date";
      setRefreshMessage(`${imported} · ${formatNumber(result.eventsStored)} events stored`);
    } catch (error) {
      setRefreshMessage(errorMessage(error));
    } finally {
      setRefreshing(null);
      setRefreshVersion((value) => value + 1);
    }
  }

  async function refreshQuota() {
    if (health.kind !== "ready") {
      return;
    }
    setQuotaBusy(true);
    setQuotaMessage("");
    try {
      const value = await refreshCodexQuota(health.value.controlToken);
      setQuota({ kind: "ready", value });
      setQuotaMessage("Subscription quota refreshed");
    } catch (error) {
      setQuotaMessage(errorMessage(error));
      try {
        setQuota({ kind: "ready", value: await loadQuotas() });
      } catch (reloadError) {
        setQuota({ kind: "error", message: errorMessage(reloadError) });
      }
    } finally {
      setQuotaBusy(false);
    }
  }

  async function setQuotaConsent(enabled: boolean) {
    if (health.kind !== "ready") {
      return;
    }
    setQuotaBusy(true);
    setQuotaMessage("");
    try {
      const values = await updateSettings(health.value.controlToken, enabled);
      setSettings({ kind: "ready", value: values });
      if (enabled) {
        const value = await refreshCodexQuota(health.value.controlToken);
        setQuota({ kind: "ready", value });
        setQuotaMessage("Codex subscription quota enabled");
      } else {
        setQuota({ kind: "ready", value: await loadQuotas() });
        setQuotaMessage("Codex subscription quota disabled");
      }
    } catch (error) {
      setQuotaMessage(errorMessage(error));
      try {
        const [quotaValue, settingsValue] = await Promise.all([loadQuotas(), loadSettings()]);
        setQuota({ kind: "ready", value: quotaValue });
        setSettings({ kind: "ready", value: settingsValue });
      } catch (reloadError) {
        const message = errorMessage(reloadError);
        setQuota({ kind: "error", message });
        setSettings({ kind: "error", message });
      }
    } finally {
      setQuotaBusy(false);
    }
  }

  const online = health.kind === "ready" && health.value.backend && health.value.sqlite;

  return (
    <div className="app-shell">
      <header className="topbar">
        <button className="brand" type="button" onClick={() => setView("dashboard")}>
          <span className="brand-mark" aria-hidden="true">D</span>
          <span>Dora</span>
        </button>

        <nav className="primary-nav" aria-label="Primary">
          <button
            className={view === "dashboard" ? "active" : ""}
            type="button"
            aria-pressed={view === "dashboard"}
            onClick={() => setView("dashboard")}
          >
            Dashboard
          </button>
          <button
            className={view === "diagnostics" ? "active" : ""}
            type="button"
            aria-pressed={view === "diagnostics"}
            onClick={() => setView("diagnostics")}
          >
            Diagnostics
          </button>
        </nav>

        <div className={`connection-pill ${online ? "online" : ""}`}>
          <span aria-hidden="true" />
          {online ? "Local data" : "Disconnected"}
        </div>
      </header>

      <main className="content">
        {health.kind === "error" && (
          <Notice
            tone="error"
            title="Dora backend is unavailable"
            body={`Start the backend and refresh this page. ${health.message}`}
          />
        )}

        {view === "dashboard" ? (
          <Dashboard
            range={range}
            setRange={setRange}
            state={dashboard}
            refreshing={refreshing !== null}
            refreshMessage={refreshMessage}
            onRefresh={() => void refresh(false)}
            quota={quota}
            quotaBusy={quotaBusy}
            quotaMessage={quotaMessage}
            onQuotaRefresh={() => void refreshQuota()}
          />
        ) : (
          <Diagnostics
            state={dashboard}
            health={health}
            refreshing={refreshing}
            refreshMessage={refreshMessage}
            onScan={(full) => void refresh(full)}
            quota={quota}
            settings={settings}
            quotaBusy={quotaBusy}
            quotaMessage={quotaMessage}
            onQuotaRefresh={() => void refreshQuota()}
            onQuotaConsent={(enabled) => void setQuotaConsent(enabled)}
          />
        )}
      </main>
    </div>
  );
}

type DashboardProps = {
  range: UsageRange;
  setRange: (range: UsageRange) => void;
  state: LoadState<DashboardData>;
  refreshing: boolean;
  refreshMessage: string;
  onRefresh: () => void;
  quota: LoadState<QuotaData>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
};

function Dashboard({
  range,
  setRange,
  state,
  refreshing,
  refreshMessage,
  onRefresh,
  quota,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
}: DashboardProps) {
  return (
    <>
      <section className="page-heading">
        <div>
          <p className="eyebrow">Codex usage</p>
          <h1>Your local activity, clearly counted.</h1>
          <p className="lede">Private token analytics built from the Codex data already on this Mac.</p>
        </div>
        <button className="refresh-button" type="button" disabled={refreshing} onClick={onRefresh}>
          <RefreshIcon spinning={refreshing} />
          {refreshing ? "Scanning…" : "Refresh usage"}
        </button>
      </section>

      <div className="range-row">
        <div className="range-picker" aria-label="Usage range">
          {ranges.map((value) => (
            <button
              className={range === value ? "active" : ""}
              type="button"
              aria-pressed={range === value}
              key={value}
              onClick={() => setRange(value)}
            >
              {value}
            </button>
          ))}
        </div>
        {refreshMessage && <p className="refresh-message" role="status">{refreshMessage}</p>}
      </div>

      {state.kind === "loading" && <DashboardSkeleton />}
      {state.kind === "error" && (
        <Notice tone="error" title="Usage could not be loaded" body={state.message} />
      )}
      {state.kind === "ready" && (
        <DashboardContent
          data={state.value}
          quota={quota}
          quotaBusy={quotaBusy}
          quotaMessage={quotaMessage}
          onQuotaRefresh={onQuotaRefresh}
        />
      )}
    </>
  );
}

function DashboardContent({
  data,
  quota,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
}: {
  data: DashboardData;
  quota: LoadState<QuotaData>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
}) {
  const { summary, diagnostics } = data;
  if (summary.eventCount === 0) {
    return (
      <>
        {(diagnostics.status === "error" || diagnostics.status === "degraded") && (
          <Notice
            tone={diagnostics.status === "error" ? "error" : "warning"}
            title={diagnostics.message}
            body={diagnostics.advice}
          />
        )}
        <section className="empty-state">
          <div className="empty-mark">0</div>
          <p className="eyebrow">No usage yet</p>
          <h2>{diagnostics.filesSeen === 0 ? "No Codex sessions found" : "No token events in this range"}</h2>
          <p>
            {diagnostics.filesSeen === 0
              ? "Run Codex once, then refresh usage. Dora scans the local sessions and archived sessions folders."
              : "Choose a wider range or create a new Codex session."}
          </p>
        </section>
        <QuotaPanel
          state={quota}
          busy={quotaBusy}
          message={quotaMessage}
          onRefresh={onQuotaRefresh}
        />
      </>
    );
  }

  return (
    <>
      {diagnostics.status === "error" && (
        <Notice tone="warning" title={diagnostics.message} body={diagnostics.advice} />
      )}

      <section className="summary-grid">
        <article className="hero-metric panel">
          <p className="panel-label">Total tokens · {summary.range}</p>
          <strong>{formatCompact(summary.totalTokens)}</strong>
          <span>{formatNumber(summary.totalTokens)} exact tokens</span>
          <div className="hero-meta">
            <div>
              <small>Events</small>
              <b>{formatNumber(summary.eventCount)}</b>
            </div>
            <div>
              <small>Cache hit</small>
              <b>{formatPercent(summary.cacheHitRate)}</b>
            </div>
          </div>
        </article>

        <article className="token-mix panel">
          <div className="panel-heading">
            <div>
              <p className="panel-label">Token mix</p>
              <h2>Non-overlapping usage</h2>
            </div>
          </div>
          <div className="metric-list">
            <Metric label="Input" value={summary.inputTokens} color="input" />
            <Metric label="Cache read" value={summary.cachedInputTokens} color="cache" />
            <Metric label="Cache creation" value={summary.cacheCreationInputTokens} color="creation" />
            <Metric label="Output" value={summary.outputTokens} color="output" />
            <Metric label="Reasoning" value={summary.reasoningOutputTokens} color="reasoning" />
          </div>
        </article>
      </section>

      <section className="panel trend-panel">
        <div className="panel-heading">
          <div>
            <p className="panel-label">Daily trend</p>
            <h2>Where the tokens went</h2>
          </div>
          <TokenLegend />
        </div>
        <TimelineChart points={data.timeline} />
      </section>

      <section className="breakdown-grid">
        <BreakdownCard title="Models" eyebrow="Model distribution" items={data.models} />
        <BreakdownCard title="Projects" eyebrow="Project distribution" items={data.projects} />
      </section>

      <QuotaPanel
        state={quota}
        busy={quotaBusy}
        message={quotaMessage}
        onRefresh={onQuotaRefresh}
      />
    </>
  );
}

function Metric({ label, value, color }: { label: string; value: number; color: string }) {
  return (
    <div>
      <span className={`legend-dot ${color}`} aria-hidden="true" />
      <p>{label}</p>
      <strong>{formatCompact(value)}</strong>
      <small>{formatNumber(value)}</small>
    </div>
  );
}

function TimelineChart({ points }: { points: TimelinePoint[] }) {
  const max = Math.max(...points.map((point) => point.totalTokens), 1);
  return (
    <div className="chart-scroll">
      <div className="chart" aria-hidden="true">
        {points.map((point) => {
          const height = Math.max(10, Math.round((point.totalTokens / max) * 190));
          return (
            <div className="chart-column" key={point.date} title={`${point.date}: ${formatNumber(point.totalTokens)}`}>
              <div className="bar-value">{formatCompact(point.totalTokens)}</div>
              <div className="stacked-bar" style={{ height }}>
                <BarSegment value={point.reasoningOutputTokens} total={point.totalTokens} color="reasoning" />
                <BarSegment value={point.outputTokens} total={point.totalTokens} color="output" />
                <BarSegment value={point.cacheCreationInputTokens} total={point.totalTokens} color="creation" />
                <BarSegment value={point.cachedInputTokens} total={point.totalTokens} color="cache" />
                <BarSegment value={point.inputTokens} total={point.totalTokens} color="input" />
              </div>
              <time dateTime={point.date}>{point.date.slice(5)}</time>
            </div>
          );
        })}
      </div>
      <table className="sr-only">
        <caption>Daily token usage by token type</caption>
        <thead>
          <tr>
            <th>Date</th>
            <th>Total</th>
            <th>Input</th>
            <th>Cache read</th>
            <th>Cache creation</th>
            <th>Output</th>
            <th>Reasoning</th>
          </tr>
        </thead>
        <tbody>
          {points.map((point) => (
            <tr key={point.date}>
              <th>{point.date}</th>
              <td>{point.totalTokens}</td>
              <td>{point.inputTokens}</td>
              <td>{point.cachedInputTokens}</td>
              <td>{point.cacheCreationInputTokens}</td>
              <td>{point.outputTokens}</td>
              <td>{point.reasoningOutputTokens}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function BarSegment({ value, total, color }: { value: number; total: number; color: string }) {
  if (value <= 0 || total <= 0) return null;
  return <span className={color} style={{ height: `${(value / total) * 100}%` }} />;
}

function QuotaPanel({
  state,
  busy,
  message,
  onRefresh,
}: {
  state: LoadState<QuotaData>;
  busy: boolean;
  message: string;
  onRefresh: () => void;
}) {
  if (state.kind === "loading") {
    return <section className="panel quota-placeholder" aria-label="Loading Codex quota" />;
  }
  if (state.kind === "error") {
    return (
      <section className="panel quota-placeholder">
        <div>
          <p className="panel-label">Codex quota</p>
          <h2>Quota status is unavailable</h2>
          <p>{state.message} Local token analytics remains available.</p>
        </div>
        <span className="muted-badge error">Unavailable</span>
      </section>
    );
  }

  const value = state.value;
  if (!value.enabled) {
    return (
      <section className="panel quota-placeholder">
        <div>
          <p className="panel-label">Codex quota</p>
          <h2>Subscription limits are not enabled</h2>
          <p>Enable OAuth quota access in Diagnostics. Local token analytics stays independent.</p>
        </div>
        <span className="muted-badge">Not enabled</span>
      </section>
    );
  }

  return (
    <section className="panel quota-panel">
      <div className="panel-heading quota-heading">
        <div>
          <p className="panel-label">Codex quota</p>
          <h2>Subscription windows</h2>
          {value.items[0]?.accountLabel && (
            <p className="quota-account">
              {value.items[0].accountLabel}
              {value.items[0].plan ? ` · ${value.items[0].plan}` : ""}
            </p>
          )}
        </div>
        <button type="button" disabled={busy} onClick={onRefresh}>
          {busy ? "Refreshing…" : "Refresh quota"}
        </button>
      </div>

      {value.status !== "ready" && value.items.length > 0 && (
        <Notice tone="warning" title={value.message} body={value.advice} />
      )}
      {value.items.length === 0 ? (
        <div className="quota-empty">
          <strong>{value.message}</strong>
          <p>{value.advice || "Refresh when Codex OAuth is available."}</p>
        </div>
      ) : (
        <div className="quota-grid">
          {value.items.map((item) => <QuotaCard key={item.windowKey} item={item} />)}
        </div>
      )}
      {message && <p className="refresh-message quota-message" role="status">{message}</p>}
    </section>
  );
}

function QuotaCard({ item }: { item: QuotaItem }) {
  return (
    <article className="quota-card">
      <div className="quota-card-heading">
        <div>
          <span>{item.label}</span>
          {item.sourceState === "stale" && <small>Stale</small>}
        </div>
        <strong>{formatPercentValue(item.remainingPercent)} left</strong>
      </div>
      <div
        className="quota-progress"
        role="progressbar"
        aria-label={`${item.label} quota used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={item.usedPercent}
      >
        <span style={{ width: `${item.usedPercent}%` }} />
      </div>
      <div className="quota-meta">
        <span>{formatPercentValue(item.usedPercent)} used</span>
        <span>Resets {formatDate(item.resetsAt)}</span>
      </div>
    </article>
  );
}

function TokenLegend() {
  return (
    <div className="token-legend">
      {[
        ["Input", "input"],
        ["Cache", "cache"],
        ["Create", "creation"],
        ["Output", "output"],
        ["Reason", "reasoning"],
      ].map(([label, color]) => (
        <span key={label}><i className={color} />{label}</span>
      ))}
    </div>
  );
}

function BreakdownCard({ title, eyebrow, items }: { title: string; eyebrow: string; items: BreakdownItem[] }) {
  const maximum = Math.max(items[0]?.totalTokens ?? 0, 1);
  return (
    <article className="panel breakdown-card">
      <div className="panel-heading">
        <div>
          <p className="panel-label">{eyebrow}</p>
          <h2>{title}</h2>
        </div>
      </div>
      <div className="ranking-list">
        {items.slice(0, 6).map((item) => (
          <div key={item.name}>
            <div className="ranking-label">
              <span title={item.name}>{item.name}</span>
              <b>{formatCompact(item.totalTokens)}</b>
            </div>
            <div className="ranking-track">
              <span style={{ width: `${Math.max(3, (item.totalTokens / maximum) * 100)}%` }} />
            </div>
          </div>
        ))}
      </div>
    </article>
  );
}

type DiagnosticsProps = {
  state: LoadState<DashboardData>;
  health: LoadState<HealthStatus>;
  refreshing: "incremental" | "full" | null;
  refreshMessage: string;
  onScan: (full: boolean) => void;
  quota: LoadState<QuotaData>;
  settings: LoadState<DoraSettings>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
  onQuotaConsent: (enabled: boolean) => void;
};

function Diagnostics({
  state,
  health,
  refreshing,
  refreshMessage,
  onScan,
  quota,
  settings,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
  onQuotaConsent,
}: DiagnosticsProps) {
  const data = state.kind === "ready" ? state.value.diagnostics : null;
  const quotaData = quota.kind === "ready" ? quota.value : null;
  const quotaConsent = settings.kind === "ready" && settings.value.codexQuotaConsent;
  return (
    <>
      <section className="page-heading compact">
        <div>
          <p className="eyebrow">Diagnostics</p>
          <h1>Local data health.</h1>
          <p className="lede">A concise view of discovery, parsing and persistence.</p>
        </div>
      </section>

      {state.kind === "loading" && <DashboardSkeleton />}
      {state.kind === "error" && <Notice tone="error" title="Diagnostics unavailable" body={state.message} />}
      {data && (
        <>
          <section className="diagnostic-grid">
            <DiagnosticMetric label="Status" value={humanStatus(data.status)} tone={data.status} />
            <DiagnosticMetric label="Files seen" value={formatNumber(data.filesSeen)} />
            <DiagnosticMetric label="Stored events" value={formatNumber(data.storedEvents)} />
            <DiagnosticMetric label="Parser version" value={`v${data.parserVersion}`} />
          </section>

          <section className="panel diagnostic-detail">
            <div>
              <p className="panel-label">Codex source</p>
              <h2>Automatic local discovery</h2>
            </div>
            <dl>
              <div><dt>Directories</dt><dd>sessions + archived_sessions</dd></div>
              <div><dt>Last scan</dt><dd>{formatDate(data.lastScanAt)}</dd></div>
              <div><dt>Last mode</dt><dd>{data.lastRunMode || "Not scanned"}</dd></div>
              <div><dt>Last parsed</dt><dd>{formatNumber(data.eventsSeen)} records</dd></div>
              <div>
                <dt>Dora initialized</dt>
                <dd>{health.kind === "ready" ? formatDate(health.value.initializedAt) : "Unavailable"}</dd>
              </div>
            </dl>
            <div className="diagnostic-actions">
              <button type="button" disabled={refreshing !== null} onClick={() => onScan(false)}>
                {refreshing === "incremental" ? "Scanning…" : "Scan changes"}
              </button>
              <button className="secondary" type="button" disabled={refreshing !== null} onClick={() => onScan(true)}>
                {refreshing === "full" ? "Rebuilding…" : "Full rebuild"}
              </button>
            </div>
            {refreshMessage && <p className="refresh-message" role="status">{refreshMessage}</p>}
          </section>

          <Notice
            tone="neutral"
            title="Your conversations stay private"
            body="Dora stores token metadata and checkpoints only. Prompt text, replies, tool arguments and raw transcript lines are never copied into SQLite."
          />
        </>
      )}

      <section className="panel quota-settings">
        <div className="quota-settings-copy">
          <p className="panel-label">Codex subscription quota</p>
          <h2>Allow Dora to read quota</h2>
          <p>
            When enabled, Dora reads the local Codex OAuth session and contacts only ChatGPT's
            quota endpoint. Access tokens are never stored in SQLite, settings or logs.
          </p>
        </div>
        <label className="consent-toggle">
          <input
            type="checkbox"
            checked={quotaConsent}
            disabled={settings.kind !== "ready" || quotaBusy || health.kind !== "ready"}
            onChange={(event) => onQuotaConsent(event.target.checked)}
          />
          <span aria-hidden="true" />
          <b>{quotaConsent ? "Enabled" : "Disabled"}</b>
        </label>

        {settings.kind === "error" && (
          <Notice tone="error" title="Settings unavailable" body={settings.message} />
        )}
        {quota.kind === "error" && (
          <Notice tone="error" title="Quota status unavailable" body={quota.message} />
        )}
        {quotaData && quotaData.enabled && (
          <div className="quota-setting-status">
            <div>
              <span>Status</span>
              <strong>{humanQuotaStatus(quotaData.status)}</strong>
            </div>
            <div>
              <span>Last success</span>
              <strong>{formatDate(quotaData.lastSuccessAt)}</strong>
            </div>
            <button type="button" disabled={quotaBusy} onClick={onQuotaRefresh}>
              {quotaBusy ? "Refreshing…" : "Refresh now"}
            </button>
          </div>
        )}
        {quotaData && quotaData.status !== "ready" && (
          <p className="quota-advice">{quotaData.message}{quotaData.advice ? ` · ${quotaData.advice}` : ""}</p>
        )}
        {quotaMessage && <p className="refresh-message" role="status">{quotaMessage}</p>}
      </section>
    </>
  );
}

function DiagnosticMetric({ label, value, tone = "" }: { label: string; value: string; tone?: string }) {
  return (
    <article className="panel diagnostic-metric">
      <p className="panel-label">{label}</p>
      <strong className={tone}>{value}</strong>
    </article>
  );
}

function Notice({ tone, title, body }: { tone: "error" | "warning" | "neutral"; title: string; body: string }) {
  return (
    <aside className={`notice ${tone}`} role={tone === "error" ? "alert" : "status"}>
      <span aria-hidden="true">i</span>
      <div><strong>{title}</strong><p>{body}</p></div>
    </aside>
  );
}

function DashboardSkeleton() {
  return (
    <div className="skeleton-grid" aria-label="Loading usage">
      <div className="skeleton tall" />
      <div className="skeleton tall" />
      <div className="skeleton wide" />
    </div>
  );
}

function RefreshIcon({ spinning }: { spinning: boolean }) {
  return <span className={`refresh-icon ${spinning ? "spinning" : ""}`} aria-hidden="true">↻</span>;
}

function humanStatus(status: string) {
  const labels: Record<string, string> = {
    ready: "Ready",
    degraded: "Ready with warnings",
    error: "Needs attention",
    not_scanned: "Not scanned",
  };
  return labels[status] ?? status;
}

function humanQuotaStatus(status: string) {
  const labels: Record<string, string> = {
    ready: "Ready",
    error: "Last refresh failed",
    unsupported: "Unsupported auth",
    not_configured: "Login required",
  };
  return labels[status] ?? status;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat().format(value);
}

function formatCompact(value: number) {
  return new Intl.NumberFormat("en-US", {
    notation: value >= 10_000 ? "compact" : "standard",
    maximumFractionDigits: 1,
  }).format(value);
}

function formatPercent(value: number) {
  return new Intl.NumberFormat(undefined, {
    style: "percent",
    maximumFractionDigits: 1,
  }).format(value);
}

function formatPercentValue(value: number) {
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 1,
  }).format(value) + "%";
}

function formatDate(value: string | null) {
  if (!value) return "Not yet";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Unknown error";
}

export default App;
