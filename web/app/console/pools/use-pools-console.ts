"use client";

import { glossary } from "@/lib/glossary";
import { useEffect, useState } from "react";
import { createAccountsClient, createConnectionClient, createParticipantsClient, createPoolsClient } from "@sub-pool/contracts/client";
import { participantCreateInput, participantUpdateInput, usageHistoryRange, type Participant, type ParticipantExit } from "@sub-pool/contracts/participants";
import { poolCreateInput, poolMemberLimit, poolUpdateInput, type PoolContract } from "@sub-pool/contracts/pools";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { ConnectionConfiguration } from "@sub-pool/contracts/connection";
import { connectionFailure, displayTime, readManagedPages } from "@/lib/connection-data";
import { checkParticipantExit, checkParticipantDeletion, checkParticipantSave, checkPoolSave, readCandidatePages, readParticipantPages } from "@/lib/pools-data";
import { requestFailureText } from "@/lib/request-id";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { useGuardedMutation } from "@/lib/use-guarded-mutation";
import { usePoolData } from "@/lib/use-pool-data";
import { useRecordedUsage, type RecordedUsageQuery } from "@/lib/use-recorded-usage";

type MemberForm = { original: Participant | null; name: string; contact: string; notes: string; upstreamID: string; enabled: boolean; sourceID: string; connectionRevision: string };
type PoolForm = { original: PoolContract | null; name: string; enabled: boolean; accountIDs: string[] };
type NativeUser = NonNullable<Participant["binding"]["identity"]>;
const emptyUsageDraft = { accountID: "", participantID: "", startAt: "", endAt: "" };
const missingConnection = "请先在连接页保存可用的管理连接。";

