package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

type managementRegistrationResponse struct {
	Resources []managementResource `json:"resources,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method  string              `json:"Method"`
	Path    string              `json:"Path"`
	Headers http.Header         `json:"Headers"`
	Query   map[string][]string `json:"Query"`
	Body    []byte              `json:"Body"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type dashboardPageData struct {
	Snapshot       dashboardSnapshot
	Uptime         string
	HitRate        string
	HitRatePercent float64
	Started        string
	Updated        string
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Snapshot.Plugin.Name}} - Runtime</title>
  <style>
    :root {
      color-scheme: dark;
      --canvas: #101416;
      --surface: #171d20;
      --surface-raised: #20282b;
      --line: #2d383b;
      --ink: #f2f5f3;
      --muted: #a5b0af;
      --teal: #73d6c2;
      --teal-soft: #193d3a;
      --amber: #edbd72;
      --red: #ef8f89;
      --shadow: 0 18px 45px rgba(0, 0, 0, .18);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-width: 320px;
      background: var(--canvas);
      color: var(--ink);
      font: 15px/1.55 ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      letter-spacing: 0;
    }
    a { color: var(--teal); text-underline-offset: 3px; }
    a:focus-visible { outline: 2px solid var(--amber); outline-offset: 4px; }
    .shell { width: min(1180px, calc(100% - 40px)); margin: 0 auto; padding: 48px 0 56px; }
    .topbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; padding-bottom: 34px; border-bottom: 1px solid var(--line); }
    .eyebrow { margin: 0 0 8px; color: var(--teal); font-size: 12px; font-weight: 700; letter-spacing: .12em; text-transform: uppercase; }
    h1 { margin: 0; font-size: clamp(28px, 4vw, 46px); line-height: 1.08; letter-spacing: 0; }
    .subtitle { max-width: 620px; margin: 14px 0 0; color: var(--muted); }
    .status { display: inline-flex; align-items: center; gap: 9px; flex: 0 0 auto; padding: 9px 13px; border: 1px solid rgba(115, 214, 194, .35); border-radius: 999px; background: var(--teal-soft); color: var(--teal); font-size: 13px; font-weight: 700; }
    .status::before { width: 8px; height: 8px; border-radius: 50%; background: currentColor; content: ""; box-shadow: 0 0 0 4px rgba(115, 214, 194, .12); }
    .section { padding-top: 34px; }
    .section-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 20px; margin-bottom: 14px; }
    h2 { margin: 0; color: var(--ink); font-size: 16px; letter-spacing: .02em; }
    .section-note { margin: 0; color: var(--muted); font-size: 13px; }
    .grid { display: grid; gap: 12px; grid-template-columns: repeat(4, minmax(0, 1fr)); }
    .grid-wide { grid-template-columns: repeat(3, minmax(0, 1fr)); }
    .panel { min-width: 0; padding: 20px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); box-shadow: var(--shadow); }
    .metric-label { color: var(--muted); font-size: 13px; }
    .metric-value { display: block; margin-top: 8px; color: var(--ink); font-size: 30px; font-weight: 700; line-height: 1; }
    .metric-value.teal { color: var(--teal); }
    .metric-value.amber { color: var(--amber); }
    .metric-value.red { color: var(--red); }
    .meter { display: block; width: 100%; height: 6px; margin-top: 15px; overflow: hidden; border: 0; border-radius: 99px; background: var(--line); appearance: none; }
    .meter::-webkit-meter-bar { border: 0; border-radius: 99px; background: var(--line); }
    .meter::-webkit-meter-optimum-value { border-radius: 99px; background: var(--teal); }
    .meter::-moz-meter-bar { border-radius: 99px; background: var(--teal); }
    dl { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px 20px; margin: 0; }
    dt { color: var(--muted); }
    dd { margin: 0; color: var(--ink); font-variant-numeric: tabular-nums; text-align: right; }
    .footer { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 12px 20px; margin-top: 42px; padding-top: 18px; border-top: 1px solid var(--line); color: var(--muted); font-size: 13px; }
    .footer span { font-variant-numeric: tabular-nums; }
    @media (max-width: 820px) { .grid, .grid-wide { grid-template-columns: repeat(2, minmax(0, 1fr)); } .topbar { flex-direction: column; } }
    @media (max-width: 520px) { .shell { width: min(100% - 24px, 1180px); padding-top: 28px; } .grid, .grid-wide { grid-template-columns: 1fr; } .panel { padding: 17px; } .section-heading { align-items: flex-start; flex-direction: column; gap: 4px; } }
    @media (prefers-reduced-motion: reduce) { * { scroll-behavior: auto !important; } }
  </style>
