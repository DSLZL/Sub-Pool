# Sub2API 资金一致性补丁

[atomic-finance.patch](atomic-finance.patch) 适用于 Sub2API 0.2.19 的提交 `d851a38477597157e78450d30af2d8504764d085`。在独立的 Sub2API 工作区应用和构建，然后用隔离数据库验证计费及管理余额操作。

## 补丁行为

| 路径 | 改动 |
| --- | --- |
| 标准计费 | 在同一事务中写入完整消费记录、钱包或订阅扣减、额度和去重标记。日志写入失败时回滚该事务。相同计费键只扣一次。 |
| 管理员调整余额 | 在同一事务中保存余额、调整历史和成功的幂等回执。失败时回滚。提交结果不明时继续保留原请求键。 |
| 幂等和租约 | 余额调整必须带 `Idempotency-Key`。前端在发送前保存请求键，在失败或重载后复用它，成功后清除。事务内锁定并校验处理租约，余额缓存于事务提交后失效。 |

补丁包含应用代码和回归测试，使用现有依赖和数据库结构。

## 应用与构建

在干净的 Sub2API 工作区运行以下命令，替换补丁的绝对路径。

```powershell
git switch --detach d851a38477597157e78450d30af2d8504764d085
git apply --check C:/path/to/Sub-Pool/integrations/sub2api/atomic-finance.patch
git apply C:/path/to/Sub-Pool/integrations/sub2api/atomic-finance.patch
```

准备该版本所需的 Go、Bun 和 pnpm。从 Sub2API 工作区根目录构建前端，再构建带嵌入前端的后端。

```powershell
Set-Location frontend
bun install --frozen-lockfile
bun run build
Set-Location ../backend
go build -tags embed -trimpath -ldflags "-X main.Version=0.2.19-pool-funds.2 -X main.Commit=d851a384+atomic-finance -X main.BuildType=source" -o bin/sub2api-fixed ./cmd/server
```

## 回归测试

在 Sub2API 的 `backend` 目录检查仓储、计费与管理接口。

```powershell
go test -tags unit ./internal/repository -run 'Test(UsageBilling|AtomicIdempotency)' -count=1
go test -tags unit ./internal/service -run 'RecordUsage|UsageBilling|Idempotency|AdminService_UpdateUserBalance|GeminiCachedContent|ChannelPricing|RecordCyberPolicyUsageLog|AstraForwardPreservesMax|GeminiNativeReasoningPricing|GrokResponseAudit|GrokVideoBilling' -count=1
go test -tags unit ./internal/handler/admin -count=1
```

事务回归使用已迁移、可丢弃的本地 PostgreSQL 数据库，名称以 `subpool_test_` 开头。测试会创建合成账号和故障触发器，合成记录会留在测试数据库中。

```powershell
$env:SUB2API_FINANCE_TEST_DSN = 'postgres://USER:PASSWORD@127.0.0.1:5432/subpool_test_finance?sslmode=disable'
go test -tags unit ./internal/repository -run '^TestFinancialAtomicityPostgres$' -count=1 -v
Remove-Item Env:SUB2API_FINANCE_TEST_DSN
```

在 `frontend` 目录验证余额请求和表单重试。

```powershell
bun run typecheck
bun x --no-install eslint src/api/admin/users.ts src/api/__tests__/admin.users.balance.spec.ts
bun x --no-install vitest run src/api/__tests__/admin.users.balance.spec.ts src/components/admin/user/__tests__/UserBalanceModal.spec.ts
```

## 使用边界

- 自行调用余额调整接口时，保存并复用同一请求的 `Idempotency-Key`。成功回执按服务设置保留，默认 24 小时。超过保留期且结果未知时先人工核账。
- 绝对值 `set` 缺少版本比较。使用前核对当前余额，并协调并发写入。
- 补丁只影响应用后的写入。上游请求可能已完成并产生费用，即使本地计费因日志写入失败而回滚，也需要运营侧对账。
- 将修补后的 Sub2API 接入 Sub-Pool 时，单独验证钱包的消费覆盖、结算凭证、长期幂等和并发版本控制。相关门禁见 [Sub-Pool 后端说明](../../backend/README.md#上游连接与能力)。
