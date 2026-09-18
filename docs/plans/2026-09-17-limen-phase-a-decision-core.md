# Limen 阶段 A：可解释决策核心实施计划

> **For agentic workers:** 本计划按复选框逐项执行；每个任务完成后运行指定验证并独立提交。

> 状态：已完成（2026-09-17）。目录、决策引擎、能力校验、Plan 执行器、严格 Chat 契约和安全出站 Client 已实现；`make check`、`make build`、`make smoke` 均通过。

**目标：** 在不破坏现有 OpenAI/Anthropic 文本 Chat、SSE、熔断、Fallback、取消和结算行为的前提下，引入能力目录、确定性 Decision Engine、ExecutionPlan 和能力安全 Fallback。

**架构：** 将模型目录从 gateway 注册表职责中独立出来；Decision Engine 只消费带版本的快照并输出有序 ExecutionPlan，Gateway Executor 只执行计划并返回现有 Result/Settlement 所需的报告。阶段 A 不引入 PostgreSQL、Run 持久化、Responses、Tools、Vision 或第三 Provider，但会为后续 Run 保存稳定的输入和计划哈希边界。

**技术栈：** Go 1.24、标准库、现有 Provider/HTTP 实现、crypto/sha256、encoding/json。不新增第三方依赖。

---

## 文件职责

- 创建 internal/catalog/catalog.go：模型、目标、能力元数据和只读注册表；兼容模式也在此实现。
- 修改 internal/gateway/registry.go：保留旧导出名的类型别名和构造函数包装，避免一次性改动所有调用方。
- 创建 internal/decision/types.go：能力契约、健康快照、DecisionInput、候选结果和 ExecutionPlan。
- 创建 internal/decision/canonical.go：规范化快照编码、输入哈希和计划哈希。
- 创建 internal/decision/engine.go：硬过滤、策略排序、原因码和 model=auto 决策。
- 创建 internal/decision/decision_test.go、canonical_test.go：纯决策表驱动测试、确定性和 golden fixture。
- 修改 internal/config/config.go、config_test.go：解析目标能力字段、校验受控枚举和数据等级。
- 修改 internal/gateway/breaker.go：增加不改变熔断状态的观察快照。
- 修改 internal/gateway/router.go、router_test.go：由 Decision Engine 生成计划，Executor 按计划执行；保留当前兼容路径和结果类型。
- 修改 internal/httpapi/handler.go、handler_test.go：解析 limen 扩展和所有未支持字段显式错误。
- 创建 internal/provider/client.go、client_test.go：生产安全 HTTP Client 的重定向、代理和地址策略边界；Provider 构造函数使用它。
- 修改 cmd/limen/main.go：使用 catalog 类型和安全 Client，保持启动装配简单。
- 修改 README.md、docs/design.md、AGENTS.md：同步阶段 A 已实现范围和兼容矩阵。

## Task 1：建立 catalog 类型并迁移注册表

**Files:** 创建 internal/catalog/catalog.go；修改 internal/gateway/registry.go；测试 internal/catalog/catalog_test.go 和现有 registry 测试。

- [ ] **Step 1: 写失败测试**

增加测试，验证目标复制包含能力元数据且调用方不能修改注册表：

~~~go
func TestRegistryClonesCapabilityMetadata(t *testing.T) {
  registry, err := NewRegistry([]Model{{
    ID: "coding",
    Targets: []Target{{
      ID: "openai-code", Provider: "openai", UpstreamModel: "gpt-test",
      Capabilities: []string{"text"}, SupportsStreaming: true,
      QualityTier: 3, CostTier: 1, ContextWindow: 128000,
      DataClasses: []string{"public", "internal"},
    }},
  }})
  if err != nil { t.Fatal(err) }
  got := registry.List()
  got[0].Targets[0].Capabilities[0] = "changed"
  if registry.List()[0].Targets[0].Capabilities[0] != "text" {
    t.Fatal("registry metadata was mutable")
  }
}
~~~

- [ ] **Step 2: 运行失败测试**

~~~bash
go test ./internal/catalog -run TestRegistryClonesCapabilityMetadata -count=1
~~~

