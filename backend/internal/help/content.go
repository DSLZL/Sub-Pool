package help

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

const Version = "help-v1"

type Guide struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Minutes  int      `json:"minutes"`
	Audience string   `json:"audience"`
	Steps    []string `json:"steps"`
	Feature  string   `json:"-"`
}
type Announcement struct {
	ID            string     `json:"id"`
	ContentSHA256 string     `json:"content_sha256"`
	Title         string     `json:"title"`
	PublishedAt   string     `json:"published_at"`
	Body          string     `json:"body"`
	ReadAt        *time.Time `json:"read_at"`
}
type Catalog struct {
	Version       string         `json:"version"`
	Guides        []Guide        `json:"guides"`
	Announcements []Announcement `json:"announcements"`
}

func guides() []Guide {
	return []Guide{
		{ID: "authority", Title: "登录、权限与结果核对", Minutes: 2, Audience: "all", Steps: []string{
			"使用本系统账号登录。管理员配置普通用户可读功能和账号、参与者范围；看到菜单或持有 API Key 不会增加权限。",
			"退出、停用、密码变更或权限收缩后，旧页面与弹窗失效。个人 Key 仅在签发或轮换时显示一次；不要把密码或 Key 写进共享记录。",
			"页面报错时，可以把请求编号交给管理员。管理员可以在服务日志中查找对应记录。",
			"失败、未知和成功是不同状态。超时后先读取原记录核对，不重复提交可能已生效的操作。生产读取失败不会改用演示数据。",
		}},
		{ID: "connection", Title: "连接与被动采集", Minutes: 3, Audience: "admin", Steps: []string{
			"在连接页面保存 Sub2API 地址和管理凭据，再执行真实连接测试。已保存秘密不回显，也不交给浏览器直连上游。",
			"纳管账号必须对应当前来源与原生身份。被动采集保留服务端实际快照、来源时间和失败记录；上次成功读数不代表当前请求成功。",
			"采集设置可以独立启用用量采集和保存频率。在用量与费用的费用明细或参与者与额度池的消费归属中，选择账号和日期后可立即采集。",
			"用量任务按 UTC 日期显示已读数量、等待原因和最近完成时间。完成表示已复核上游可见的原生记录，缺日及历史扣费仍需独立证据。",
			"主动探测可能影响上游周期。未核验重置等副作用前不可启用；本地测试通过不代表目标部署已经核验。",
		}},
		{ID: "shares", Title: "参与者与本地份额", Minutes: 2, Audience: "scoped", Feature: "pools", Steps: []string{
			"管理员将参与者绑定到真实上游用户，并保存本地额度池与份额契约。普通用户只能看到当前获授权范围。",
			"份额是本地分配意图，不会修改上游余额。历史事实按当时绑定和契约归属；未知归属不能猜给某位参与者。",
			"账号额度、钱包、冻结与订阅是不同事实。不能把账号剩余额度直接当成可发放的钱包金额。",
		}},
		{ID: "estimates", Title: "费用事实与估计边界", Minutes: 3, Audience: "scoped", Feature: "estimates", Steps: []string{
			"估计页区分官方与人工区间、来源时间、样本和生效条件。条件变化后需重新读取或重算；失效样本不能继续作为当前结果。",
			"当前尚未完成区间费用覆盖、历史计价和模型校准，因此容量与剩余金额保持未知。空值不是零，描述性日月统计不是完整财务结算。",
			"分组倍率、原生实际费用和条件估计各有口径。standard 模式本身不能证明计价可写；不要据此调整真实钱包。",
		}},
		{ID: "wallets", Title: "钱包、冻结与临时模式", Minutes: 2, Audience: "scoped", Feature: "balances", Steps: []string{
			"钱包和冻结保持上游原生单位与精度，分别显示状态；不要直接相减，或把它们与账号额度相加。",
			"全局费用、合法正目标、并发与冻结权属未核验时，不产生可执行建议。当前应用请求只保存本地 rejected/not_dispatched 结果，没有远端写入。",
			"临时模式目前不可启用。订阅不等于可变钱包；恢复普通建议也不等于已回收远端临时钱包。零目标没有备用写入路径。",
		}},
		{ID: "temporary", Title: "为何临时额度仍不可用", Minutes: 1, Audience: "scoped", Feature: "temporary", Steps: []string{
			"当前缺少已核验的原生钱包计价、冻结权属、全局合法建议及回收条件，服务端明确保持临时模式不可用。",
			"页面只呈现真实条件和保留结果。演示数值、计时结束或点击确认都不能启用生产临时额度。",
		}},
		{ID: "recovery", Title: "受保护备份与恢复核对", Minutes: 3, Audience: "admin", Steps: []string{
			"导出的是本系统业务数据，不是 Sub2API 数据库，也不能直接导入 Sub2Pool SQLite。秘密文件需要保留服务端保护密钥，并在数据库之外安全保存。",
			"只接受匹配当前迁移与物理结构的受保护文件。升级后旧结构文件可能不兼容；先保留匹配版本与恢复路径，不自动转换。",
			"上传实际文件做预检，下载并核对恢复前副本后再确认。采集中、存在未确认远端操作、权限失效或当前数据已变化时，不能继续套用旧预检。",
			"恢复会清除旧会话、撤销导入 Key、暂停采集和邮件。用备份内管理员重新登录，读取原恢复记录，再测试已保存连接。人工已核对不会恢复任务或重放旧通知，也不能撤销已发生的上游写入。",
		}},
	}
}

func announcements() []Announcement {
	out := []Announcement{
		{ID: "runtime-readiness-v1", Title: "请求编号和就绪检查", PublishedAt: "2026-11-20T00:00:00Z", Body: "页面报错时会显示可查询的请求编号。部署人员可以查看进程状态，以及数据库和后台任务是否就绪。"},
		{ID: "native-safety-v1", Title: "当前业务范围与未启用能力", PublishedAt: "2026-11-08T00:00:00Z", Body: "本版本提供真实身份权限、被动采集、保留事实与本地配置。容量估计仍缺少完整费用与校准；钱包写入、临时额度、计价变更和自动暂停恢复尚未核验，不会因页面存在按钮而启用。真实请求失败不会回退合成演示。"},
		{ID: "protected-recovery-v1", Title: "恢复后需要重新登录并核对连接", PublishedAt: "2026-11-08T00:00:00Z", Body: "受保护业务恢复要求匹配当前结构、真实预检与恢复前副本。恢复后旧会话被清除、导入 Key 撤销、监控和邮件暂停；需要重新登录并显式测试当前保存的上游连接。人工核对仅保存个人确认，不自动恢复任务或重放旧远端操作。"},
	}
	for i := range out {
		body, _ := json.Marshal([]string{out[i].ID, out[i].Title, out[i].PublishedAt, out[i].Body})
		out[i].ContentSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	return out
}
