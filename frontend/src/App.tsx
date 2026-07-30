import { useEffect, useState } from "react";

type HealthStatus = {
  backend: boolean;
  sqlite: boolean;
  initializedAt: string;
};

type LoadState =
  | { kind: "loading" }
  | { kind: "ready"; status: HealthStatus }
  | { kind: "error"; message: string };

function App() {
  const [state, setState] = useState<LoadState>({ kind: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    async function loadHealth() {
      try {
        const response = await fetch("/api/v1/health", {
          signal: controller.signal,
          cache: "no-store",
        });
        if (!response.ok) {
          throw new Error(`status ${response.status}`);
        }

        const status = (await response.json()) as HealthStatus;
        setState({ kind: "ready", status });
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        const message = error instanceof Error ? error.message : "unknown error";
        setState({ kind: "error", message });
      }
    }

    void loadHealth();
    return () => controller.abort();
  }, []);

  return (
    <main className="page-shell">
      <section className="status-card" aria-live="polite">
        <div className="brand-mark" aria-hidden="true">
          D
        </div>

        {state.kind === "loading" && (
          <>
            <p className="eyebrow">Local status</p>
            <h1>Checking Dora…</h1>
            <p className="supporting-copy">Connecting to the local backend.</p>
          </>
        )}

        {state.kind === "error" && (
          <>
            <p className="eyebrow error-label">Connection failed</p>
            <h1>Dora is unavailable</h1>
            <p className="supporting-copy">
              Start the backend and refresh this page. ({state.message})
            </p>
          </>
        )}

        {state.kind === "ready" && (
          <>
            <p className="eyebrow">Local status</p>
            <h1>{state.status.backend ? "Dora is running" : "Dora is unavailable"}</h1>

            <dl className="status-list">
              <div>
                <dt>
                  <span className={state.status.backend ? "status-dot ready" : "status-dot"} />
                  Backend
                </dt>
                <dd>{state.status.backend ? "Backend connected" : "Backend unavailable"}</dd>
              </div>
              <div>
                <dt>
                  <span className={state.status.sqlite ? "status-dot ready" : "status-dot"} />
                  Storage
                </dt>
                <dd>{state.status.sqlite ? "SQLite ready" : "SQLite unavailable"}</dd>
              </div>
            </dl>

            <div className="initialized-at">
              <span>Initialized</span>
              <time dateTime={state.status.initializedAt}>
                {formatInitializedAt(state.status.initializedAt)}
              </time>
            </div>
          </>
        )}
      </section>
    </main>
  );
}

function formatInitializedAt(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export default App;