</head>
<body>
  <main class="shell">
    <header class="topbar">
      <div>
        <p class="eyebrow">CPA Plugin Runtime</p>
        <h1>{{.Snapshot.Plugin.Name}}</h1>
        <p class="subtitle">Operational view of reasoning continuity across OpenAI, Claude, and Gemini protocol turns. The page contains aggregate counters only.</p>
      </div>
      <div class="status">{{.Snapshot.Status}}</div>
    </header>

    <section class="section" aria-labelledby="overview-heading">
      <div class="section-heading"><h2 id="overview-heading">Overview</h2><p class="section-note">Updated {{.Updated}}</p></div>
      <div class="grid">
        <article class="panel"><span class="metric-label">Runtime</span><strong class="metric-value teal">{{.Uptime}}</strong></article>
        <article class="panel"><span class="metric-label">Exact cache hit rate</span><strong class="metric-value teal">{{.HitRate}}</strong><meter class="meter" min="0" max="100" value="{{.HitRatePercent}}" aria-label="Exact cache hit rate"></meter></article>
        <article class="panel"><span class="metric-label">Active streams</span><strong class="metric-value">{{.Snapshot.Cache.ActiveStreams}}</strong></article>
        <article class="panel"><span class="metric-label">Reasoning entries</span><strong class="metric-value">{{.Snapshot.Cache.ReasoningEntries}}</strong></article>
      </div>
    </section>

    <section class="section" aria-labelledby="traffic-heading">
      <div class="section-heading"><h2 id="traffic-heading">Traffic & restoration</h2><p class="section-note">Cumulative since plugin start</p></div>
      <div class="grid grid-wide">
        <article class="panel"><dl>
          <dt>OpenAI requests</dt><dd>{{.Snapshot.Requests.OpenAI}}</dd>
          <dt>Claude requests</dt><dd>{{.Snapshot.Requests.Claude}}</dd>
          <dt>Gemini requests</dt><dd>{{.Snapshot.Requests.Gemini}}</dd>
          <dt>Stream chunks</dt><dd>{{.Snapshot.Requests.StreamChunks}}</dd>
          <dt>Skipped non-target</dt><dd>{{.Snapshot.Requests.SkippedNonTarget}}</dd>
        </dl></article>
        <article class="panel"><dl>
          <dt>Exact cache hits</dt><dd>{{.Snapshot.Restoration.ExactCacheHits}}</dd>
          <dt>Cache misses</dt><dd>{{.Snapshot.Restoration.CacheMisses}}</dd>
          <dt>Repaired messages</dt><dd>{{.Snapshot.Restoration.RepairedMessages}}</dd>
          <dt>Content fallback</dt><dd>{{.Snapshot.Restoration.ContentFallbacks}}</dd>
          <dt>Placeholder fallback</dt><dd>{{.Snapshot.Restoration.PlaceholderFallbacks}}</dd>
          <dt>Passthrough</dt><dd>{{.Snapshot.Restoration.PassthroughFallbacks}}</dd>
        </dl></article>
        <article class="panel"><dl>
          <dt>Reasoning writes</dt><dd>{{.Snapshot.Capture.ReasoningWrites}}</dd>
          <dt>Completed streams</dt><dd>{{.Snapshot.Capture.CompletedStreams}}</dd>
          <dt>Truncated stream reasoning</dt><dd>{{.Snapshot.Capture.TruncatedStreamReasoning}}</dd>
          <dt>Malformed payloads</dt><dd>{{.Snapshot.Errors.MalformedPayloads}}</dd>
          <dt>Missing tool-call IDs</dt><dd>{{.Snapshot.Errors.MissingToolCallIDs}}</dd>
          <dt>Missing stream IDs</dt><dd>{{.Snapshot.Errors.MissingStreamIDs}}</dd>
          <dt>Recovered plugin panics</dt><dd>{{.Snapshot.Errors.RecoveredPanics}}</dd>
        </dl></article>
      </div>
    </section>

    <section class="section" aria-labelledby="runtime-heading">
      <div class="section-heading"><h2 id="runtime-heading">Runtime details</h2><p class="section-note">Privacy-safe configuration summary</p></div>
      <div class="grid grid-wide">
        <article class="panel"><dl>
          <dt>Cache entries</dt><dd>{{.Snapshot.Cache.Capacity}}</dd>
          <dt>Cache byte capacity</dt><dd>{{.Snapshot.Cache.ByteCapacity}}</dd>
          <dt>Retained reasoning bytes</dt><dd>{{.Snapshot.Cache.ReasoningBytes}}</dd>
          <dt>Active stream bytes</dt><dd>{{.Snapshot.Cache.ActiveStreamBytes}}</dd>
          <dt>Cache TTL</dt><dd>{{.Snapshot.Cache.TTL}}</dd>
          <dt>Expired entries</dt><dd>{{.Snapshot.Cache.ExpiredEntries}}</dd>
          <dt>Evicted entries</dt><dd>{{.Snapshot.Cache.EvictedEntries}}</dd>
          <dt>Rejected oversize entries</dt><dd>{{.Snapshot.Cache.RejectedOversize}}</dd>
          <dt>Expired streams</dt><dd>{{.Snapshot.Cache.ExpiredStreams}}</dd>
          <dt>Evicted streams</dt><dd>{{.Snapshot.Cache.EvictedStreams}}</dd>
        </dl></article>
        <article class="panel"><dl>
          <dt>Plugin version</dt><dd>{{.Snapshot.Plugin.Version}}</dd>
          <dt>Started</dt><dd>{{.Started}}</dd>
          <dt>Fallback strategy</dt><dd>{{.Snapshot.Configuration.FallbackStrategy}}</dd>
          <dt>Target patterns</dt><dd>{{.Snapshot.Configuration.TargetModelCount}}</dd>
        </dl></article>
        <article class="panel"><dl>
          <dt>Reasoning exposed</dt><dd>No</dd>
          <dt>Tool-call IDs exposed</dt><dd>No</dd>
          <dt>Request bodies exposed</dt><dd>No</dd>
          <dt>Update mode</dt><dd>Manual pull</dd>
        </dl></article>
      </div>
    </section>

    <footer class="footer"><span>Started {{.Started}}</span><span><a href="?format=json">JSON status</a> | {{.Snapshot.Plugin.ID}}</span></footer>
  </main>