预期：因 internal/catalog 或 NewRegistry 尚不存在而失败。

- [ ] **Step 3: 实现最小目录**

将现有注册表逻辑移动到 internal/catalog，增加 Target 的 ID、Capabilities、SupportsStreaming、QualityTier、CostTier、ContextWindow 和 DataClasses；clone 必须复制字符串切片。兼容模式继续生成 gpt-*、o1-*、o3-*、claude-* 四条记录。为兼容旧的 Go 构造方式，NewRegistry 遇到空 Target ID 时先按 provider:upstream_model 派生稳定 ID；JSON 配置的迁移校验在 Task 3 单独覆盖。

在 internal/gateway/registry.go 使用类型别名和构造函数包装，消除重复模型定义。

- [ ] **Step 4: 运行验证**

~~~bash
go test ./internal/catalog ./internal/gateway
~~~

预期：PASS，现有 gateway 测试行为不变。

- [ ] **Step 5: 提交**

~~~bash
git add internal/catalog internal/gateway/registry.go
git commit -m "refactor: extract model catalog"
~~~

## Task 2：实现能力契约和确定性 Decision Engine

**Files:** 创建 internal/decision/types.go、canonical.go、engine.go 及对应测试。

- [ ] **Step 1: 写失败测试**

测试硬约束、两种策略、稳定原因码和 model=auto；另外覆盖缺失能力、上下文不足、流式不支持、空候选、软预算阈值切换和哈希确定性。

~~~go
func target(id string, quality int, streaming bool, capabilities, dataClasses []string) catalog.Target {
  return catalog.Target{
    ID: id, Provider: "openai", UpstreamModel: id,
    Capabilities: capabilities, SupportsStreaming: streaming,
    QualityTier: quality, DataClasses: dataClasses,
  }
}

func TestDecideFiltersCapabilitiesAndSorts(t *testing.T) {
  input := Input{
    SchemaVersion: "decision-input.v1",
    AlgorithmVersion: "decision.v1",
    Request: Request{Model: "auto", Stream: true, Contract: Contract{
      RequiredCapabilities: []string{"text"}, MinimumQualityTier: 2,
      DataClass: "internal", Strategy: "balanced",
    }},
    Candidates: []Candidate{
      {ModelID: "slow", Target: target("slow", 4, true, []string{"text"}, []string{"public"})},
      {ModelID: "good", Target: target("good", 3, true, []string{"text"}, []string{"public", "internal"})},
    },
    Run: RunSnapshot{Governed: false},
  }
  plan, err := NewEngine().Decide(input)
  if err != nil { t.Fatal(err) }
  if got := plan.Targets[0].Target.ID; got != "good" { t.Fatalf("target = %q", got) }
  if plan.Candidates[0].Reason != "data_policy_denied" { t.Fatal(plan.Candidates) }
}
~~~

- [ ] **Step 2: 运行失败测试**

~~~bash
go test ./internal/decision -count=1
~~~

预期：因类型和引擎尚不存在而失败。

- [ ] **Step 3: 实现最小纯逻辑**

实现以下边界：

~~~go
type Engine struct{}
func NewEngine() Engine
func (Engine) Decide(Input) (ExecutionPlan, error)
func CanonicalInput(Input) ([]byte, error)
func HashInput(Input) (string, error)
func HashPlan(ExecutionPlan) (string, error)
~~~

硬过滤顺序固定为启用状态、安全允许、能力、流式、上下文、数据等级、健康观察和 minimum_attempt_window。只保留 balanced 和 economy；成本有 Token 估算和价格时使用定点预计费用，否则使用 CostTier。balanced Run 在剩余软预算低于 EconomyThresholdPercent 时由 Engine 生成 effective_strategy=economy 和原因码 economy_threshold_reached。

使用强类型结构、整数/定点字符串、固定数组顺序和排序键生成规范 JSON；SHA-256 结果带 sha256: 前缀。Engine 不读取当前时间、不访问网络、不调用数据库。

- [ ] **Step 4: 增加 golden fixture 并验证确定性**

