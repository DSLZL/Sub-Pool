import { glossary } from "@/lib/glossary";
import { Button, Callout, ConfirmModal, Field, Input, KV, Modal, Mono, Val } from "@/components/ui";
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
      {!enablingGrant && <p>撤销后停止新写入，已发送的请求须核对。</p>}
      <Field label="本人密码">
        <Input type="password" aria-label="本人密码" autoComplete="current-password" autoFocus maxLength={128} disabled={locked}
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
        <li>旧账号 <Val v={impact?.managed_accounts} /> · 绑定 <Val v={impact?.participant_bindings} /> · 请重新核对</li>
        <li>待核对 · 暂停 <Val v={impact?.dispatch_open} /> · 计价 <Val v={impact?.pricing_open} /> · 调整 <Val v={impact?.wallet_unconfirmed} /></li>
        <li>调整已拒绝 <Val v={impact?.wallet_rejected} /> · 截至 {time(impact?.checked_at)}</li>
        <li>在原地址核对旧请求。留空会清除旧凭据。</li>
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
      清除后须重填。
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
    </ConfirmModal>
    <Modal
      open={!!addOpen}
      onClose={closeAccount}
      title={editingName ? "编辑账号名" : `纳管 ${glossary.sub2api} 账号`}
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
        <Field label="本地名称">
          <Input value={addName} disabled={locked} maxLength={128} onChange={(e) => setAddName(e.target.value)} />
        </Field>
        {!editingName && <Callout tone="info">纳管后采集额度。</Callout>}
        {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      </div>
    </Modal>
    </>
  );
}
