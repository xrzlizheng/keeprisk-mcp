# 贡献指南

感谢关注 KeepRisk MCP。

## 构建与测试

```bash
go build ./...
go vet ./...
go test ./...
```

## 提交前自查

- **不要提交任何真实凭证**（token / AppCode / 密码）。测试中的真实网关调用必须走环境变量，未设置时跳过；详见 [SECURITY.md](./SECURITY.md)。
- 改 `internal/catalog/data/` 下的接口元数据时，同步运行 `cmd/catalog-dump` 验证一致性。
- 涉及工具 schema 的改动需通过 `internal/mcp` 下的回归测试（`server_schemas_test.go`、`tool_parity_test.go`）。

## 真实网关回归测试（可选）

需要管理员发放的授权，本地配置后执行：

```bash
set -a && source .env && set +a
KEEPRISK_LIVE_TOKEN=<管理员发放的测试token> \
  KEEPRISK_LIVE_JUDICIAL=1 go test -run TestLiveJudicialDeepDiveChain ./internal/mcp/ -v
```

## 行为准则

保持友善与专业。功能讨论请在 Issue / Discussion 中进行。
