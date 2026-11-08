"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createBackupsClient } from "@sub-pool/contracts/client";
import type { BackupArchive, RestorePlan, RestoreChecks } from "@sub-pool/contracts/backups";
import { useApp } from "./store";
import { restorePlanID } from "./backup-return";
import { BackupReadError, backupFailure, backupHash, backupRequestID, checkBackupFile, checkRestoreChecks, checkRestoreReceipt, readBackupUpload, readBackups, restoreCanApply, restoreReasons } from "./backup-data";

type Upload = { name: string; content: string; sha256: string };
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
  const request = useRef({ alive: true, generation: 0, busy: false, abort: new AbortController(), known: null as string | null, plan: null as RestorePlan | null, uncertain: null as Uncertain | null });
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
  const load = useCallback(async () => {
    if (!allowed || request.current.busy || !current(request.current.generation, owner)) return;
    const ticket = begin("read"), wanted = request.current.known, previous = request.current.plan;
    try {
      const client = createBackupsClient(window.location.origin), options = { signal: ticket.signal };
      const catalog = await readBackups(client, ticket.signal);
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
      setState(old => ({ ...old, owner, archives: catalog.archives, keyAvailable: catalog.keyAvailable, plan, checks,
        downloaded: old.downloaded === plan?.before_archive_id ? old.downloaded : null, blocked: unknown,
        notice: unknown ? { text: "上次操作结果尚未完全确认；当前只读核对，不会重新提交。", unknown: true } : plan ? { text: restoreReasons[plan.reason], unknown: false } : null }));
    } catch (error) {
      if (current(ticket.generation, owner)) setState(old => ({ ...old, owner, archives: null, keyAvailable: null, plan: null, checks: null, downloaded: null, blocked: true, notice: { text: failure(error).text, unknown: request.current.uncertain !== null } }));
    } finally { finish(ticket.generation, owner); }
  }, [allowed, owner, begin, current, failure, finish]);
  useEffect(() => {
    request.current.alive = true; request.current.known = restorePlanID(window.location.search); request.current.plan = null; request.current.uncertain = null; request.current.busy = false;
    setState(empty(owner)); void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); request.current.busy = false; request.current.plan = null; };
  }, [owner, load]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") void load(); };
    window.addEventListener("focus", focus); document.addEventListener("visibilitychange", focus);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", focus); };
  }, [load]);
  const visible = allowed && state.owner === owner, data = visible ? state : empty(owner);
  useEffect(() => {
    if (data.plan?.state !== "ready" && data.plan?.state !== "applying") return;
    const timer = setInterval(() => { setNow(Date.now()); if (request.current.plan?.state === "applying") void load(); }, 2000);
    return () => clearInterval(timer);
  }, [data.plan?.state, load]);
  async function choose(file: File) {
    if (!visible || request.current.busy || request.current.uncertain) return;
    const ticket = begin("file"); remember(null); request.current.plan = null;
    setState(old => ({ ...old, file: null, plan: null, checks: null, downloaded: null, notice: null, blocked: true }));
    try {
      const upload = await readBackupUpload(file);
      if (current(ticket.generation, owner)) setState(old => ({ ...old, file: upload, blocked: false }));
    } catch (error) { if (current(ticket.generation, owner)) setState(old => ({ ...old, notice: { text: failure(error).text, unknown: false } })); }
    finally { finish(ticket.generation, owner); }
  }
  async function latest() {
    if (!visible || request.current.busy) return;
    if (request.current.known || request.current.uncertain || !data.archives) { await load(); return; }
    const archive = data.archives[0]; if (!archive) return;
    const ticket = begin("file");
    try {
      const value = await createBackupsClient(window.location.origin).get({ id: archive.id }, { signal: ticket.signal });
      const file = await checkBackupFile(value, archive);
      if (!current(ticket.generation, owner)) return;
      remember(null); request.current.plan = null;
      setState(old => ({ ...old, file: { name: file.filename, content: file.content, sha256: file.archive.file_sha256 }, plan: null, checks: null, downloaded: null, notice: null, blocked: false }));
    } catch (error) { if (current(ticket.generation, owner)) setState(old => ({ ...old, file: null, blocked: true, notice: { text: failure(error).text, unknown: false } })); }
    finally { finish(ticket.generation, owner); }
  }
  async function download(archive: BackupArchive) {
    if (!visible || request.current.busy) return;
    const ticket = begin("download");
    try {
      const value = await createBackupsClient(window.location.origin).get({ id: archive.id }, { signal: ticket.signal });
      const file = await checkBackupFile(value, archive);
      if (!current(ticket.generation, owner)) return;
      saveFile(file);
      setState(old => ({ ...old, downloaded: archive.id === request.current.plan?.before_archive_id ? archive.id : old.downloaded,
        notice: { text: "已核对 SHA-256 并发起真实文件下载。请将文件与部署保护密钥分别保管。", unknown: request.current.uncertain !== null } }));
    } catch (error) { if (current(ticket.generation, owner)) setState(old => ({ ...old, notice: { text: failure(error).text, unknown: false } })); }
    finally { finish(ticket.generation, owner); }
  }
  async function createExport(includeSecrets: boolean) {
    if (!visible || request.current.busy || request.current.uncertain && request.current.uncertain.kind !== "export" || (!data.archives || data.keyAvailable !== true) && request.current.uncertain?.kind !== "export") return;
    const ticket = begin("export"), prior = request.current.uncertain, id = prior?.id ?? backupRequestID();
    let dispatched = prior !== null;
    try {
      const client = createBackupsClient(window.location.origin), options = { signal: ticket.signal };
      dispatched = true;
      const archive = prior ? null : await client.export({ request_id: id, include_secrets: includeSecrets }, options);
      if (archive && (archive.id !== id || archive.include_secrets !== includeSecrets || archive.kind !== "export")) throw new BackupReadError("导出回执未匹配本次请求。");
      const value = await client.get({ id }, options);
      if (value.archive.id !== id || value.archive.kind !== "export") throw new BackupReadError("导出文件未匹配原请求 ID。");
      const file = await checkBackupFile(value, archive ?? undefined);
      if (!current(ticket.generation, owner)) return;
      request.current.uncertain = null; saveFile(file);
      setState(old => ({ ...old, blocked: false, notice: { text: "服务端备份已保存，已核对并发起真实文件下载。", unknown: false } }));
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
      if (await backupHash(file.content) !== file.sha256) throw new BackupReadError("所选文件内容已变化，请重新选择。");
      if (!current(ticket.generation, owner)) return;
      const plan = await createBackupsClient(window.location.origin).preflight({ request_id: id, content: file.content }, { signal: ticket.signal });
      if (plan.id !== id || plan.file_sha256 !== file.sha256 || !["ready", "blocked"].includes(plan.state)) throw new BackupReadError("预检回执未匹配本次实际上传的文件。");
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
  return { ...data, allowed, pending: visible ? pending : null, canApply: visible && !pending && !data.blocked && !request.current.uncertain && restoreCanApply(data.plan, data.downloaded, now),
    knownID: visible ? request.current.known : null,
    uncertainExport: visible && request.current.uncertain?.kind === "export", uncertain: visible && request.current.uncertain !== null,
    load, choose, latest, download, createExport, preflight, apply, review };
}