export function usePoolsConsole() {
  const { auth, toast, refreshSession, displayTimezone } = useApp();
  const scope = useScope();
  const { isAdmin, participantCatalog: members } = scope;
  const [tab, setTab] = useState("pools");
  const [usageDraft, setUsageDraft] = useState(emptyUsageDraft);
  const [usageQuery, setUsageQuery] = useState<{ accountID: string; window: RecordedUsageQuery } | null>(null);
  const [usageError, setUsageError] = useState<string | null>(null);
  const poolData = usePoolData(tab === "history");
  const recorded = useRecordedUsage(scope, usageQuery?.accountID || undefined, usageQuery?.window);
  const [weeklyPool, setWeeklyPool] = useState<{ id: string; name: string; epoch: number } | null>(null);
  const [poolForm, setPoolForm] = useState<PoolForm | null>(null);
  const editPool = poolForm?.original ?? null;
  const [editMembers, setEditMembers] = useState<Participant[]>([]);
  const [editAccounts, setEditAccounts] = useState<ManagedAccount[]>([]);
  const [editConfig, setEditConfig] = useState<ConnectionConfiguration | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [editMember, setEditMember] = useState<MemberForm | null>(null);
  const [userQ, setUserQ] = useState("");
  const [upstreamUsers, setUpstreamUsers] = useState<NativeUser[]>([]);
  const [candidateError, setCandidateError] = useState<string | null>(null);
  const [candidatesReady, setCandidatesReady] = useState(false);
  const [exitTarget, setExitTarget] = useState<Participant | null>(null);
  const [exitAttempt, setExitAttempt] = useState<{ target: Participant; acknowledgement: ParticipantExit | null } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Participant | null>(null);
  const [deleteAttempt, setDeleteAttempt] = useState<Participant | null>(null);
  const [stale, setStale] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const { activate, run, isBusy, setBlocked, blocked, pending: operation } = useGuardedMutation();
  useEffect(activate, [activate]);
  const opening = operation === "open";
  const pending = operation === "submit";
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);

  const reload = async () => {
    if (isBusy()) return;
    if (deleteAttempt) { setDeleteTarget(deleteAttempt); return; }
    if (exitAttempt) { setExitTarget(exitAttempt.target); return; }
    recorded.reload();
    await Promise.all([poolData.reload(), scope.reloadAccounts(), scope.reloadParticipants()]);
  };

  const fail = (cause: unknown, confirmed: string | null = null, dispatched = false) => {
    const failure = connectionFailure(cause);
    let text = failure.text;
    if (confirmed) text = requestFailureText(failure.text, `${confirmed}；回读尚未完成，请重新读取核对，勿重复提交。`);
    else if (!dispatched && failure.status === 0) text = requestFailureText(failure.text, "暂时无法完整读取编辑依据，请重新读取后再操作。");
    setError(text);
    setNotice(text);
    if (confirmed || dispatched && failure.status === 0) setBlocked(true);
    if ([404, 409].includes(failure.status)) setStale(true);
    if ([401, 403].includes(failure.status)) {
      setEditMember(null);
      setPoolForm(null);
      setExitTarget(null);
      setDeleteTarget(null);
      setDeleteAttempt(null);
      void refreshSession();
    }
  };

  const openEditor = <T,>(read: (signal: AbortSignal) => Promise<T>, apply: (value: T) => void) =>
    run({
      name: "open",
      action(signal) {
        setError(null);
        setStale(false);
        return read(signal);
      },
      apply,
      onFailure: fail,
    });

  const openPool = (pool: PoolContract | null = null) => {
    if (!isAdmin) return;
    return openEditor(async signal => {
      const [value, config] = await Promise.all([
        pool ? createPoolsClient(window.location.origin).get({ id: pool.pool_id }, { signal }) : Promise.resolve(null),
        createConnectionClient(window.location.origin).get({}, { signal }),
      ]);
      signal.throwIfAborted();
      if (!config.source_id || !config.credential_set) return null;
      const [accounts, participants] = await Promise.all([
        readManagedPages(after => createAccountsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal }), config.revision),
        readParticipantPages(after => createParticipantsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal })),
      ]);
      return { value, config, accounts, participants };
    }, current => {
      if (!current) { setNotice(missingConnection); return; }
      const { value, config, accounts, participants } = current;
      setPoolForm({ original: value, name: value?.name ?? "", enabled: value?.enabled ?? true, accountIDs: value?.accounts.map(account => account.id) ?? [] });
      setEditConfig(config);
      setEditAccounts(accounts);
      setEditMembers(participants);
      const ids = new Set([...participants.filter(member => member.enabled).map(member => member.id), ...value?.participants.map(member => member.id) ?? []]);
      setDraft(Object.fromEntries([...ids].map(id => [id, value?.participants.find(member => member.id === id)?.share_percent ?? ""])));
    });
  };

  const openMember = (member: Participant | null = null) => {
    if (!isAdmin || !scope.participantCatalogAvailable) return;
    return openEditor(async signal => {
      setUserQ("");
      const [config, value] = await Promise.all([
        createConnectionClient(window.location.origin).get({}, { signal }),
        member ? createParticipantsClient(window.location.origin).get({ id: member.id }, { signal }) : Promise.resolve(null),
      ]);
      if (!config.source_id || !config.credential_set) return null;
      return {
        original: value, name: value?.name ?? "", contact: value?.contact ?? "", notes: value?.notes ?? "", enabled: value?.enabled ?? true,
        upstreamID: value?.source_current ? value.binding.upstream_user_id : "", sourceID: config.source_id, connectionRevision: config.revision,
      };
    }, form => { if (form) setEditMember(form); else setNotice(missingConnection); });
  };

  const candidateSource = editMember?.sourceID, candidateRevision = editMember?.connectionRevision;
  useEffect(() => {
    const abort = new AbortController();
    const { signal } = abort;
    setUpstreamUsers([]);
    setCandidateError(null);
    setCandidatesReady(false);
    if (!isAdmin || !candidateSource || !candidateRevision || pending) return;
    if ([...userQ].length > 100 || new TextEncoder().encode(userQ).length > 256) {
      setCandidateError("搜索内容过长，请缩短后再查询。");
      return;
    }
    void readCandidatePages(
      page => createParticipantsClient(window.location.origin).candidates({ source_id: candidateSource, revision: candidateRevision, page, search: userQ }, { signal }),
      candidateSource, candidateRevision,
    ).then(value => {
      if (signal.aborted) return;
      setUpstreamUsers(value.users);
      setCandidatesReady(value.read.state === "available");
      if (value.read.state !== "available") setCandidateError(`当前连接无法确认用户读取能力（${value.read.state} / ${value.read.reason}）。`);
    }).catch(cause => {
      if (signal.aborted) return;
      const failure = connectionFailure(cause);
      setCandidateError(failure.status === 0 ? requestFailureText(failure.text, "候选用户未完整读取，请重新查询。") : failure.text);
      if ([401, 403].includes(failure.status)) { setEditMember(null); void refreshSession(); }
    });
    return () => abort.abort();
  }, [isAdmin, candidateSource, candidateRevision, userQ, pending, refreshSession]);

  const saveShares = async () => {
    if (!isAdmin || !poolForm || !editConfig?.source_id || blocked || stale || isBusy()) return;
    const fields = { name: poolForm.name, enabled: poolForm.enabled, source_id: editConfig.source_id, connection_revision: editConfig.revision,
      accounts: poolForm.accountIDs.map(id => ({ id, revision: editAccounts.find(account => account.id === id)?.revision })),
      participants: Object.entries(draft).filter(([, value]) => value !== "").map(([id, share_percent]) => ({ id, share_percent, revision: editMembers.find(member => member.id === id)?.revision })),
    };
    const input = editPool
      ? { kind: "update" as const, ...poolUpdateInput.safeParse({ ...fields, id: editPool.pool_id, revision: editPool.revision }) }
      : { kind: "create" as const, ...poolCreateInput.safeParse(fields) };
    if (!input.success) {
      setError(fields.participants.length > poolMemberLimit ? "单份额度池合同最多包含 10,000 名成员。" : "请核对池名称、至少一个当前账号及成员份额。百分比最多六位小数，合计不超过 100%。");
      return;
    }
    let confirmed: string | null = null;
    await run({
      name: "submit",
      async action(signal) {
        setError(null);
        const client = createPoolsClient(window.location.origin);
        const value = input.kind === "update" ? await client.update(input.data, { signal }) : await client.create(input.data, { signal });
        signal.throwIfAborted();
        checkPoolSave(value, input.data);
        confirmed = `合同 v${value.revision} 已保存`;
        const saved = await client.contract({ id: value.id }, { signal });
        checkPoolSave(saved, input.data, value);
        return { saved, title: confirmed };
      },
      apply({ saved, title }) {
        setPoolForm(null);
        setNotice(null);
        toast({ tone: "ok", title, body: `实际生效 ${saved.effective_at}；既有记录保留原依据，未写入钱包。` });
        void poolData.reload();
      },
      onFailure: cause => fail(cause, confirmed, true),
    });
  };

  const saveMember = async () => {
    if (!isAdmin || !editMember || blocked || stale || isBusy()) return;
    const fields = { source_id: editMember.sourceID, connection_revision: editMember.connectionRevision, upstream_id: editMember.upstreamID, name: editMember.name, contact: editMember.contact, notes: editMember.notes, enabled: editMember.enabled };
    if (!participantCreateInput.safeParse(fields).success) { setError(`请核对名称、联系信息、说明及明确选择的 ${glossary.sub2api} 用户；名称最多 128 字，内容不能含控制字符。`); return; }
    const original = editMember.original;
    let confirmed: string | null = null;
    await run({
      name: "submit",
      async action(signal) {
        setError(null);
        const client = createParticipantsClient(window.location.origin);
        const result = original ? await client.update(participantUpdateInput.parse({ ...fields, id: original.id, revision: original.revision }), { signal }) : await client.create(fields, { signal });
        signal.throwIfAborted();
        if (!result.participant) return result;
        checkParticipantSave(result.participant, fields, original);
        confirmed = "参与者已保存";
        const saved = await client.get({ id: result.participant.id }, { signal });
        checkParticipantSave(saved, fields, original, result.participant);
        return { ...result, participant: saved };
      },
      apply({ participant, read }) {
        if (!participant) { setError(`当前连接未通过用户读取核对（${read?.state} / ${read?.reason}），未保存参与者。`); return; }
        setEditMember(null);
        setNotice(null);
        toast({ tone: "ok", title: "参与者已保存", body: "服务端已读回；参与者不因此获得任何登录权限。" });
        void scope.reloadParticipants();
        void poolData.reload();
      },
      onFailure: cause => fail(cause, confirmed, true),
    });
  };

  const openExit = (member: Participant) => {
    if (!isAdmin || isBusy()) return;
    if (exitAttempt) { setExitTarget(exitAttempt.target); return; }
    return openEditor(signal => createParticipantsClient(window.location.origin).get({ id: member.id }, { signal }), setExitTarget);
  };

  const exitMember = async () => {
    if (!isAdmin || !exitTarget || stale && !exitAttempt) return false;
    const recovering = exitAttempt !== null;
    const target = exitAttempt?.target ?? exitTarget;
    let acknowledgement = exitAttempt?.acknowledgement ?? null;
    await run({
      name: "submit",
      recoveryRead: recovering,
      async action(signal) {
        setError(null);
        setExitAttempt({ target, acknowledgement });
        const client = createParticipantsClient(window.location.origin);
        if (!recovering) {
          const value = await client.exit({ id: target.id, revision: target.revision, confirmation: target.name }, { signal });
          signal.throwIfAborted();
          checkParticipantExit(value, target);
          acknowledgement = value;
          setExitAttempt({ target, acknowledgement });
        }
        const receipt = await client.exitResult({ id: target.id, revision: target.revision }, { signal });
        checkParticipantExit(receipt, target, acknowledgement);
        return receipt;
      },
      apply(receipt) {
        setExitAttempt(null);
        setExitTarget(null);
        setBlocked(false);
        setNotice(null);
        toast({ tone: "ok", title: "退出已保存并核对", body: `生效 ${receipt.effective_at}；${receipt.pool_contract_ids.length} 个池已保留新合同，已记录历史不变。` });
        void scope.reloadParticipants();
        void poolData.reload();
      },
      onFailure(cause) {
        const failure = connectionFailure(cause);
        if ([401, 403].includes(failure.status) || !recovering && !acknowledgement && [400, 404, 409, 429].includes(failure.status)) {
          setExitAttempt(null);
          fail(cause);
          return;
        }
        const text = `退出结果尚未完成核对。${failure.text} 确认按钮只读取退出记录，不会重复提交。`;
        setBlocked(true);
        setError(text);
        setNotice(text);
      },
    });
    return false;
  };

  const openDelete = (member: Participant) => {
    if (!isAdmin || isBusy()) return;
    if (deleteAttempt) { setDeleteTarget(deleteAttempt); return; }
    return openEditor(signal => createParticipantsClient(window.location.origin).get({ id: member.id }, { signal }), setDeleteTarget);
  };

  const deleteMember = async () => {
    if (!isAdmin || !deleteTarget || stale && !deleteAttempt) return false;
    const recovering = deleteAttempt !== null;
    const target = deleteAttempt ?? deleteTarget;
    let acknowledged = recovering;
    await run({
      name: "submit",
      recoveryRead: recovering,
      async action(signal) {
        setError(null);
        setDeleteAttempt(target);
        const client = createParticipantsClient(window.location.origin);
        if (!recovering) {
          await client.delete({ id: target.id, revision: target.revision, confirmation: target.name }, { signal });
          signal.throwIfAborted();
          acknowledged = true;
        }
        const receipt = await client.deleteResult({ id: target.id, revision: target.revision }, { signal });
        checkParticipantDeletion(receipt, target);
        return receipt;
      },
      apply(receipt) {
        setDeleteAttempt(null);
        setDeleteTarget(null);
        setBlocked(false);
        setNotice(null);
        toast({ tone: "ok", title: "删除已保存并核对", body: `生效 ${receipt.effective_at}；本地身份与审计保留，${glossary.sub2api} 用户未删除。` });
        void scope.reloadParticipants();
        void poolData.reload();
        recorded.reload();
      },
      onFailure(cause) {
        const failure = connectionFailure(cause);
        if ([401, 403].includes(failure.status) || !acknowledged && [400, 404, 409, 429].includes(failure.status)) {
          setDeleteAttempt(null);
          fail(cause);
          return;
        }
        const text = `删除结果尚未完成核对。${failure.text} 确认按钮只读取删除记录，不会重复提交。`;
        setBlocked(true);
        setError(text);
        setNotice(text);
      },
    });
    return false;
  };

  const queryUsage = () => {
    const range = usageHistoryRange.safeParse({ start_at: usageDraft.startAt, end_at: usageDraft.endAt });
    if (!range.success) { setUsageError("请输入有效的 UTC 起止时间（Z 结尾，起点早于终点，最多 366 天，结束不含）。"); return; }
    setUsageError(null);
    setUsageQuery({ accountID: usageDraft.accountID, window: { ...range.data, participant_id: usageDraft.participantID } });
  };

  const clearUsage = () => {
    setUsageQuery(null);
    setUsageDraft(emptyUsageDraft);
    setUsageError(null);
  };

  return {
    tab, setTab, opening, pending, blocked, reload, openPool, openMember, time,
    problem: notice ?? poolData.error ?? scope.participantCatalogError,
    canCreatePool: poolData.ready && scope.accountCatalogAvailable,
    canCreateMember: scope.participantCatalogAvailable,
    weeklyPool: isAdmin && weeklyPool?.epoch === auth.epoch ? weeklyPool : null,
    closeWeeklyPool: () => setWeeklyPool(null),
    poolList: {
      poolData, isAdmin, members, pending, opening, blocked, time, openPool,
      openWeeklyPool: (pool: PoolContract) => setWeeklyPool({ id: pool.pool_id, name: pool.name, epoch: auth.epoch }),
    },
    participantList: { scope, pools: poolData.pools, isAdmin, members, pending, opening, blocked, exitAttempt, deleteAttempt, openMember, openExit, openDelete },
    spend: { scope, isAdmin, members, recorded, usageQuery, usageDraft, usageError, setUsageDraft, queryUsage, clearUsage, time },
    history: poolData,
    dialogs: {
      isAdmin, pending, blocked, stale, error, poolForm, editPool, editMembers, members, editAccounts, draft, editMember,
      userQ, upstreamUsers, candidateError, candidatesReady, exitTarget, exitAttempt, deleteTarget, deleteAttempt,
      setPoolForm, setDraft, setEditMember, setUserQ, saveShares, saveMember, exitMember, deleteMember,
      closePool: () => { if (!isBusy()) setPoolForm(null); },
      closeMember: () => { if (!isBusy()) setEditMember(null); },
      closeExit: () => { if (!isBusy()) setExitTarget(null); },
      closeDelete: () => { if (!isBusy()) setDeleteTarget(null); },
    },
  };
}

export type PoolsConsole = ReturnType<typeof usePoolsConsole>;