将至少 10 组输入/计划 fixture 放入 internal/decision/testdata/，测试重新构造 Engine 后 plan_hash 不变。

~~~bash
go test ./internal/decision -run 'TestDecide|TestCanonical|TestGolden' -count=1
~~~

- [ ] **Step 5: 提交**

~~~bash
git add internal/decision
git commit -m "feat: add deterministic decision engine"
~~~

## Task 3：扩展配置并严格校验能力字段

**Files:** 修改 internal/config/config.go、internal/config/config_test.go、configs/models.example.json。

- [ ] **Step 1: 写失败测试**

覆盖缺失 target ID、重复 target ID、非法 capability、非法 data class、非正 context window、quality/cost 越界；不支持的 Tools/Vision 字段不进入配置模型。

~~~go
func TestLoadRejectsUnknownCapabilityAndDataClass(t *testing.T) {
  contents := "{\"models\":[{\"id\":\"m\",\"targets\":[{\"id\":\"t\",\"provider\":\"openai\",\"upstream_model\":\"gpt\",\"capabilities\":[\"magic\"],\"data_classes\":[\"secret\"]}]}]}"
  modelsFile := filepath.Join(t.TempDir(), "models.json")
  if err := os.WriteFile(modelsFile, []byte(contents), 0o600); err != nil { t.Fatal(err) }
  t.Setenv("LIMEN_API_KEY", "limen-secret")
  t.Setenv("OPENAI_API_KEY", "openai-secret")
  t.Setenv("ANTHROPIC_API_KEY", "")
  t.Setenv("LIMEN_MODELS_FILE", modelsFile)
  if _, err := Load(); err == nil {
    t.Fatal("expected capability validation error")
  }
}
~~~

- [ ] **Step 2: 运行失败测试**

~~~bash
go test ./internal/config -run 'Capability|DataClass|TargetID' -count=1
~~~

- [ ] **Step 3: 实现校验**

在 JSON Target 增加明确字段；能力只允许 text，数据等级只允许 public、internal、confidential、restricted；Provider、upstream model 和 target ID 必填且不可重复。为旧配置缺失能力字段按 text；缺失 target ID 时使用确定性 provider:upstream_model 派生 ID 并保留迁移说明。

- [ ] **Step 4: 运行验证**

~~~bash
go test ./internal/config ./internal/catalog
~~~

- [ ] **Step 5: 提交**

~~~bash
git add internal/config configs/models.example.json
git commit -m "feat: validate model capabilities"
~~~

## Task 4：把 Router 改为 Plan 执行器并保留可靠性语义

**Files:** 修改 internal/gateway/breaker.go、router.go、router_test.go、reliability_test.go、cmd/limen/main.go。

- [ ] **Step 1: 写失败测试**

增加固定 Provider 测试：model=auto 只调用满足能力和数据等级的目标；计划生成后 Half-Open 被占用时记录 skipped_due_to_race 并尝试下一个；显式模型无契约时保持原配置目标顺序。

- [ ] **Step 2: 运行回归基线**

~~~bash
go test ./internal/gateway ./cmd/limen -count=1
~~~

预期：新增测试失败，旧测试通过。

- [ ] **Step 3: 实现观察和计划执行**

给 Breaker 增加只读 Observe，返回 closed/open/half_open 和探测可用状态；Router 创建 DecisionInput 并调用 Engine。Router 只把已解析的 ExecutionPlan 交给内部执行函数，按计划替换 upstream model、执行当前 allow、记录 DecisionStep 和生成现有 Settlement。

显式模型且未提供契约时使用兼容顺序；auto 只在显式 catalog 模式可用。Executor 不写 Decision Journal 或 Ledger，只返回现有 Result 以及计划摘要。

- [ ] **Step 4: 运行完整回归**

~~~bash
go test ./... -count=1
go test ./... -race -count=1
~~~

- [ ] **Step 5: 提交**

~~~bash
git add internal/gateway cmd/limen/main.go
git commit -m "refactor: execute deterministic routing plans"
~~~

## Task 5：严格处理 Chat 扩展字段

**Files:** 修改 internal/httpapi/handler.go、handler_test.go、README.md。

