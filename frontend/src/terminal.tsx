import { useLayoutEffect, useRef, useState } from "react";
import type { ChatMessage, Participant } from "./types";
import { Icon } from "./icons";
import { StatusDot } from "./ui";
import { durationMsLabel, shortClock } from "./lib";
import { parseCommandResult } from "./messages";

/* ──────────────────────────────────────────────────────────────────
   执行器终端边栏:把发往各 executor 的 command 与回来的 command_result
   按时间累计追加,渲染成一个连续的"伪终端"会话——聊天流里命令卡片默认
   收起后,这里提供另一条实时观察远端执行过程的通道。
   ────────────────────────────────────────────────────────────────── */

export interface TermEntry {
  commandID: string;
  command?: ChatMessage;
  result?: ChatMessage;
}

// collectExecutorSessions 按执行器聚合命令流。已上线的 executor(presence
// metadata.mode=executor)即使还没收到命令也先占位,让终端栏能立即看到它。
export function collectExecutorSessions(
  messages: ChatMessage[],
  participants: Participant[],
): Map<string, TermEntry[]> {
  const sessions = new Map<string, TermEntry[]>();
  const byCommand = new Map<string, TermEntry>();
  const ensure = (executorID: string): TermEntry[] => {
    let list = sessions.get(executorID);
    if (!list) {
      list = [];
      sessions.set(executorID, list);
    }
    return list;
  };
  for (const p of participants) {
    if (p.metadata?.mode === "executor") ensure(p.id);
  }
  for (const msg of messages) {
    if (msg.type === "command") {
      const executorID = msg.target_id || "";
      if (!executorID) continue;
      const entry: TermEntry = {
        commandID: msg.id || `${executorID}:${msg.created_at || ""}`,
        command: msg,
      };
      ensure(executorID).push(entry);
      if (msg.id) byCommand.set(msg.id, entry);
      continue;
    }
    if (msg.type === "command_result") {
      const commandID = msg.metadata?.command_id || "";
      const entry = commandID ? byCommand.get(commandID) : undefined;
      if (entry) {
        entry.result = msg;
        continue;
      }
      // 命令本体被历史分页截掉时,结果独立成一条(命令文本不可见)。
      const executorID = msg.sender_id || "";
      if (!executorID) continue;
      ensure(executorID).push({ commandID: commandID || msg.id || "", result: msg });
    }
  }
  return sessions;
}

export function ExecutorTermPanel({
  sessions,
  participants,
  onClose,
}: {
  sessions: Map<string, TermEntry[]>;
  participants: Participant[];
  onClose: () => void;
}) {
  const ids = [...sessions.keys()];
  const [picked, setPicked] = useState("");
  const active = picked && sessions.has(picked) ? picked : ids[0] || "";
  const entries = active ? sessions.get(active) || [] : [];
  const online = participants.some((p) => p.id === active && p.metadata?.mode === "executor");
  const runningCount = entries.filter((e) => e.command && !e.result).length;

  // 自动吸底:有新输出时跟随到底部;用户向上翻看历史时不打扰。
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const pinnedRef = useRef(true);
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight;
  }, [sessions, active]);
  function onScroll() {
    const el = scrollRef.current;
    if (!el) return;
    pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
  }

  return (
    <aside className="term-panel">
      <div className="term-head">
        <span className="term-head-title">
          <Icon name="terminal" size={14} /> 执行器终端
        </span>
        <span className="term-flex" />
        {active && (
          <span className="term-host" title={active}>
            <StatusDot tone={online ? "live" : "off"} pulse={online && runningCount > 0} />
            {active}
          </span>
        )}
        <button type="button" className="term-close" onClick={onClose} title="收起终端">
          <Icon name="x" size={14} />
        </button>
      </div>
      {ids.length > 1 && (
        <div className="term-tabs">
          {ids.map((id) => (
            <button
              key={id}
              type="button"
              className={id === active ? "on" : ""}
              onClick={() => setPicked(id)}
              title={id}
            >
              {id}
            </button>
          ))}
        </div>
      )}
      <div className="term-scroll" ref={scrollRef} onScroll={onScroll}>
        {entries.length === 0 ? (
          <div className="term-empty">
            {active ? "等待第一条远程命令…" : "房间内还没有执行器。"}
          </div>
        ) : (
          entries.map((entry) => <TermEntryView key={entry.commandID} entry={entry} />)
        )}
      </div>
    </aside>
  );
}

function TermEntryView({ entry }: { entry: TermEntry }) {
  const { command, result } = entry;
  const parsed = result ? parseCommandResult(result) : null;
  const timedOut = result?.metadata?.timed_out === "true";
  const ok = Boolean(result && !timedOut && parsed?.exitCode === "0");
  const duration = durationMsLabel(result?.metadata?.duration_ms);
  const from = command?.sender_id || "";
  const at = shortClock(command?.created_at || result?.created_at);
  return (
    <div className={`term-entry${result ? (ok ? " is-ok" : " is-failed") : " is-running"}`}>
      <div className="term-cmd">
        <span className="term-prompt">$</span>
        <span className="term-cmd-text">{command?.content || "(命令不可见,已被历史截断)"}</span>
        <span className="term-cmd-meta">
          {from && <span className="term-from">{from}</span>}
          {at && <time>{at}</time>}
        </span>
      </div>
      {result ? (
        <>
          {parsed?.stdout && <pre className="term-out">{parsed.stdout}</pre>}
          {parsed?.stderr && <pre className="term-out term-err">{parsed.stderr}</pre>}
          {!parsed?.stdout && !parsed?.stderr && <pre className="term-out term-mute">(no output)</pre>}
          <div className="term-status">
            exit {parsed?.exitCode || "?"}
            {timedOut && " · timed out"}
            {duration && ` · ${duration}`}
          </div>
        </>
      ) : (
        <div className="term-running-line">
          <span className="term-cursor" /> 运行中…
        </div>
      )}
    </div>
  );
}