</body>
</html>`))

func managementRegistration() ([]byte, error) {
	response := managementRegistrationResponse{Resources: []managementResource{{
		Path:        "/status",
		Menu:        pluginName,
		Description: "Privacy-safe runtime dashboard for reasoning continuity and cache health.",
	}}}
	encoded, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode management registration: %w", errMarshal)
	}
	return okEnvelopeJSON(string(encoded))
}

func handleManagementMethod(requestBytes []byte) ([]byte, error) {
	var request managementRequest
	if errUnmarshal := json.Unmarshal(requestBytes, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(request.Method), http.MethodGet) {
		return marshalManagementResponse(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"Allow": []string{http.MethodGet}},
			Body:       []byte("method not allowed"),
		})
	}

	query := make(map[string][]string, len(request.Query))
	for key, values := range request.Query {
		query[key] = append([]string(nil), values...)
	}
	accept := strings.ToLower(request.Headers.Get("Accept"))
	wantsJSON := strings.Contains(accept, "application/json")
	if values := query["format"]; len(values) > 0 && strings.EqualFold(values[0], "json") {
		wantsJSON = true
	}

	snapshot := currentDashboardSnapshot(time.Now())
	if wantsJSON {
		body, errMarshal := json.MarshalIndent(snapshot, "", "  ")
		if errMarshal != nil {
			return nil, fmt.Errorf("encode dashboard status: %w", errMarshal)
		}
		return marshalManagementResponse(managementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"application/json; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: body,
		})
	}

	page := dashboardPageData{
		Snapshot:       snapshot,
		Uptime:         formatUptime(snapshot.UptimeSeconds),
		HitRate:        fmt.Sprintf("%.1f%%", snapshot.Restoration.HitRate*100),
		HitRatePercent: snapshot.Restoration.HitRate * 100,
		Started:        snapshot.StartedAt.Format(time.RFC3339),
		Updated:        snapshot.GeneratedAt.Format(time.RFC3339),
	}
	var body strings.Builder
	if errExecute := dashboardTemplate.Execute(&body, page); errExecute != nil {
		return nil, fmt.Errorf("render dashboard: %w", errExecute)
	}
	return marshalManagementResponse(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Cache-Control":           []string{"no-store"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'"},
		},
		Body: []byte(body.String()),
	})
}

func marshalManagementResponse(response managementResponse) ([]byte, error) {
	encoded, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode management response: %w", errMarshal)
	}
	return okEnvelopeJSON(string(encoded))
}

func formatUptime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	duration := time.Duration(seconds) * time.Second
	days := duration / (24 * time.Hour)
	duration %= 24 * time.Hour
	hours := duration / time.Hour
	duration %= time.Hour
	minutes := duration / time.Minute
	return fmt.Sprintf("%dd %02dh %02dm", days, hours, minutes)
}
