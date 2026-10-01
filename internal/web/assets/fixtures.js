window.LimenFixtures = {
  overview: {
    period: "过去 7 天",
    updated: "12 秒前",
    kpis: [
      { label: "Confirmed cost", value: "$1,284.32", foot: "较上周期 +8.2%", tone: "up" },
      { label: "Budget health", value: "72.4%", foot: "3 个 Run 需要关注", progress: 72.4, tone: "neutral" },
      { label: "Success rate", value: "99.42%", foot: "较上周期 -0.18%", tone: "down" },
      { label: "Auto-routing saving", value: "$186.40", foot: "相对 preferred model · 12.7%", tone: "up" }
    ],
    trend: [38, 44, 41, 52, 63, 61, 72, 78, 76, 84, 91, 98],
    budget: [44, 48, 52, 56, 60, 64, 68, 72, 76, 80, 84, 88],
    reliability: [
      { label: "Success rate", value: "99.42%", progress: 99.42 },
      { label: "P95 latency", value: "4.2s", progress: 64 },
      { label: "Fallback rate", value: "3.8%", progress: 18 },
      { label: "Accounting uncertain", value: "2", progress: 8, tone: "warning" }
    ],
    decisions: [
      { label: "Premium quality", value: 42 },
      { label: "Balanced", value: 37 },
      { label: "Economy", value: 21 }
    ],
    providers: [
      { name: "OpenAI", status: "Healthy", rate: "99.7%", latency: "2.8s" },
      { name: "Anthropic", status: "Degraded", rate: "97.1%", latency: "5.3s", tone: "warning" },
      { name: "Gemini", status: "Healthy", rate: "99.4%", latency: "3.1s" }
    ],
    attention: [
      { severity: "Critical", tone: "danger", subject: "run_8FA2", problem: "Accounting uncertain", impact: "$12.40?", updated: "2 分钟前", route: "#/runs/run_8FA2" },
      { severity: "Warning", tone: "warning", subject: "Research Bot", problem: "Budget 91% used", impact: "14 active", updated: "5 分钟前", route: "#/runs/run_8FA2" },
      { severity: "Warning", tone: "warning", subject: "run_21C9", problem: "Fallback spike", impact: "6 attempts", updated: "8 分钟前", route: "#/decisions/dec_7F31" },
      { severity: "Info", tone: "info", subject: "Config v18", problem: "Publish pending", impact: "—", updated: "14 分钟前", route: "#/settings" }
    ]
  },
  runs: [
    { id: "run_8FA2", name: "Research Agent Run", tenant: "acme-prod", state: "suspended_accounting", budget: "$0.7421 / $1.00", budgetProgress: 74.2, requests: "18 · 2 in flight", fallback: "3 / 18", success: "94.4%", updated: "2 分钟前", tone: "danger" },
    { id: "run_21C9", name: "Coding Review", tenant: "platform-team", state: "active", budget: "$0.388 / $0.50", budgetProgress: 77.6, requests: "11 · 1 in flight", fallback: "4 / 11", success: "100%", updated: "8 分钟前", tone: "warning" },
    { id: "run_36B0", name: "Support Triage", tenant: "support-prod", state: "completed", budget: "$0.164 / $0.80", budgetProgress: 20.5, requests: "26 · settled", fallback: "0 / 26", success: "100%", updated: "31 分钟前", tone: "success" },
    { id: "run_44D1", name: "Release Notes", tenant: "docs-team", state: "active", budget: "$0.049 / $0.20", budgetProgress: 24.5, requests: "7 · 1 in flight", fallback: "0 / 7", success: "100%", updated: "1 小时前", tone: "success" }
  ],
  runDetails: {
    run_8FA2: {
      name: "Research Agent Run", tenant: "acme-prod", state: "suspended_accounting", tone: "danger", created: "14:32:08", deadline: "18 分钟后", config: "v18", algorithm: "decision.v1", strategy: "balanced", cost: "$0.7421", budget: "$1.00", budgetProgress: 74.2, reserved: "$0.0810", uncertain: "$12.40?", remaining: "$0.1769", inflight: "2 / 4", requests: "18 · 17 settled", fallback: "3 / 18", failed: "1", accounting: "Suspended", deadlineRisk: "Low",
      timeline: [
        { time: "14:32:08", title: "Run created", description: "soft budget $1.00 · max parallelism 4" },
        { time: "14:32:11", title: "req_01 · smart-model → anthropic-claude", description: "$0.082 · 2.8s · primary selected", tone: "success" },
        { time: "14:32:19", title: "req_02 · auto → openai-balanced", description: "$0.014 · 1.3s · economy policy activated" },
        { time: "14:33:04", title: "req_03 · safe fallback", description: "anthropic 429 → openai success · $0.097 · 5.6s", tone: "warning" },
        { time: "14:34:27", title: "req_04 · openai-balanced", description: "Streaming in progress · 12.1s", tone: "success" }
      ],
      requestRows: [
        { id: "req_01", contract: "reasoning · Q4", decision: "anthropic-claude", cost: "$0.082", latency: "2.8s", state: "settled", tone: "success" },
        { id: "req_02", contract: "text · economy", decision: "openai-balanced", cost: "$0.014", latency: "1.3s", state: "settled", tone: "success" },
        { id: "req_03", contract: "tools · Q4", decision: "1 fallback", cost: "$0.097", latency: "5.6s", state: "settled", tone: "warning" },
        { id: "req_04", contract: "text · stream", decision: "openai-balanced", cost: "—", latency: "12.1s", state: "in flight", tone: "info" }
      ]
    }
  },
  decisions: {
    dec_7F31: {
      id: "dec_7F31", state: "accepted", run: "run_8FA2", request: "req_03", time: "14:33:04", algorithm: "decision.v1", config: "v18", inputHash: "sha256:91de…", planHash: "sha256:4c91…", model: "auto", capabilities: ["reasoning", "tool_calling"], quality: "≥ 4", context: "32K", dataClass: "internal", strategy: "balanced", selected: "anthropic-claude", selectedBecause: ["满足 reasoning 与 tool_calling", "质量等级达到 Q4", "允许发送 internal 数据", "在健康候选中成本最低"], plan: ["anthropic-claude", "openai-balanced", "anthropic-fast"], candidates: [
        { name: "anthropic-claude", result: "SELECTED", tone: "success", reason: "capability + quality + healthy" },
        { name: "openai-balanced", result: "FALLBACK", tone: "info", reason: "同一契约，成本略高" },
        { name: "anthropic-fast", result: "REJECTED", tone: "danger", reason: "quality_tier_too_low" },
        { name: "openai-legacy", result: "REJECTED", tone: "danger", reason: "missing tool_calling" },
        { name: "private-model", result: "REJECTED", tone: "danger", reason: "data_policy_denied" }
      ],
      attempts: [
        { index: 1, target: "anthropic-claude", status: "429", outcome: "transient_rate_limit", description: "Fallback allowed", tone: "warning" },
        { index: 2, target: "openai-balanced", status: "200", outcome: "success", description: "Result committed", tone: "success" }
      ],
      replay: { match: false, plan: ["openai-balanced", "anthropic-claude"], differences: [
        { path: "targets[0]", original: "anthropic-claude", replay: "openai-balanced" },
        { path: "health.anthropic-claude", original: "healthy", replay: "degraded" },
        { path: "effective_strategy", original: "balanced", replay: "economy" }
      ] }
    }
  },
  models: [
    { id: "coding-strong", description: "Coding Strong", targets: [
      { id: "anthropic-claude", provider: "Anthropic", capabilities: ["reasoning", "coding", "tools"], quality: "Q4", context: "200K", cost: "$$$", health: "Healthy", tone: "success" },
      { id: "openai-balanced", provider: "OpenAI", capabilities: ["reasoning", "coding", "tools"], quality: "Q4", context: "128K", cost: "$$", health: "Healthy", tone: "success" },
      { id: "openai-legacy", provider: "OpenAI", capabilities: ["text"], quality: "Q2", context: "16K", cost: "$", health: "Blocked", tone: "danger" }
    ] },
    { id: "fast-general", description: "Fast General", targets: [
      { id: "openai-balanced", provider: "OpenAI", capabilities: ["text", "structured"], quality: "Q3", context: "32K", cost: "$", health: "Healthy", tone: "success" }
    ] },
    { id: "long-context", description: "Long Context", targets: [
      { id: "anthropic-claude", provider: "Anthropic", capabilities: ["text", "long_context"], quality: "Q4", context: "200K", cost: "$$$", health: "Degraded", tone: "warning" }
    ] }
  ]
};
