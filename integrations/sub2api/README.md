# Sub2API 资金一致性修复补丁

[atomic-finance.patch](atomic-finance.patch) 针对 Sub2API 固定提交 `d851a38477597157e78450d30af2d8504764d085`（0.2.19）。补丁包含应用代码和对应回归测试，不增加依赖或修改数据库结构。

## 修复行为

- 标准计费将完整消费记录、钱包或订阅扣减、额度和去重标记放在同一事务中。消费记录写入失败时整笔回滚；重复计费键不再次扣款。
- 管理员余额修改将余额、调整历史和成功回执放在同一事务中。历史或回执失败时整笔回滚；提交结果不明时保留原请求键，供后续查询和重放。
- 余额接口强制要求 `Idempotency-Key`，存储或事务能力不可用时拒绝执行。原生前端在发送前保存请求键，失败和页面重载后重用，成功后清除。外部调用方也必须提供并保留原键。
- 已过期的处理中租约允许安全重试；事务内锁定并校验租约，阻止旧持有者在回收后执行。余额缓存于事务提交后失效。

## 应用与构建

在独立的 Sub2API Git 工作区检出上述固定提交后，使用本补丁的绝对路径：

```powershell
git apply --check C:/path/to/Sub-Pool/integrations/sub2api/atomic-finance.patch
git apply C:/path/to/Sub-Pool/integrations/sub2api/atomic-finance.patch
```

使用该版本已有的构建流程，准备 Go、Bun 和 pnpm（前端构建脚本会调用 pnpm）。以下命令在 Sub2API 工作区运行：

```powershell
Set-Location frontend
bun install --frozen-lockfile
bun run build
Set-Location ../backend
go build -tags embed -trimpath -ldflags "-X main.Version=0.2.19-pool-funds.2 -X main.Commit=d851a384+atomic-finance -X main.BuildType=source" -o bin/sub2api-fixed ./cmd/server
```

## 回归测试

后端在 Sub2API 的 `backend` 目录运行：

```powershell
go test -tags unit ./internal/repository -run 'Test(UsageBilling|AtomicIdempotency)' -count=1
go test -tags unit ./internal/service -run 'RecordUsage|UsageBilling|Idempotency|AdminService_UpdateUserBalance|GeminiCachedContent|ChannelPricing|RecordCyberPolicyUsageLog|AstraForwardPreservesMax|GeminiNativeReasoningPricing|GrokResponseAudit|GrokVideoBilling' -count=1
go test -tags unit ./internal/handler/admin -count=1
```

真实 PostgreSQL 测试使用已迁移的、可丢弃的本地数据库。数据库名称必须以 `subpool_test_` 开头，地址必须为回环地址；测试创建合成账户和故障触发器，不清理合成账户。未提供连接串时此项会明确跳过：

```powershell
$env:SUB2API_FINANCE_TEST_DSN = 'postgres://USER:PASSWORD@127.0.0.1:PORT/subpool_test_finance?sslmode=disable'
go test -tags unit ./internal/repository -run '^TestFinancialAtomicityPostgres$' -count=1 -v
Remove-Item Env:SUB2API_FINANCE_TEST_DSN
```

前端在 `frontend` 目录运行：

```powershell
bun run typecheck
bun x --no-install eslint src/api/admin/users.ts src/api/__tests__/admin.users.balance.spec.ts
bun x --no-install vitest run src/api/__tests__/admin.users.balance.spec.ts src/components/admin/user/__tests__/UserBalanceModal.spec.ts
```

## 边界

成功回执仍使用原有的有限保留期（默认 24 小时），不是永久资金流水。超过保留期且结果未知的操作应先人工核账，不能直接重试或更换请求键。绝对值 `set` 仍没有版本比较保护，不能用它根据旧余额并发结算。

修复仅对之后的写入生效，无法还原先前缺失的消费记录。日志写入故障时本地计费回滚，但上游请求可能已完成并产生上游成本，需要运营侧对账处理。

部署本补丁后的 Sub2API 仍需单独完成 Sub-Pool 资金能力接入验收；完整共享钱包消费范围、结算凭证、长期幂等和并发版本控制不在本补丁范围内。