- [ ] **Step 1: 写失败测试**

增加请求字段矩阵测试：未知字段、tools、tool_choice、response_format、图片内容、n 和 logprobs 都返回 400 unsupported_field；合法 text/system/developer/user/assistant 请求继续通过；limen 未知子字段同样拒绝。

- [ ] **Step 2: 运行失败测试**

~~~bash
go test ./internal/httpapi -run 'Unsupported|Field|Contract' -count=1
~~~

- [ ] **Step 3: 实现严格解析**

为入站请求增加 Limen 结构和 json.Decoder.DisallowUnknownFields；每个不支持字段返回字段名和稳定错误码，不把字段静默丢弃。限制能力为 text，stream 从标准请求派生为 supports_streaming 硬约束。

- [ ] **Step 4: 运行 HTTP 回归**

~~~bash
go test ./internal/httpapi -count=1
~~~

- [ ] **Step 5: 提交**

~~~bash
git add internal/httpapi README.md
git commit -m "feat: reject unsupported chat fields explicitly"
~~~

## Task 6：加入生产安全 HTTP Client

**Files:** 创建 internal/provider/client.go、client_test.go；修改 internal/provider/openai.go、anthropic.go、cmd/limen/main.go。

- [ ] **Step 1: 写失败测试**

测试自定义 Client：重定向不自动跟随，默认不读取环境代理，生产模式拒绝 HTTP；允许列表外域名、环回、RFC1918、链路本地和云元数据地址被拒绝。

- [ ] **Step 2: 运行失败测试**

~~~bash
go test ./internal/provider -run 'Client|Redirect|Proxy|Private' -count=1
~~~

- [ ] **Step 3: 实现最小安全 Transport**

提供 NewHTTPClient(SecurityOptions) *http.Client；CheckRedirect 返回 http.ErrUseLastResponse；自定义 DialContext 在实际连接前检查解析出的 IP，默认关闭 ProxyFromEnvironment；保留 TLS ServerName。HTTP 仅在显式开发选项开启。

- [ ] **Step 4: 保持 Provider 协议回归**

~~~bash
go test ./internal/provider ./internal/gateway -count=1
~~~

- [ ] **Step 5: 提交**

~~~bash
git add internal/provider cmd/limen/main.go
git commit -m "feat: enforce secure provider transport"
~~~

## Task 7：同步阶段 A 文档并完成验收

**Files:** 修改 README.md、docs/design.md、AGENTS.md、CHANGELOG.md。

- [ ] **Step 1: 更新文档**

写明阶段 A 的能力目录字段、model=auto、balanced/economy、软预算阈值、OpenAI Chat 支持子集、显式拒绝字段、DecisionInput/ExecutionPlan 哈希和安全出站边界。明确 Run、PostgreSQL、Responses、Tools、Vision、Gemini 和第三 Provider 仍未实现。

- [ ] **Step 2: 全量检查**

~~~bash
gofmt -w $(rg --files -g '*.go')
go vet ./...
go clean -testcache
go test ./... -race -count=1
make check
make build
make smoke
make bench
git diff --check
~~~

预期：所有命令成功；没有敏感字段进入新增测试日志；SSE 仍保持实时转发。

- [ ] **Step 3: 提交**

~~~bash
git add README.md docs/design.md AGENTS.md CHANGELOG.md
git commit -m "docs: record phase A decision core"
~~~

## 阶段 A 完成标准

- model=auto 能在显式注册表中按照能力、数据等级、流式支持、质量和策略选择目标。
- 不满足硬约束的目标永远不会进入 ExecutionPlan；Fallback 只能使用同一计划中的合格目标。
- 相同 DecisionInput 产生完全相同的 plan_hash，至少 10 组 golden fixture 通过。
- 现有显式模型、兼容模式、普通响应、SSE、熔断、Fallback、超时、取消和结算回归通过。
- 不支持的 Chat 字段显式返回 400，不再静默丢弃。
- Provider 默认禁用重定向和环境代理，生产出站地址执行允许列表和私网地址检查。
- go vet、竞态测试、构建、冒烟、基准和 diff 检查全部通过。
