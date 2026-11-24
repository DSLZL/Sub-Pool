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
			"收到初始口令后，按登录提示先修改密码，再用新密码登录。管理员在「用户与权限」授予普通用户可读的功能、账号和参与者范围。",
			"退出、停用、密码变更或权限收缩后，旧页面与弹窗失效。个人 Key 仅在签发或轮换时显示一次，请存入自己的凭据配置。",
			"页面报错时，可以把请求编号交给管理员。管理员可在服务日志中查找对应记录。",
			"超时后先读取原记录核对结果。可能已生效的操作应等待确认后再决定下一步。",
		}},
		{ID: "connection", Title: "连接、采集与写入授权", Minutes: 4, Audience: "admin", Steps: []string{
			"在「连接与采集 → Sub2API 连接」保存地址和管理凭据，再测试连接。到「能力检查」分别核对访问、鉴权和各项能力。",
			"兼容范围为原版 Sub2API 0.2.23。未核验的版本会在诊断中标明，可用管理员 JWT 核对运行模式。",
			"在「账号纳管」确认当前来源和原生账号身份。在「账号与额度」核对上游快照时间和最近失败记录。读数过期时等待上游更新后再核对。",
			"在「采集设置」启用定时采集。也可在「用量与费用 → 费用明细」或「参与者与额度池 → 消费归属」选择账号和日期后立即采集。",
			"用量任务按 UTC 日期保留进度、等待原因和最近完成时间。采集完成表示已复核上游可见记录。缺日或缺少扣费依据时，先补齐记录再用于结算。",
			"钱包、分组计价和整账号暂停的写入授权默认关闭。需要启用时，在「能力检查」先核对当前来源和能力，再用本人密码逐项授权。更换地址后为新来源重新核对。",
			"可以在同一位置撤销授权。已派发的操作继续使用原记录只读核对。整实例需要暂停新的上游写入时，部署人员可启用紧急禁写。",
		}},
		{ID: "shares", Title: "参与者与本地份额", Minutes: 2, Audience: "scoped", Feature: "pools", Steps: []string{
			"管理员将参与者绑定到真实上游用户，并保存本地额度池与份额契约。普通用户只能看到当前获授权范围。",
			"份额是本地分配意图，不会修改上游余额。历史事实按当时绑定和契约归属；未知归属不能猜给某位参与者。",
			"账号额度、钱包、冻结与订阅是不同事实。不能把账号剩余额度直接当成可发放的钱包金额。",
		}},
		{ID: "estimates", Title: "七天估算与费用依据", Minutes: 3, Audience: "scoped", Feature: "estimates", Steps: []string{
			"在「容量估算」选择开启被动监控的 OpenAI OAuth 账号，查看原生七天周期、已记录标准费用、额度读数和采集进度。",
			"通过「查看依据」核对当前周期和采用的样本。区间记录或有效样本不足时，页面会说明等待原因，容量和剩余金额保持未知。",
			"估算金额使用已记录的标准费用口径。查看账单或决定钱包操作时，分别核对实际扣费和相应的钱包依据。",
		}},
		{ID: "wallets", Title: "钱包、余额调整与周额度", Minutes: 3, Audience: "scoped", Feature: "balances", Steps: []string{
			"在「余额建议与应用」查看当前已绑定钱包、冻结读数和应用记录。金额以实际读数与单位为准。",
			"管理员可在原版标准模式、当前能力核验和钱包授权均有效时，对已绑定用户提交单笔余额调整。完成后核对上游流水和独立余额读数。",
			"打开「周额度建议」查看每个账号的原生周期、份额与扣除项。一个参与者的跨池贡献汇总到同一个绑定钱包。缺少任何所需账号的依据时先补齐。",
			"周额度设置中的预发和自动正式分配分别启用，默认关闭。应用结果尚未确认时，到应用记录核对原操作。",
		}},
		{ID: "notifications", Title: "通知种类与投递记录", Minutes: 2, Audience: "scoped", Feature: "notifications", Steps: []string{
			"在「通知 → 通知记录」按授权的参与者或账号查看已排队、渠道已接受、已跳过、失败和结果未知的记录。",
			"管理员在「邮件渠道」保存配置并发送测试邮件，再在「通知种类」启用需要的项目。邮件发往配置的管理员接收地址。",
			"余额低、钱包建议变化和估算显著变化使用当前来源的已核对依据。缺少依据时等待采集或读回，重复提醒按已保存间隔处理。",
		}},
		{ID: "temporary", Title: "临时放宽（规划中）", Minutes: 1, Audience: "scoped", Feature: "temporary", Steps: []string{
			"生产环境的临时放宽尚未开放。启用前需要核验原生钱包计价、冻结权属、全局合法建议和回收条件。",
			"只有显式绑定的隔离模拟环境可以核验和开启临时轮次。",
		}},
		{ID: "recovery", Title: "受保护备份与恢复核对", Minutes: 3, Audience: "admin", Steps: []string{
			"在「备份与恢复」导出本系统的加密业务档案。轻量档保留配置与业务关系，档案默认大小上限为 2 GB。将备份与服务端保护密钥分别保管。",
			"上传后看预检结果，核对归档版本、结构与当前环境。下载并核对恢复前副本，再确认恢复。数据或权限变化后重新预检。",
			"恢复会清除旧会话、撤销导入的 Key 和写入授权，并暂停采集和邮件。使用备份中的管理员重新登录，查看恢复记录并测试当前连接。",
			"按当前环境核对后再逐项恢复任务与授权。原操作结果待确认时先读取已有记录。",
		}},
		{ID: "operations", Title: "服务状态、来源阻断与管理员恢复", Minutes: 3, Audience: "admin", Steps: []string{
			"遇到服务故障时，部署人员查看 /health 与 /health/ready。根据请求编号核对数据库、迁移或后台任务的就绪日志。请求与后台使用独立数据库连接预算。",
			"在「登录与审计 → 来源阻断」填写精确 IP、原因与到期时间。可选择永久阻断。到期后服务自动解除并留下审计。",
			"需要变更管理员角色时，在「用户与权限」用当前管理员本人密码确认。初始口令遗失时，在部署本机执行 sub-pool reset-password <用户名>，通过标准输入提供新密码，并按要求重新登录和改密。",
		}},
	}
}

