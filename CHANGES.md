# TarsOneAPI 变更日志

## v1.5.0 (2026-10-18)
- 整合 TSG 历史优点：指标监控体系（/metrics 端点、请求计数、错误率、平均延迟）
- 健康检查增强：记录上游响应延迟（latency_ms）
- 详细错误信息人话化：上游故障返回"上游服务暂时不可用，请稍后重试"

## v1.4.0 (2026-10-18)
- 整合 TSG 历史优点：安全加固层
- 速率限制：基于 IP 的令牌桶算法（默认 60 req/s，突发 10）
- 请求体大小限制：1MB MaxBodyReader
- 错误信息脱敏：不向上游暴露原始错误细节

## v1.3.0 (2026-10-18)
- 整合 TSG 历史优点：性能优化（连接池与重试机制）
- HTTP Transport 连接池：MaxIdleConns=100，MaxConnsPerHost=100
- 指数退避重试：reqWithRetry 自动重试 3 次
- User-Agent 标识：TarsOneAPI/{version}

## v1.2.0 (2026-10-18)
- 整合 TSG 历史优点：审计与可观测性
- 请求日志中间件：记录方法、路径、状态码、耗时
- 响应包装器：捕获 HTTP 状态码用于日志

## v1.1.0 (2026-10-18)
- 整合 TSG 历史优点：配置系统灵活性
- JSON 配置文件支持（config.json）
- 配置优先级：环境变量 > 配置文件 > 默认值
- 配置解析失败时输出警告日志（不中断启动）

## v1.0.0
- 初始版本：MCP adapter for OneAPI
- 支持 stdio 和 HTTP 双模式
- 提供 oneapi_models 和 oneapi_chat 两个工具
