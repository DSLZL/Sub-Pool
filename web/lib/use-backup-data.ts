"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createBackupsClient } from "@sub-pool/contracts/client";
import { backupStreamFormat, type BackupArchive, type BackupScope, type RestorePlan, type RestoreChecks } from "@sub-pool/contracts/backups";
import { useApp } from "./store";
import { restorePlanID } from "./backup-return";
import { BackupReadError, backupFailure, backupRequestID, checkBackupFile, checkRestoreChecks, checkRestoreReceipt, inspectBackupUpload, readBackups, restoreCanApply, restoreReasons, uploadBackup } from "./backup-data";

type Upload = { name: string } & (Awaited<ReturnType<typeof inspectBackupUpload>> | { archive: BackupArchive });
type Notice = { text: string; unknown: boolean };
type Data = { owner: number; archives: BackupArchive[] | null; keyAvailable: boolean | null; file: Upload | null; plan: RestorePlan | null; checks: RestoreChecks | null; downloaded: string | null; notice: Notice | null; blocked: boolean };
type Uncertain = { kind: "export" | "preflight" | "apply" | "review"; id: string };
export function useBackupData() {
  const { auth, refreshSession } = useApp(), owner = auth.epoch;
  const allowed = auth.status === "authenticated" && auth.access?.all_data === true;
  const empty = (epoch: number): Data => ({ owner: epoch, archives: null, keyAvailable: null, file: null, plan: null, checks: null, downloaded: null, notice: null, blocked: true });
  const [state, setState] = useState<Data>(() => empty(owner));
  const [pending, setPending] = useState<string | null>(null), [now, setNow] = useState(Date.now);
  const live = useRef({ owner, allowed }); live.current = { owner, allowed };
  const request = useRef({ alive: true, generation: 0, busy: false, abort: new AbortController(), known: null as string | null, plan: null as RestorePlan | null, beforeRequested: null as string | null, uncertain: null as Uncertain | null });
  const transfers = useRef(new Set<HTMLIFrameElement>());
  const current = useCallback((generation: number, epoch: number) => request.current.alive && request.current.generation === generation && live.current.owner === epoch && live.current.allowed, []);
  const begin = useCallback((kind: string) => {
    request.current.abort.abort(); request.current.abort = new AbortController(); request.current.busy = true; setPending(kind);
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const finish = useCallback((generation: number, epoch: number) => { if (current(generation, epoch)) { request.current.busy = false; setPending(null); } }, [current]);
  const failure = useCallback((error: unknown) => {
    const result = backupFailure(error);
    if ([401, 403].includes(result.status)) void refreshSession();
    return result;
  }, [refreshSession]);
  function remember(id: string | null) {
    request.current.known = id;
    request.current.beforeRequested = null;
    const url = new URL(window.location.href);
    if (id) url.searchParams.set("restore", id); else url.searchParams.delete("restore");
    window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
  }
  function saveFile(file: { filename: string; content: string }) {
    const url = URL.createObjectURL(new Blob([file.content], { type: "application/octet-stream" }));
    const anchor = document.createElement("a"); anchor.href = url; anchor.download = file.filename;
    document.body.append(anchor); anchor.click(); anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  function saveAttachment(id: string, planID?: string) {
    const frame = document.createElement("iframe");
    frame.hidden = true;
    frame.name = "backup-" + backupRequestID();
    frame.title = "备份下载";
    const form = document.createElement("form");
    form.hidden = true;
    form.target = frame.name;
    form.method = "post";
    form.action = "/api/v1/backups/download";
    for (const [name, value] of Object.entries(planID ? { id, plan_id: planID } : { id })) {
      const input = document.createElement("input");
      input.type = "hidden"; input.name = name; input.value = value;
      form.append(input);
    }
    transfers.current.add(frame);
    document.body.append(frame, form);
    form.submit();
    form.remove();
  }
  const load = useCallback(async (planOnly = false) => {
    if (!allowed || request.current.busy || !current(request.current.generation, owner)) return;
    const ticket = begin("read"), wanted = request.current.known, previous = request.current.plan;
    try {
      const client = createBackupsClient(window.location.origin), options = { signal: ticket.signal };
      const catalog = planOnly ? null : await readBackups(client, ticket.signal);
      const plan = wanted ? await client.plan({ id: wanted }, options) : null;
      if (plan && plan.id !== wanted) throw new BackupReadError("返回记录未匹配待核对的恢复 ID。");
      if (plan && previous) checkRestoreReceipt(previous, plan);
      const checks = plan?.state === "applied" ? await client.checks({ id: plan.id }, options) : null;
      if (plan && checks) checkRestoreChecks(plan, checks);
      if (!current(ticket.generation, owner)) return;
      request.current.plan = plan;
      const uncertain = request.current.uncertain;
      if (plan && uncertain?.id === plan.id && (uncertain.kind === "preflight" || uncertain.kind === "apply" && ["applied", "expired", "failed", "blocked"].includes(plan.state) || uncertain.kind === "review" && plan.reviewed_at !== null)) request.current.uncertain = null;
      const unknown = request.current.uncertain !== null || plan?.state === "applying";
      setState(old => ({ ...old, owner, archives: catalog?.archives ?? old.archives, keyAvailable: catalog?.keyAvailable ?? old.keyAvailable, plan, checks,
        downloaded: old.downloaded === plan?.before_archive_id ? old.downloaded : null, blocked: unknown,
        notice: unknown ? { text: "上次操作结果尚未完全确认；当前只读核对，不会重新提交。", unknown: true } : plan ? { text: restoreReasons[plan.reason], unknown: false } : null }));
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const notice = { text: failure(error).text, unknown: request.current.uncertain !== null };
        setState(old => planOnly ? { ...old, notice } : { ...old, owner, archives: null, keyAvailable: null, plan: null, checks: null, downloaded: null, blocked: true, notice });
      }
    } finally { finish(ticket.generation, owner); }
  }, [allowed, owner, begin, current, failure, finish]);
  useEffect(() => {
    request.current.alive = true; request.current.known = restorePlanID(window.location.search); request.current.plan = null; request.current.beforeRequested = null; request.current.uncertain = null; request.current.busy = false;
    setState(empty(owner)); void load();
    return () => {
      request.current.alive = false; request.current.generation++; request.current.abort.abort(); request.current.busy = false; request.current.plan = null;
      for (const frame of transfers.current) frame.remove();
      transfers.current.clear();
    };
  }, [owner, load]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") void load(); };
    window.addEventListener("focus", focus); document.addEventListener("visibilitychange", focus);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", focus); };
  }, [load]);
  const visible = allowed && state.owner === owner, data = visible ? state : empty(owner);
  useEffect(() => {
    if (data.plan?.state !== "ready" && data.plan?.state !== "applying") return;
    const timer = setInterval(() => {
      const at = Date.now();
      setNow(at);
      const plan = request.current.plan;
      if (plan?.state === "applying" || plan?.state === "ready" && request.current.beforeRequested === plan.id
        && !plan.before_downloaded && Date.parse(plan.expires_at) > at) void load(true);
    }, 2000);
    return () => clearInterval(timer);
  }, [data.plan?.state, load]);
  async function choose(file: File) {
    if (!visible || request.current.busy || request.current.uncertain) return;
    const ticket = begin("file"); remember(null); request.current.plan = null;
    setState(old => ({ ...old, file: null, plan: null, checks: null, downloaded: null, notice: null, blocked: true }));
    try {
      const upload = await inspectBackupUpload(file);
      if (current(ticket.generation, owner)) setState(old => ({ ...old, file: { name: file.name, ...upload }, blocked: false }));
    } catch (error) { if (current(ticket.generation, owner)) setState(old => ({ ...old, notice: { text: failure(error).text, unknown: false } })); }
    finally { finish(ticket.generation, owner); }
  }
  async function latest() {
    if (!visible || request.current.busy) return;
    if (request.current.known || request.current.uncertain || !data.archives) { await load(); return; }
    const archive = data.archives[0]; if (!archive) return;
    remember(null); request.current.plan = null;
    setState(old => ({ ...old, file: { name: `sub-pool-${archive.id}.spbak`, archive }, plan: null, checks: null, downloaded: null, notice: null, blocked: false }));
  }
  async function download(archive: BackupArchive) {
    if (!visible || request.current.busy) return;
    const ticket = begin("download");
    try {
      if (archive.format === backupStreamFormat) {
        const plan = request.current.plan;
        const before = plan?.state === "ready" && plan.before_archive_id === archive.id ? plan.id : undefined;
        saveAttachment(archive.id, before);
        if (before) request.current.beforeRequested = before;
        setState(old => ({ ...old, notice: { text: "已发起下载。请在浏览器中查看保存进度。", unknown: request.current.uncertain !== null } }));
        return;
      }
      const value = await createBackupsClient(window.location.origin).get({ id: archive.id }, { signal: ticket.signal });
      const file = await checkBackupFile(value, archive);
      if (!current(ticket.generation, owner)) return;
      saveFile(file);
      setState(old => ({ ...old, downloaded: archive.id === request.current.plan?.before_archive_id ? archive.id : old.downloaded,
        notice: { text: "已核对 SHA-256 并发起真实文件下载。请将文件与部署保护密钥分别保管。", unknown: request.current.uncertain !== null } }));
    } catch (error) { if (current(ticket.generation, owner)) setState(old => ({ ...old, notice: { text: failure(error).text, unknown: false } })); }
    finally { finish(ticket.generation, owner); }
  }
  async function createExport(includeSecrets: boolean, scope: BackupScope) {
    if (!visible || request.current.busy || request.current.uncertain && request.current.uncertain.kind !== "export" || (!data.archives || data.keyAvailable !== true) && request.current.uncertain?.kind !== "export") return;
    const ticket = begin("export"), prior = request.current.uncertain, id = prior?.id ?? backupRequestID();
    let dispatched = prior !== null;
    try {
      const client = createBackupsClient(window.location.origin), options = { signal: ticket.signal };
      dispatched = true;
      const archive = prior ? (await client.get({ id }, options)).archive : await client.export({ request_id: id, include_secrets: includeSecrets, scope }, options);
      if (archive.id !== id || archive.include_secrets !== includeSecrets || archive.kind !== "export"
        || archive.format !== backupStreamFormat || archive.scope !== scope) throw new BackupReadError("导出回执未匹配本次请求。");
      if (!current(ticket.generation, owner)) return;
      request.current.uncertain = null; saveAttachment(archive.id);
      setState(old => ({ ...old, blocked: false, notice: { text: "服务端已保存备份。已发起浏览器下载。", unknown: false } }));
      finish(ticket.generation, owner); await load();
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = dispatched && (result.status === 0 || prior !== null);
        if (unknown) request.current.uncertain = { kind: "export", id };
        setState(old => ({ ...old, blocked: unknown, notice: { text: unknown ? "导出结果尚未确认。请点击「核对导出结果」按原 ID 只读查询，勿重新导出。" : result.text, unknown } }));
      }
    } finally { finish(ticket.generation, owner); }
  }
  async function preflight() {
    if (!visible || request.current.busy) return;
    if (request.current.uncertain || !data.file) { await load(); return; }
    if (!data.archives || data.keyAvailable !== true) return;
    const file = data.file, id = backupRequestID(), ticket = begin("preflight");
    remember(id); request.current.plan = null;
    setState(old => ({ ...old, plan: null, checks: null, downloaded: null, notice: null, blocked: true }));
    try {
      const plan = "archive" in file
        ? await createBackupsClient(window.location.origin).preflight({ request_id: id, archive_id: file.archive.id }, { signal: ticket.signal })
        : await uploadBackup(file.file, id, ticket.signal);
      const expected = "archive" in file
        ? { sha256: file.archive.file_sha256, bytes: file.archive.bytes, format: file.archive.format }
        : { sha256: file.sha256, bytes: String(file.file.size), format: file.format };
      if (plan.id !== id || plan.source_bytes !== expected.bytes || plan.source.format !== expected.format
        || expected.sha256 && plan.file_sha256 !== expected.sha256 || !["ready", "blocked"].includes(plan.state)) {
        throw new BackupReadError("预检回执未匹配本次实际文件。");
      }
      if (!current(ticket.generation, owner)) return;
      request.current.plan = plan;
      setState(old => ({ ...old, plan, blocked: false, notice: { text: restoreReasons[plan.reason], unknown: false } }));
      finish(ticket.generation, owner); await load();
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = result.status === 0;
        if (unknown) request.current.uncertain = { kind: "preflight", id };
        setState(old => ({ ...old, blocked: true, notice: { text: unknown ? "预检结果尚未确认，请按已保留 ID 只读核对；不会重新上传。" : result.text, unknown } }));
      }
    } finally { finish(ticket.generation, owner); }
  }
  async function apply() {
    const expected = request.current.plan;
    if (!expected || !visible || request.current.busy || request.current.uncertain || data.blocked || !restoreCanApply(expected, data.downloaded)) return false;
    const ticket = begin("apply");
    let dispatched = false;
    try {
      const client = createBackupsClient(window.location.origin), options = { signal: ticket.signal };
      const plan = await client.plan({ id: expected.id }, options); checkRestoreReceipt(expected, plan);
      if (!current(ticket.generation, owner)) return false;
      request.current.plan = plan; setState(old => ({ ...old, plan }));
      if (!restoreCanApply(plan, data.downloaded)) { setState(old => ({ ...old, blocked: true, notice: { text: restoreReasons[plan.reason], unknown: false } })); return false; }
      dispatched = true;
      const receipt = await client.apply({ id: plan.id, target_basis: plan.target_basis, acknowledged: true }, options);
      checkRestoreReceipt(plan, receipt);
      if (!current(ticket.generation, owner)) return false;
      request.current.plan = receipt;
      setState(old => ({ ...old, plan: receipt, blocked: true, notice: { text: restoreReasons[receipt.reason], unknown: receipt.state === "applying" } }));
      if (receipt.state === "applied") { await refreshSession(); return true; }
      return false;
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = dispatched && result.status === 0;
        if (unknown) request.current.uncertain = { kind: "apply", id: expected.id };
        setState(old => ({ ...old, blocked: true, notice: { text: unknown ? "恢复结果尚未确认。重新登录后按原 ID 只读核对；不会重新恢复。" : result.text, unknown } }));
        if (unknown || result.status === 401) await refreshSession();
      }
      return false;
    } finally { finish(ticket.generation, owner); }
  }
  async function review() {
    const plan = request.current.plan;
    if (!visible || request.current.busy || request.current.uncertain || plan?.state !== "applied" || !data.checks || plan.reviewed_at !== null) return;
    const ticket = begin("review");
    try {
      const value = await createBackupsClient(window.location.origin).review({ id: plan.id, acknowledged: true }, { signal: ticket.signal }); checkRestoreReceipt(plan, value);
      if (!value.reviewed_at) throw new BackupReadError("人工核对尚无服务端确认记录。");
      if (!current(ticket.generation, owner)) return;
      request.current.plan = value; setState(old => ({ ...old, plan: value }));
      finish(ticket.generation, owner); await load();
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = result.status === 0;
        if (unknown) request.current.uncertain = { kind: "review", id: plan.id };
        setState(old => ({ ...old, blocked: true, notice: { text: unknown ? "人工核对记录结果未知，请刷新当前记录，勿重复提交。" : result.text, unknown } }));
      }
    } finally { finish(ticket.generation, owner); }
  }
  return { ...data, downloaded: data.plan?.before_downloaded ? data.plan.before_archive_id : data.downloaded,
    allowed, pending: visible ? pending : null, canApply: visible && !pending && !data.blocked && !request.current.uncertain && restoreCanApply(data.plan, data.downloaded, now),
    knownID: visible ? request.current.known : null,
    uncertainExport: visible && request.current.uncertain?.kind === "export", uncertain: visible && request.current.uncertain !== null,
    load, choose, latest, download, createExport, preflight, apply, review };
}