func announcements() []Announcement {
	out := []Announcement{
		{ID: "native-safety-v1", Title: "生产化更新与写入开放条件", PublishedAt: "2026-11-24T18:00:00Z", Body: "本期兼容原版 Sub2API 0.2.23。用量按账号和日期采集，周额度按各账号的原生周期和份额计算，再汇总到参与者的绑定钱包。钱包调整、分组计价和整账号暂停均默认关闭，在「连接与采集 → 能力检查」核对当前来源与能力后逐项授权。结果待确认时请读取原记录。生产临时放宽仍为规划中。"},
		{ID: "protected-recovery-v1", Title: "恢复后需要重新登录并核对连接", PublishedAt: "2026-11-24T18:00:00Z", Body: "恢复前先完成预检并保存恢复前副本。恢复后旧会话被清除，导入的 Key 和写入授权被撤销，采集及邮件暂停。使用备份中的管理员重新登录，核对恢复记录与当前连接，再按需恢复任务和授权。"},
		{ID: "runtime-readiness-v1", Title: "请求编号和就绪检查", PublishedAt: "2026-11-20T00:00:00Z", Body: "页面报错时会显示可查询的请求编号。部署人员可以查看进程状态，以及数据库和后台任务是否就绪。"},
	}
	for i := range out {
		body, _ := json.Marshal([]string{out[i].ID, out[i].Title, out[i].PublishedAt, out[i].Body})
		out[i].ContentSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	return out
}
