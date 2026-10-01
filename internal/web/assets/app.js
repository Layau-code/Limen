(function () {
  "use strict";

  const fixtures = window.LimenFixtures;
  const state = {
    token: sessionStorage.getItem("limen_ui_token") || "",
    live: false,
    liveModels: null,
    liveRunDetails: {},
    liveDecisions: {},
    lastError: ""
  };

  const root = document.getElementById("app");

  function escapeHTML(value) {
    return String(value == null ? "" : value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function badge(text, tone) {
    return `<span class="badge ${tone || ""}">${escapeHTML(text)}</span>`;
  }

  function dot(tone) {
    return `<span class="status-dot ${tone === "warning" ? "warning" : tone === "danger" ? "danger" : ""}"></span>`;
  }

  function money(value) {
    return escapeHTML(value);
  }

  function usdFromNano(value) {
    const numeric = Number(value);
    return Number.isFinite(numeric) ? `$${(numeric / 1000000000).toFixed(4)}` : "—";
  }

  function relativeDeadline(value) {
    if (!value) return "not set";
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return value;
    const minutes = Math.round((timestamp - Date.now()) / 60000);
    if (minutes >= 0) return `${minutes} 分钟后`;
    return `${Math.abs(minutes)} 分钟前`;
  }

  function navItems() {
    return [
      ["overview", "Overview", "⌂"],
      ["runs", "Runs", "◷"],
      ["decisions", "Decisions", "◇"],
      ["models", "Models", "▦"],
      ["usage", "Usage", "↗"],
      ["settings", "Settings", "⚙"]
    ];
  }

  function currentRoute() {
    const value = location.hash.replace(/^#\/?/, "");
    return value || "overview";
  }

  function routeName(route) {
    return route.split("/")[0] || "overview";
  }

  function renderShell() {
    root.innerHTML = `
      <div class="app-shell">
        <aside class="sidebar">
          <a class="brand" href="#/overview" aria-label="Limen Overview">
            <span class="brand-mark">L</span>
            <span>Limen<small>Observability</small></span>
          </a>
          <nav class="nav" aria-label="Primary navigation">
            <div class="nav-label">Workspace</div>
            ${navItems().map(([id, label, icon]) => `<a class="nav-link" data-nav="${id}" href="#/${id}"><span class="nav-icon">${icon}</span><span>${label}</span></a>`).join("")}
          </nav>
          <div class="sidebar-footer">
            <div class="environment"><span><span class="environment-dot"></span>Production</span><span class="small">v1.0.0</span></div>
          </div>
        </aside>
        <main class="main">
          <header class="topbar">
            <div class="breadcrumb"><span>Limen</span><span aria-hidden="true"> / </span><strong id="breadcrumb-current">Overview</strong></div>
            <div class="topbar-actions">
              <span class="range-control">▣&nbsp; Past 7 days</span>
              <button class="connection-control" data-action="connect"><span class="connection-dot"></span><span id="connection-label">Demo data</span></button>
            </div>
          </header>
          <div id="view"></div>
        </main>
      </div>`;
  }

  function pageHeading(eyebrow, title, subtitle, action) {
    return `<div class="page-heading"><div><div class="eyebrow">${escapeHTML(eyebrow)}</div><h1>${escapeHTML(title)}</h1>${subtitle ? `<p class="subtitle">${escapeHTML(subtitle)}</p>` : ""}</div>${action || `<div class="updated">Updated ${escapeHTML(fixtures.overview.updated)}</div>`}</div>`;
  }

  function demoBanner() {
    const route = currentRoute();
    const pageIsLive = route === "models" || (route.startsWith("runs/") && Boolean(state.liveRunDetails[route.split("/")[1]])) || (route.startsWith("decisions/") && Boolean(state.liveDecisions[route.split("/")[1]]));
    if (state.live && pageIsLive) return "";
    if (state.live) {
      return `<div class="demo-banner"><span><strong>Partial live data</strong> · Models are connected; this surface still uses safe fixture data because no aggregate endpoint is available.</span><button data-action="disconnect">Disconnect</button></div>`;
    }
    if (state.token && state.lastError) {
      return `<div class="notice danger"><span>!</span><span><strong>Live API unavailable.</strong> ${escapeHTML(state.lastError)} · Showing safe demo data until the connection is restored.</span><button class="button" data-action="connect">Reconnect</button></div>`;
    }
    return `<div class="demo-banner"><span><strong>Demo data</strong> · Connect a Limen API key to load live decisions, Runs and models.</span><button data-action="connect">Connect API</button></div>`;
  }

  function svgTrend(values, budget) {
    const width = 760;
    const height = 190;
    const pad = { left: 30, right: 10, top: 14, bottom: 27 };
    const max = Math.max(100, ...values, ...(budget || []));
    const x = i => pad.left + (i * (width - pad.left - pad.right)) / (values.length - 1);
    const y = value => pad.top + (height - pad.top - pad.bottom) * (1 - value / max);
    const pathFor = arr => arr.map((value, index) => `${index ? "L" : "M"}${x(index).toFixed(1)},${y(value).toFixed(1)}`).join(" ");
    const grid = [25, 50, 75, 100].map(mark => `<line x1="${pad.left}" x2="${width - pad.right}" y1="${y(mark)}" y2="${y(mark)}" />`).join("");
    const labels = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((label, index) => `<text class="chart-axis" x="${x(Math.min(index * 2, values.length - 1))}" y="${height - 7}" text-anchor="middle">${label}</text>`).join("");
    const point = values.map((value, index) => `<circle class="chart-point" cx="${x(index)}" cy="${y(value)}" r="2.5" />`).join("");
    return `<svg class="chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Spend and budget trend"><g class="chart-grid">${grid}</g><path class="chart-budget" d="${pathFor(budget || [])}"/><path class="chart-line" d="${pathFor(values)}"/>${point}${labels}</svg>`;
  }

  function renderOverview() {
    const data = fixtures.overview;
    return `<div class="page">
      ${demoBanner()}
      ${pageHeading("Executive overview", "AI Gateway Overview", "成本、预算、可靠性与模型决策概览")}
      <section class="kpi-grid">${data.kpis.map(kpi => `<article class="card kpi clickable" data-route="${kpi.label === "Budget health" ? "runs" : kpi.label === "Auto-routing saving" ? "usage" : "usage"}"><div class="kpi-label">${escapeHTML(kpi.label)}</div><div class="kpi-value">${escapeHTML(kpi.value)}</div>${kpi.progress ? `<div class="progress"><span style="width:${kpi.progress}%"></span></div>` : ""}<div class="kpi-foot"><span class="${kpi.tone === "up" ? "trend-up" : kpi.tone === "down" ? "trend-down" : "trend-neutral"}">${escapeHTML(kpi.foot)}</span><span>→</span></div></article>`).join("")}</section>
      <section class="section-grid cols-2">
        <article class="card"><div class="card-header"><h2>Spend &amp; budget trend</h2><span class="updated">${escapeHTML(data.period)}</span></div><div class="card-body">${svgTrend(data.trend, data.budget)}<div class="legend"><span><i></i>Actual cost</span><span><i class="dashed"></i>Budget pace</span></div></div></article>
        <article class="card"><div class="card-header"><h2>Reliability</h2><span class="updated">Live summary</span></div><div class="card-body"><div class="metric-list">${data.reliability.map(metric => `<div class="metric-row"><span class="metric-row-label">${escapeHTML(metric.label)}</span><strong class="metric-row-value">${escapeHTML(metric.value)}</strong><div class="metric-row-bar"><span style="width:${metric.progress}%;${metric.tone === "warning" ? "background:#a47732" : ""}"></span></div></div>`).join("")}</div></div></article>
      </section>
      <section class="section-grid cols-2">
        <article class="card"><div class="card-header"><h2>Model decision mix</h2><span class="updated">71% model=auto</span></div><div class="card-body"><div class="metric-list">${data.decisions.map(item => `<div class="metric-row"><span class="metric-row-label">${escapeHTML(item.label)}</span><strong class="metric-row-value">${item.value}%</strong><div class="metric-row-bar"><span style="width:${item.value}%;"></span></div></div>`).join("")}</div><p class="small muted" style="margin-top:16px">策略按 Run 软预算和能力契约动态确定。</p></div></article>
        <article class="card"><div class="card-header"><h2>Provider health</h2><a class="small muted" href="#/models">查看模型 →</a></div><div class="card-body"><div class="health-list">${data.providers.map(provider => `<div class="health-row"><div><div class="health-name">${dot(provider.tone)}${escapeHTML(provider.name)}</div><div class="health-meta">${escapeHTML(provider.status)}</div></div><span class="health-stat">${escapeHTML(provider.rate)}</span><span class="health-stat">${escapeHTML(provider.latency)}</span></div>`).join("")}</div></div></article>
      </section>
      <section class="card"><div class="card-header"><h2>Requires attention</h2><span class="updated">按风险优先</span></div><div class="card-body table-scroll"><table class="attention-table"><thead><tr><th>Severity</th><th>Run / subject</th><th>Problem</th><th>Impact</th><th>Updated</th></tr></thead><tbody>${data.attention.map(item => `<tr class="clickable" data-href="${item.route}"><td>${badge(item.severity, item.tone)}</td><td class="primary-cell">${escapeHTML(item.subject)}</td><td>${escapeHTML(item.problem)}</td><td>${escapeHTML(item.impact)}</td><td>${escapeHTML(item.updated)}</td></tr>`).join("")}</tbody></table></div></section>
    </div>`;
  }

  function stateBadge(value, tone) {
    const labels = { active: "Active", completed: "Completed", suspended_accounting: "Accounting uncertain", completing: "Completing", cancelled: "Cancelled", deadline_exceeded: "Deadline exceeded", soft_budget_exhausted: "Budget exhausted" };
    if (value !== "suspended_accounting" && tone === "danger") tone = "";
    return badge(labels[value] || value, tone || (value === "suspended_accounting" ? "danger" : value === "active" ? "info" : "success"));
  }

  function renderRuns() {
    const rows = fixtures.runs;
    return `<div class="page">${demoBanner()}${pageHeading("Governed workloads", "Runs", "按预算、可靠性和结算状态查看 Agent 工作流")}
      <article class="card"><div class="card-body"><div class="table-toolbar"><div class="tabs"><button class="tab is-active">All</button><button class="tab">Active</button><button class="tab">Budget risk</button><button class="tab">Fallbacking</button><button class="tab">Uncertain</button><button class="tab">Completed</button></div><div class="filters"><input class="input" placeholder="Search Run or tenant" aria-label="Search Run or tenant"/><select class="select" aria-label="Filter status"><option>All statuses</option><option>Active</option><option>Accounting uncertain</option></select></div></div><div class="table-scroll"><table class="data-table"><thead><tr><th>Run</th><th>Tenant</th><th>Status</th><th>Budget</th><th>Requests</th><th>Fallback</th><th>Success</th><th>Last activity</th></tr></thead><tbody>${rows.map(row => `<tr class="clickable" data-href="#/runs/${row.id}"><td class="primary-cell"><div>${escapeHTML(row.name)}</div><div class="mono muted">${escapeHTML(row.id)}</div></td><td>${escapeHTML(row.tenant)}</td><td>${stateBadge(row.state, row.tone)}</td><td><div>${escapeHTML(row.budget)}</div><div class="progress"><span style="width:${row.budgetProgress}%;${row.tone === "danger" ? "background:#a6534b" : row.tone === "warning" ? "background:#a47732" : ""}"></span></div></td><td>${escapeHTML(row.requests)}</td><td>${escapeHTML(row.fallback)}</td><td>${escapeHTML(row.success)}</td><td>${escapeHTML(row.updated)}</td></tr>`).join("")}</tbody></table></div></div></article></div>`;
  }

  function renderRunDetail(runID) {
    const run = state.liveRunDetails[runID] || fixtures.runDetails[runID] || fixtures.runDetails.run_8FA2;
    const requestRows = run.requestRows || [];
    const action = run.state === "active" ? `<button class="button danger" data-action="cancel">Cancel Run</button>` : run.state === "suspended_accounting" ? `<button class="button" data-action="resolve-accounting">Resolve accounting</button>` : "";
    const accountingNotice = run.state === "suspended_accounting" ? `<div class="notice warning" style="margin-bottom:16px"><span>!</span><span><strong>Accounting uncertain.</strong> This Run may have incurred provider cost that Limen cannot currently confirm. New requests are blocked until resolved.</span></div>` : "";
    const usedPercent = Number.isFinite(Number(run.budgetProgress)) ? Number(run.budgetProgress).toFixed(1) : "—";
    return `<div class="page">${demoBanner()}${pageHeading("Runs / " + runID, run.name, `${run.tenant} · Created ${run.created} · Deadline in ${run.deadline}`, `<div class="detail-actions">${action}</div>`)}
      ${accountingNotice}
      <section class="stat-strip">${[["Confirmed cost", run.cost], ["Soft budget", `${run.budget} · ${usedPercent}% used`], ["In flight", run.inflight], ["Requests", run.requests]].map(item => `<div class="stat-strip-item"><div class="stat-strip-label">${escapeHTML(item[0])}</div><div class="stat-strip-value">${escapeHTML(item[1])}</div></div>`).join("")}</section>
      <section class="detail-grid"><div class="stack"><article class="card"><div class="card-header"><h2>Run timeline</h2><span class="updated">Config ${escapeHTML(run.config)} · ${escapeHTML(run.algorithm)}</span></div><div class="card-body"><div class="timeline">${run.timeline.map(item => `<div class="timeline-item"><span class="timeline-dot ${item.tone || ""}"></span><div class="timeline-meta"><span>${escapeHTML(item.time)}</span>${item.title.startsWith("req_") ? badge("Request", "dark") : ""}</div><div class="timeline-title">${escapeHTML(item.title)}</div><div class="timeline-description">${escapeHTML(item.description)}</div></div>`).join("")}</div></div></article><article class="card"><div class="card-header"><h2>Request evidence</h2><span class="updated">${run.requests}</span></div><div class="card-body table-scroll"><table class="data-table"><thead><tr><th>Status</th><th>Request</th><th>Contract</th><th>Decision</th><th>Cost</th><th>Latency</th></tr></thead><tbody>${requestRows.map(request => `<tr class="clickable" data-href="#/decisions/dec_7F31"><td>${badge(request.state, request.tone)}</td><td class="primary-cell mono">${escapeHTML(request.id)}</td><td>${escapeHTML(request.contract)}</td><td>${escapeHTML(request.decision)}</td><td>${escapeHTML(request.cost)}</td><td>${escapeHTML(request.latency)}</td></tr>`).join("")}</tbody></table></div></article></div><aside class="stack"><article class="card"><div class="card-header"><h2>Budget &amp; cost</h2></div><div class="card-body"><div class="key-value-list"><div class="key-value"><span>Confirmed</span><strong>${money(run.cost)}</strong></div><div class="key-value"><span>Reserved / in-flight</span><strong>${money(run.reserved)}</strong></div><div class="key-value"><span>Uncertain</span><strong class="trend-down">${money(run.uncertain)}</strong></div><div class="key-value"><span>Remaining</span><strong>${money(run.remaining)}</strong></div></div><div class="progress" style="margin-top:18px"><span style="width:74.2%"></span></div><p class="small muted" style="margin-top:9px">Soft budget：已准入请求允许正常完成。</p></div></article><article class="card"><div class="card-header"><h2>Run health</h2></div><div class="card-body"><div class="key-value-list"><div class="key-value"><span>Status</span><strong>${stateBadge(run.state, "danger")}</strong></div><div class="key-value"><span>Fallbacks</span><strong>${escapeHTML(run.fallback)}</strong></div><div class="key-value"><span>Failed requests</span><strong>${escapeHTML(run.failed)}</strong></div><div class="key-value"><span>Accounting</span><strong>${escapeHTML(run.accounting)}</strong></div><div class="key-value"><span>Deadline risk</span><strong class="trend-up">${escapeHTML(run.deadlineRisk)}</strong></div></div></div></article></aside></section></div>`;
  }

  function renderDecision(decisionID, replayMode) {
    const decision = state.liveDecisions[decisionID] || fixtures.decisions[decisionID] || fixtures.decisions.dec_7F31;
    const replay = decision.replay;
    const replayAvailable = replay && !replay.unavailable;
    const replayStatus = replayAvailable ? (replay.match ? badge("Same plan", "success") : badge("Plan changed", "warning")) : badge("Unavailable", "danger");
    return `<div class="page">${demoBanner()}${pageHeading("Decisions / " + decision.id, "Decision evidence", `Run ${decision.run} · Request ${decision.request} · ${decision.time}`, `<div class="detail-actions"><button class="button" data-action="replay">Replay</button><button class="button" data-action="compare">Compare config</button></div>`)}
      <div class="decision-header" style="margin-bottom:16px"><div class="decision-meta"><span>${badge(decision.plan.length ? "Decision accepted" : "No eligible target", decision.plan.length ? "success" : "danger")}</span><span>Algorithm ${escapeHTML(decision.algorithm)}</span><span>Config ${escapeHTML(decision.config)}</span><span>Snapshot ${escapeHTML(decision.inputHash)}</span><span>Plan ${escapeHTML(decision.planHash)}</span></div><span class="updated">Deterministic</span></div>
      <section class="decision-grid"><article class="decision-panel"><h3>Request contract</h3><div class="key-value-list"><div class="key-value"><span>Model</span><strong>${escapeHTML(decision.model)}</strong></div><div class="key-value"><span>Quality</span><strong>${escapeHTML(decision.quality)}</strong></div><div class="key-value"><span>Context</span><strong>${escapeHTML(decision.context)}</strong></div><div class="key-value"><span>Data class</span><strong>${escapeHTML(decision.dataClass)}</strong></div><div class="key-value"><span>Strategy</span><strong>${escapeHTML(decision.strategy)}</strong></div></div><div class="tag-list" style="margin-top:15px">${decision.capabilities.map(cap => badge(cap, "dark")).join("")}</div></article><article class="decision-panel"><h3>Selected plan</h3>${decision.plan.map((target, index) => `<div class="plan-step"><span class="plan-index">${index + 1}</span><span>${escapeHTML(target)}</span>${index === 0 ? badge("selected", "success") : badge("fallback", "info")}</div>`).join("")}<div class="decision-note">Fallback targets use the same hard capability constraints as the primary target.</div></article><article class="decision-panel"><h3>Selected because</h3><div class="key-value-list">${decision.selectedBecause.map((reason, index) => `<div class="key-value"><span>${index + 1}</span><strong>${escapeHTML(reason)}</strong></div>`).join("")}</div></article></section>
      ${renderSemanticPanel(decision.semantic)}
      <article class="card" style="margin-bottom:16px"><div class="card-header"><h2>Candidate evaluation</h2><span class="updated">Stable reason codes</span></div><div class="card-body">${decision.candidates.map(candidate => `<div class="candidate-result"><div><div class="candidate-name">${escapeHTML(candidate.name)}</div><div class="small muted">opaque target reference</div></div><div>${badge(candidate.result, candidate.tone)}</div><div class="candidate-reason">${escapeHTML(candidate.reason)}</div></div>`).join("")}</div></article>
      ${renderExecutionEvidence(decision)}
      ${replayMode ? `<article class="card"><div class="card-header"><h2>Replay comparison</h2>${replayStatus}</div><div class="card-body"><div class="notice">↻ <span><strong>Decision-only replay.</strong> No Provider call and no cost generated.</span></div>${replayAvailable ? `<div class="replay-grid" style="margin-top:15px"><div class="replay-column"><h3>Historical decision <span class="small muted">Config ${escapeHTML(decision.config)}</span></h3>${decision.plan.map((target, index) => `<div class="plan-step"><span class="plan-index">${index + 1}</span><span>${escapeHTML(target)}</span></div>`).join("")}</div><div class="replay-column"><h3>Replayed decision ${replayStatus}</h3>${replay.plan.map((target, index) => `<div class="plan-step"><span class="plan-index">${index + 1}</span><span>${escapeHTML(target)}</span></div>`).join("")}</div></div><div class="diff-list">${replay.differences.map(diff => `<div class="diff-item"><strong>${escapeHTML(diff.path)}</strong><span>${escapeHTML(diff.original)}</span><span>${escapeHTML(diff.replay)}</span></div>`).join("")}</div>` : `<div class="notice danger" style="margin-top:15px"><span>!</span><span>Replay is unavailable for the current API scope or decision algorithm. The historical decision remains unchanged.</span></div>`}</div></article>` : ""}
    </div>`;
  }

  function renderExecutionEvidence(decision) {
    if (decision.live) {
      return `<article class="card" style="margin-bottom:16px"><div class="card-header"><h2>Execution evidence</h2>${badge("Not available", "dark")}</div><div class="card-body"><p class="small muted">This decision record contains a routing plan only. Actual attempts, latency, generation cost and settlement must be read from the associated Run request. Missing execution data does not mean zero cost.</p><p class="small muted">Jev assessment cost is measured separately and is excluded from the Run generation budget.</p></div></article>`;
    }
    return `<article class="card" style="margin-bottom:16px"><div class="card-header"><h2>Execution evidence</h2><span class="updated">2 attempts · settlement complete</span></div><div class="card-body">${decision.attempts.map(attempt => `<div class="evidence-row"><span class="evidence-check ${attempt.tone === "warning" ? "warning" : ""}">${attempt.tone === "warning" ? "!" : "✓"}</span><div><div class="evidence-title">${attempt.index}. ${escapeHTML(attempt.target)} · ${escapeHTML(attempt.status)}</div><div class="evidence-subtitle">${escapeHTML(attempt.outcome)}</div></div>${badge(attempt.description, attempt.tone)}</div>`).join("")}<div class="key-value-list" style="margin-top:16px"><div class="key-value"><span>Total attempts</span><strong>2</strong></div><div class="key-value"><span>TTFB</span><strong>1.2s</strong></div><div class="key-value"><span>Cost</span><strong>$0.097</strong></div><div class="key-value"><span>Settlement</span><strong class="trend-up">complete</strong></div></div></div></article>`;
  }

  function renderSemanticPanel(semantic) {
    if (!semantic || semantic.status === "not_evaluated") {
      return `<article class="card" style="margin-bottom:16px"><div class="card-header"><h2>Semantic assessment</h2>${badge("Not evaluated", "dark")}</div><div class="card-body"><p class="small muted">This plan used the deterministic rule baseline. Dry-run and historical replay do not call Jev.</p></div></article>`;
    }
    const statusTone = semantic.applied ? "success" : semantic.status === "assessed" ? "info" : "warning";
    const taskDistribution = probabilityList(semantic.task_probabilities);
    const complexityDistribution = probabilityList(semantic.complexity_probabilities);
    const preferred = (semantic.preferred_target_ids || []).map(value => escapeHTML(value)).join(", ") || "—";
    return `<article class="card" style="margin-bottom:16px"><div class="card-header"><h2>Semantic assessment</h2>${badge(semantic.status || "unknown", statusTone)}</div><div class="card-body"><div class="decision-grid"><div class="decision-panel"><h3>Task type</h3><div class="key-value-list"><div class="key-value"><span>Class</span><strong>${escapeHTML(semantic.task_type || "—")}</strong></div><div class="key-value"><span>Confidence</span><strong>${formatConfidence(semantic.task_confidence)}</strong></div><div class="small muted">${taskDistribution}</div></div></div><div class="decision-panel"><h3>Complexity</h3><div class="key-value-list"><div class="key-value"><span>Class</span><strong>${escapeHTML(semantic.complexity || "—")}</strong></div><div class="key-value"><span>Confidence</span><strong>${formatConfidence(semantic.complexity_confidence)}</strong></div><div class="small muted">${complexityDistribution}</div></div></div><div class="decision-panel"><h3>Routing effect</h3><div class="key-value-list"><div class="key-value"><span>Applied</span><strong>${semantic.applied ? "Yes" : "No"}</strong></div><div class="key-value"><span>Quality floor</span><strong>${semantic.minimum_quality_tier ? `≥ ${semantic.minimum_quality_tier}` : "—"}</strong></div><div class="key-value"><span>Preferred targets</span><strong>${preferred}</strong></div><div class="key-value"><span>Reason</span><strong>${escapeHTML(semantic.reason || "—")}</strong></div></div></div></div><p class="small muted" style="margin-top:12px">${escapeHTML(semantic.model_version || "Jev unavailable")} · ${escapeHTML(semantic.language || "language unknown")} · state hash ${escapeHTML((semantic.state_hash || "—").slice(0, 23))} · no request text retained. Replay uses this frozen result.</p></div></article>`;
  }

  function probabilityList(values) {
    if (!values || typeof values !== "object") return "No probability distribution stored";
    return Object.entries(values).sort((left, right) => right[1] - left[1]).map(([label, value]) => `${escapeHTML(label)} ${formatConfidence(value)}`).join(" · ");
  }

  function formatConfidence(value) {
    const number = Number(value);
    return Number.isFinite(number) ? `${(number * 100).toFixed(1)}%` : "—";
  }

  function renderDecisions() {
    return `<div class="page">${demoBanner()}${pageHeading("Decision journal", "Decisions", "可解释、可重放的模型选择证据")}
      <section class="section-grid cols-3"><article class="card kpi"><div class="kpi-label">Decisions today</div><div class="kpi-value">2,481</div><div class="kpi-foot"><span class="trend-up">+14.2%</span><span>→</span></div></article><article class="card kpi"><div class="kpi-label">Replay match rate</div><div class="kpi-value">98.8%</div><div class="kpi-foot"><span>last 7 days</span><span>→</span></div></article><article class="card kpi"><div class="kpi-label">Capability-safe fallback</div><div class="kpi-value">100%</div><div class="kpi-foot"><span class="trend-up">No unsafe fallback</span><span>→</span></div></article></section>
      <article class="card"><div class="card-header"><h2>Recent decisions</h2><span class="updated">No payload content retained</span></div><div class="card-body table-scroll"><table class="data-table"><thead><tr><th>Decision</th><th>Run / request</th><th>Strategy</th><th>Selected target</th><th>Result</th><th>Time</th></tr></thead><tbody><tr class="clickable" data-href="#/decisions/dec_7F31"><td class="primary-cell mono">dec_7F31</td><td>run_8FA2 / req_03</td><td>balanced</td><td>anthropic-claude</td><td>${badge("Fallback success", "warning")}</td><td>14:33:04</td></tr><tr><td class="primary-cell mono">dec_7E02</td><td>run_21C9 / req_09</td><td>economy</td><td>openai-balanced</td><td>${badge("Selected", "success")}</td><td>14:31:21</td></tr></tbody></table></div></article></div>`;
  }

  function renderModels() {
    const models = state.liveModels || fixtures.models;
    return `<div class="page">${demoBanner()}${pageHeading("Model catalog", "Models", "逻辑模型、能力契约、目标健康与成本")}
      <article class="card"><div class="card-body"><div class="table-toolbar"><div class="tabs"><button class="tab is-active">All models</button><button class="tab">Healthy</button><button class="tab">Degraded</button><button class="tab">Blocked</button></div><div class="filters"><input class="input" placeholder="Search model" aria-label="Search model"/></div></div>${models.map(model => `<div class="card" style="margin-bottom:12px;box-shadow:none"><div class="card-header"><div><h2>${escapeHTML(model.description || model.id)}</h2><div class="mono muted" style="margin-top:4px">${escapeHTML(model.id)}</div></div><span class="badge dark">${model.targets.length} target${model.targets.length === 1 ? "" : "s"}</span></div><div class="card-body table-scroll"><table class="data-table"><thead><tr><th>Target</th><th>Provider</th><th>Capabilities</th><th>Quality</th><th>Context</th><th>Cost</th><th>Health</th></tr></thead><tbody>${model.targets.map(target => `<tr><td class="primary-cell mono">${escapeHTML(target.id)}</td><td>${escapeHTML(target.provider)}</td><td><div class="tag-list">${target.capabilities.map(cap => badge(cap, "dark")).join("")}</div></td><td>${escapeHTML(target.quality)}</td><td>${escapeHTML(target.context)}</td><td>${escapeHTML(target.cost)}</td><td>${badge(target.health, target.tone)}</td></tr>`).join("")}</tbody></table></div></div>`).join("")}</div></article></div>`;
  }

  function renderUsage() {
    return `<div class="page">${demoBanner()}${pageHeading("Cost & usage", "Usage", "确认成本、不确定账目与自动路由价值")}
      <section class="kpi-grid"><article class="card kpi"><div class="kpi-label">Confirmed cost</div><div class="kpi-value">$1,284.32</div><div class="kpi-foot"><span class="trend-up">+8.2%</span><span>vs previous period</span></div></article><article class="card kpi"><div class="kpi-label">Uncertain cost</div><div class="kpi-value">$12.40?</div><div class="kpi-foot"><span class="trend-down">2 Runs</span><span>requires resolution</span></div></article><article class="card kpi"><div class="kpi-label">Input tokens</div><div class="kpi-value">18.4M</div><div class="kpi-foot"><span>71% of total</span><span>→</span></div></article><article class="card kpi"><div class="kpi-label">Fallback cost</div><div class="kpi-value">$64.20</div><div class="kpi-foot"><span>5.0% of spend</span><span>→</span></div></article></section>
      <section class="section-grid cols-2"><article class="card"><div class="card-header"><h2>Cost by provider</h2></div><div class="card-body"><div class="metric-list"><div class="metric-row"><span class="metric-row-label">OpenAI</span><strong class="metric-row-value">$714.20 · 55.6%</strong><div class="metric-row-bar"><span style="width:55.6%"></span></div></div><div class="metric-row"><span class="metric-row-label">Anthropic</span><strong class="metric-row-value">$506.12 · 39.4%</strong><div class="metric-row-bar"><span style="width:39.4%"></span></div></div><div class="metric-row"><span class="metric-row-label">Other</span><strong class="metric-row-value">$63.99 · 5.0%</strong><div class="metric-row-bar"><span style="width:5%"></span></div></div></div></div></article><article class="card"><div class="card-header"><h2>Auto-routing saving</h2></div><div class="card-body"><div class="key-value-list"><div class="key-value"><span>Estimated saving</span><strong>$186.40</strong></div><div class="key-value"><span>Baseline</span><strong>preferred model</strong></div><div class="key-value"><span>Auto decisions</span><strong>71%</strong></div><div class="key-value"><span>Confidence</span><strong class="trend-up">Price-complete targets</strong></div></div><div class="notice" style="margin-top:18px">Saving is an estimate against the configured baseline, not a quality claim.</div></div></article></section></div>`;
  }

  function renderSettings() {
    return `<div class="page">${demoBanner()}${pageHeading("Control plane", "Settings", "配置版本、权限、凭据与审计状态")}
      <section class="section-grid cols-2"><article class="card"><div class="card-header"><h2>Configuration versions</h2><span class="badge success">v18 published</span></div><div class="card-body"><div class="evidence-row"><span class="evidence-check">✓</span><div><div class="evidence-title">Config v18</div><div class="evidence-subtitle">Published 14 minutes ago · 3 models · balanced default</div></div>${badge("Published", "success")}</div><div class="evidence-row"><span class="evidence-check">✓</span><div><div class="evidence-title">Config v17</div><div class="evidence-subtitle">Superseded yesterday · 3 models</div></div>${badge("Superseded", "dark")}</div><button class="button" style="margin-top:15px">View config diff</button></div></article><article class="card"><div class="card-header"><h2>Security posture</h2><span class="badge success">Healthy</span></div><div class="card-body"><div class="key-value-list"><div class="key-value"><span>API key storage</span><strong>HMAC digest</strong></div><div class="key-value"><span>Provider credentials</span><strong>Encrypted</strong></div><div class="key-value"><span>Payload logging</span><strong>Disabled</strong></div><div class="key-value"><span>Tenant isolation</span><strong>RLS enabled</strong></div><div class="key-value"><span>Outbound policy</span><strong>Allowlist enforced</strong></div></div></div></article></section><article class="card"><div class="card-header"><h2>Recent audit events</h2><span class="updated">Admin scope required</span></div><div class="card-body table-scroll"><table class="data-table"><thead><tr><th>Actor</th><th>Action</th><th>Resource</th><th>Result</th><th>Time</th></tr></thead><tbody><tr><td class="primary-cell">admin@acme</td><td>Published config</td><td class="mono">v18</td><td>${badge("Success", "success")}</td><td>14 minutes ago</td></tr><tr><td class="primary-cell">platform-bot</td><td>Rotated credential</td><td>openai endpoint</td><td>${badge("Success", "success")}</td><td>2 hours ago</td></tr></tbody></table></div></article></div>`;
  }

  async function fetchJSON(path, options) {
    if (!state.token) throw new Error("no token");
    const headers = Object.assign({ Authorization: `Bearer ${state.token}`, Accept: "application/json" }, (options && options.headers) || {});
    const response = await fetch(path, Object.assign({}, options || {}, { headers }));
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.json();
  }

  function mapLiveRun(raw) {
    const existing = fixtures.runDetails.run_8FA2;
    const settled = usdFromNano(raw.settled_cost_nano_usd);
    const budget = usdFromNano(raw.soft_budget_nano_usd);
    const used = Number(raw.soft_budget_nano_usd) > 0 ? Math.min(100, Number(raw.settled_cost_nano_usd || 0) / Number(raw.soft_budget_nano_usd) * 100) : 0;
    return Object.assign({}, existing, {
      name: raw.id,
      tenant: "current tenant",
      state: raw.state,
      created: raw.created_at || existing.created,
      deadline: relativeDeadline(raw.deadline),
      config: raw.config_version || "runtime",
      cost: settled,
      budget,
      remaining: Number(raw.soft_budget_nano_usd) > 0 ? usdFromNano(Math.max(0, Number(raw.soft_budget_nano_usd) - Number(raw.settled_cost_nano_usd || 0))) : "—",
      inflight: `${raw.in_flight || 0} / ${raw.max_parallelism || 0}`,
      requests: "request detail via API",
      accounting: raw.state === "suspended_accounting" ? "Suspended" : "Confirmed",
      deadlineRisk: raw.deadline && Date.parse(raw.deadline) < Date.now() ? "High" : "Low",
      requestRows: [],
      timeline: [
        { time: raw.created_at || "—", title: "Run created", description: `soft budget ${budget} · max parallelism ${raw.max_parallelism || "—"}` },
        { time: raw.updated_at || "—", title: `Run ${raw.state}`, description: `settled cost ${settled} · in flight ${raw.in_flight || 0}`, tone: raw.state === "suspended_accounting" ? "warning" : "success" }
      ],
      budgetProgress: used
    });
  }

  function mapLiveDecision(raw, replay) {
    const input = raw.input || {};
    const plan = raw.plan || {};
    const contract = input.request && input.request.contract || {};
    const targets = (plan.targets || []).map(target => target.target_id || target.model_id).filter(Boolean);
    const candidates = (plan.candidates || []).map(candidate => ({
      name: candidate.target_id || candidate.model_id || "opaque target",
      result: !candidate.accepted ? "REJECTED" : targets[0] === candidate.target_id ? "SELECTED" : "FALLBACK",
      tone: candidate.accepted ? "success" : "danger",
      reason: candidate.reason || candidate.reason_code || "recorded by decision engine"
    }));
    return {
      id: raw.decision_id,
      live: true,
      state: targets.length ? "accepted" : "rejected",
      run: input.run && input.run.governed ? "governed run" : "ungoverned",
      request: "request payload omitted",
      time: raw.created_at || "—",
      algorithm: plan.algorithm_version || input.algorithm_version || "decision.v1",
      config: plan.config_version || input.config_version || "runtime",
      inputHash: plan.input_hash || "—",
      planHash: plan.plan_hash || "—",
      model: input.request && input.request.model || "auto",
      capabilities: contract.required_capabilities || [],
      quality: contract.minimum_quality_tier ? `≥ ${contract.minimum_quality_tier}` : "—",
      context: contract.required_context_tokens ? String(contract.required_context_tokens) : "—",
      dataClass: contract.data_class || "—",
      strategy: plan.effective_strategy || "balanced",
      selectedBecause: plan.reasons || [],
      semantic: input.semantic_assessment ? input.semantic_assessment : {status: plan.semantic_status || "not_evaluated"},
      plan: targets,
      candidates,
      attempts: [],
      replay: replay ? {
        match: Boolean(replay.match),
        plan: (replay.replay_plan && replay.replay_plan.targets || []).map(target => target.target_id || target.model_id).filter(Boolean),
        differences: replay.differences || []
      } : { unavailable: true, match: false, plan: [], differences: [] }
    };
  }

  async function loadLiveRoute(route) {
    if (!state.live) return;
    if (route.startsWith("runs/")) {
      const runID = route.split("/")[1];
      if (!runID || state.liveRunDetails[runID]) return;
      try {
        state.liveRunDetails[runID] = mapLiveRun(await fetchJSON(`/v1/limen/runs/${encodeURIComponent(runID)}`));
        render();
      } catch (error) {
        state.lastError = error.message;
      }
      return;
    }
    if (route.startsWith("decisions/")) {
      const decisionID = route.split("/")[1];
      if (!decisionID || state.liveDecisions[decisionID]) return;
      try {
        const record = await fetchJSON(`/v1/limen/decisions/${encodeURIComponent(decisionID)}`);
        let replay = null;
        try { replay = await fetchJSON(`/v1/limen/decisions/${encodeURIComponent(decisionID)}/replay`, { method: "POST" }); } catch (_) {}
        state.liveDecisions[decisionID] = mapLiveDecision(record, replay);
        render();
      } catch (error) {
        state.lastError = error.message;
      }
    }
  }

  async function refreshLive() {
    if (!state.token) return;
    try {
      const models = await fetchJSON("/v1/models");
      state.liveModels = (models.data || []).map(model => ({ id: model.id, description: model.id, targets: [{ id: model.id, provider: model.owned_by || "limen", capabilities: ["catalogued"], quality: "—", context: "—", cost: "—", health: "Available", tone: "success" }] }));
      state.live = true;
      state.lastError = "";
      render();
    } catch (error) {
      state.lastError = error.message;
      state.live = false;
      render();
    }
  }

  function connect() {
    if (root.querySelector(".connection-dialog")) return;
    const dialog = document.createElement("dialog");
    dialog.className = "card connection-dialog";
    dialog.innerHTML = `<form class="connection-form"><h2>Connect Limen API</h2><p class="small muted">The key is stored only in this browser session and sent to this Limen server.</p><label for="limen-api-key">Limen API key</label><input id="limen-api-key" name="apiKey" type="password" autocomplete="off" required autofocus><div class="connection-actions"><button type="button" class="button" data-action="close-connect">Cancel</button><button type="submit" class="button primary">Connect</button></div></form>`;
    dialog.addEventListener("close", () => dialog.remove());
    dialog.querySelector("[data-action=close-connect]").addEventListener("click", () => dialog.close());
    dialog.querySelector("form").addEventListener("submit", event => {
      event.preventDefault();
      const token = dialog.querySelector("input").value.trim();
      if (!token) return;
      state.token = token;
      sessionStorage.setItem("limen_ui_token", token);
      state.live = false;
      state.liveModels = null;
      state.liveRunDetails = {};
      state.liveDecisions = {};
      state.lastError = "";
      dialog.close();
      refreshLive();
    });
    root.appendChild(dialog);
    dialog.showModal();
  }

  function disconnect() {
    sessionStorage.removeItem("limen_ui_token");
    state.token = "";
    state.live = false;
    state.liveModels = null;
    state.liveRunDetails = {};
    state.liveDecisions = {};
    render();
  }

  function viewFor(route) {
    if (route === "overview") return renderOverview();
    if (route === "runs") return renderRuns();
    if (route.startsWith("runs/")) return renderRunDetail(route.split("/")[1]);
    if (route === "decisions") return renderDecisions();
    if (route.startsWith("decisions/")) return renderDecision(route.split("/")[1], route.endsWith("/replay"));
    if (route === "models") return renderModels();
    if (route === "usage") return renderUsage();
    if (route === "settings") return renderSettings();
    return renderOverview();
  }

  function render() {
    const route = currentRoute();
    const base = routeName(route);
    if (!root.querySelector(".app-shell")) renderShell();
    root.querySelectorAll("[data-nav]").forEach(link => link.classList.toggle("is-active", link.dataset.nav === base));
    const label = navItems().find(item => item[0] === base);
    const breadcrumb = root.querySelector("#breadcrumb-current");
    if (breadcrumb) breadcrumb.textContent = label ? label[1] : "Overview";
    const connection = root.querySelector("#connection-label");
    if (connection) connection.textContent = state.live ? "Connected" : "Demo data";
    const connectionButton = root.querySelector(".connection-control");
    if (connectionButton) connectionButton.dataset.action = state.live ? "disconnect" : "connect";
    root.querySelector("#view").innerHTML = viewFor(route);
    loadLiveRoute(route);
  }

  root.addEventListener("click", event => {
    const target = event.target.closest("[data-href], [data-route], [data-action]");
    if (!target) return;
    if (target.dataset.href) {
      location.hash = target.dataset.href.replace(/^#/, "");
      return;
    }
    if (target.dataset.route) {
      location.hash = `/${target.dataset.route}`;
      return;
    }
    if (target.dataset.action === "connect") connect();
    if (target.dataset.action === "disconnect") disconnect();
    if (target.dataset.action === "replay") {
      const route = currentRoute();
      if (!route.endsWith("/replay")) location.hash = `/${route}/replay`;
    }
    if (target.dataset.action === "cancel") window.alert("Demo mode: cancellation is available when connected to a live Run API.");
    if (target.dataset.action === "resolve-accounting") window.alert("Accounting resolution requires an explicit cost or accept-unknown decision with an admin-scoped API key.");
    if (target.dataset.action === "compare") window.alert("Choose a config version from Settings to compare this decision.");
  });

  window.addEventListener("hashchange", render);
  render();
  refreshLive();
})();
