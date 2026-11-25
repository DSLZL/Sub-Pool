import { glossary } from "@/lib/glossary";
import { Button, Callout, ConfirmModal, Field, Input, KV, Modal, Mono } from "@/components/ui";
import { grantNames, type ConnectionConsole } from "./use-connection-console";

export function ConnectionDialogs({ view }: { view: ConnectionConsole["dialogs"] }) {
  const {
    grantConfirm, closeGrant, updateWriteGrant, locked, grantPassword, grantView, enablingGrant, saved, setGrantPassword, notice,
    urlConfirm, closeAddressChange, saveConnection, impact, url, time,
    clearConfirm, closeCredentialClear, addOpen, closeAccount, editingName, pending, addName, adopt, setAddName,
  } = view;
  return (
    <>
    <ConfirmModal
      open={grantConfirm !== null}
      onClose={closeGrant}
      onConfirm={updateWriteGrant}
      disabled={locked || !grantPassword || !grantView?.source_id}
      title={grantConfirm ? (enablingGrant ? "开启" : "撤销") + grantNames[grantConfirm] + "授权" : "写入授权"}
      danger={!enablingGrant}
      confirmLabel={enablingGrant ? "确认开启" : "确认撤销"}
    >
      <p className="break-all">当前来源 <Mono>{saved?.base_url}</Mono></p>
      <p>{grantView?.confirmation_text}</p>
      {!enablingGrant && <p>撤销生效后停止新的派发。已发送的请求仍需核对结果。</p>}
      <Field label="当前管理员密码">
        <Input type="password" aria-label="当前管理员密码" autoComplete="current-password" autoFocus maxLength={128} disabled={locked}
          value={grantPassword} onChange={event => setGrantPassword(event.target.value)} />
      </Field>
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
    </ConfirmModal>
    <ConfirmModal
      open={urlConfirm}
      onClose={closeAddressChange}
      onConfirm={() => saveConnection()}
      disabled={locked || !impact}
      title={`确认更换 ${glossary.sub2api} 地址`}
      phrase="更换地址"
      confirmLabel="确认更换"
    >
      <p>新地址：<Mono>{url}</Mono></p>
      <ul className="list-disc space-y-1 pl-5 text-muted">
        <li>{impact?.managed_accounts ?? "未知数量的"} 个原目标已纳管账号、{impact?.participant_bindings ?? "未知数量的"} 个未结束用户绑定将保留旧归属，需重新核对</li>
        <li>截至 {time(impact?.checked_at)}，本地待核对调度 {impact?.dispatch_open ?? "未知"} 条、计价 {impact?.pricing_open ?? "未知"} 条；{glossary.balanceAdjustment}已拒绝且未下发 {impact?.wallet_rejected ?? "未知"} 条、其他需核对 {impact?.wallet_unconfirmed ?? "未知"} 条。未记录的原生操作数量未知；旧建议与确认须重新核验，不会在新目标重放旧任务</li>
        <li>没有输入新凭据时将清除旧凭据，旧秘密不会随地址迁移</li>
      </ul>
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
    </ConfirmModal>
    <ConfirmModal
      open={clearConfirm}
      onClose={closeCredentialClear}
      onConfirm={() => saveConnection(true)}
      disabled={locked}
      title="清除管理凭据"
      danger
      confirmLabel="清除"
    >
      清除已保存目标的凭据；正在执行的请求不会被宣称已撤回，后续读取将不可用，直到重新配置。
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
    </ConfirmModal>
    <Modal
      open={!!addOpen}
      onClose={closeAccount}
      title={editingName ? "编辑本地账号名称" : `纳管 ${glossary.sub2api} 账号`}
      footer={
        <>
          <Button disabled={!!pending} onClick={closeAccount}>取消</Button>
          <Button
            variant="primary"
            disabled={locked || !addName.trim()}
            onClick={() => void adopt()}
          >
            {editingName ? "保存" : "纳管"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <KV items={[[`${glossary.sub2api} ID`, <Mono key="i">{addOpen?.upstream_id}</Mono>], ["类型", addOpen && "snapshot" in addOpen ? addOpen.snapshot.account.type : addOpen?.type]]} />
        <Field label="本地显示名称">
          <Input value={addName} disabled={locked} maxLength={128} onChange={(e) => setAddName(e.target.value)} />
        </Field>
        <Callout tone="info">{editingName ? `仅修改本地显示名称，不改变 ${glossary.sub2api} 身份、来源方式或本地监控状态。` : `纳管会核对 ${glossary.sub2api} 身份，默认使用被动快照；主动查询暂不可用。本地编辑不会创建或重绑 ${glossary.sub2api} 身份。`}</Callout>
        {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      </div>
    </Modal>
    </>
  );
}
