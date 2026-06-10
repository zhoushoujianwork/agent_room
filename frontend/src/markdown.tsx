import { type ReactNode } from "react";
import { hueFromID } from "./lib";

/* ──────────────────────────────────────────────────────────────────
   Tiny, dependency-free Markdown renderer for chat bubbles and the room
   summary. It builds React nodes directly (never dangerouslySetInnerHTML),
   so agent/LLM output stays untrusted-safe, and it preserves the existing
   @mention highlighting that plain chat text relied on.

   Supported: paragraphs, soft line breaks, #..###### headings, - / * / +
   and 1. lists, > blockquotes, ``` fenced code, `inline code`, **bold**,
   *italic*, and [text](url) links. Intentionally small — not a spec-complete
   parser; unrecognised syntax falls through as plain text.
   ────────────────────────────────────────────────────────────────── */

export function Markdown({ text, className }: { text: string; className?: string }) {
  return <div className={`md${className ? ` ${className}` : ""}`}>{parseBlocks(text || "")}</div>;
}

/* ── Mention token (shared with the legacy chat renderer) ──────────── */

export function MentionToken({ id }: { id: string }) {
  const hue = hueFromID(id);
  return (
    <span
      className="mention-token"
      style={{
        background: `oklch(0.56 0.15 ${hue} / 0.14)`,
        color: `oklch(0.46 0.17 ${hue})`,
      }}
    >
      @{id}
    </span>
  );
}

/* ── Block level ───────────────────────────────────────────────────── */

function parseBlocks(src: string): ReactNode[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const out: ReactNode[] = [];
  let i = 0;
  let key = 0;
  const nextKey = () => `b${key++}`;

  while (i < lines.length) {
    const line = lines[i];

    // Fenced code block ``` … ```
    if (/^\s*```/.test(line)) {
      const buf: string[] = [];
      i += 1;
      while (i < lines.length && !/^\s*```/.test(lines[i])) {
        buf.push(lines[i]);
        i += 1;
      }
      if (i < lines.length) i += 1; // consume the closing fence
      out.push(
        <pre className="md-pre" key={nextKey()}>
          <code>{buf.join("\n")}</code>
        </pre>,
      );
      continue;
    }

    // Blank line — paragraph separator
    if (!line.trim()) {
      i += 1;
      continue;
    }

    // Heading
    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      const level = Math.min(heading[1].length, 6);
      const k = nextKey();
      out.push(
        <div className={`md-h md-h${level}`} key={k}>
          {parseInline(heading[2].trim(), k)}
        </div>,
      );
      i += 1;
      continue;
    }

    // Blockquote
    if (/^\s*>\s?/.test(line)) {
      const buf: string[] = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
        buf.push(lines[i].replace(/^\s*>\s?/, ""));
        i += 1;
      }
      const k = nextKey();
      out.push(
        <blockquote className="md-quote" key={k}>
          {renderSoftLines(buf, k)}
        </blockquote>,
      );
      continue;
    }

    // Unordered list
    if (/^\s*[-*+]\s+/.test(line)) {
      const items: ReactNode[] = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) {
        const k = nextKey();
        items.push(<li key={k}>{parseInline(lines[i].replace(/^\s*[-*+]\s+/, ""), k)}</li>);
        i += 1;
      }
      out.push(
        <ul className="md-ul" key={nextKey()}>
          {items}
        </ul>,
      );
      continue;
    }

    // Ordered list
    const ordered = line.match(/^\s*(\d+)\.\s+/);
    if (ordered) {
      const start = Number(ordered[1]) || 1;
      const items: ReactNode[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        const k = nextKey();
        items.push(<li key={k}>{parseInline(lines[i].replace(/^\s*\d+\.\s+/, ""), k)}</li>);
        i += 1;
      }
      out.push(
        <ol className="md-ol" start={start} key={nextKey()}>
          {items}
        </ol>,
      );
      continue;
    }

    // Paragraph — gather consecutive plain lines, keeping soft breaks
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i])) {
      para.push(lines[i]);
      i += 1;
    }
    const k = nextKey();
    out.push(
      <p className="md-p" key={k}>
        {renderSoftLines(para, k)}
      </p>,
    );
  }

  return out;
}

function isBlockStart(line: string): boolean {
  return (
    /^\s*```/.test(line) ||
    /^(#{1,6})\s+/.test(line) ||
    /^\s*>\s?/.test(line) ||
    /^\s*[-*+]\s+/.test(line) ||
    /^\s*\d+\.\s+/.test(line)
  );
}

// renderSoftLines joins lines with <br/>, the chat-friendly reading of single
// newlines inside a paragraph (GitHub-flavored markdown line breaks).
function renderSoftLines(lines: string[], keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  lines.forEach((ln, idx) => {
    if (idx > 0) out.push(<br key={`${keyBase}-br${idx}`} />);
    out.push(...parseInline(ln, `${keyBase}-l${idx}`));
  });
  return out;
}

/* ── Inline level ──────────────────────────────────────────────────── */

// Ordered alternation: code spans win first (no formatting inside), then bold
// before italic (so the leading `**` isn't mistaken for italic), then links,
// then @mentions. Bold/italic/link contents recurse so nesting works.
const INLINE_RE =
  /`([^`]+)`|\*\*(.+?)\*\*|\*(.+?)\*|\[([^\]]+)\]\(([^)\s]+)\)|(@[a-zA-Z0-9][\w.-]*)|(https?:\/\/[\w\-.~:/?#@!$&*+,;=%]+)/g;

function parseInline(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = new RegExp(INLINE_RE.source, INLINE_RE.flags);
  let last = 0;
  let n = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const key = `${keyBase}-i${n++}`;
    if (m[1] !== undefined) {
      out.push(
        <code className="md-code" key={key}>
          {m[1]}
        </code>,
      );
    } else if (m[2] !== undefined) {
      out.push(<strong key={key}>{parseInline(m[2], key)}</strong>);
    } else if (m[3] !== undefined) {
      out.push(<em key={key}>{parseInline(m[3], key)}</em>);
    } else if (m[4] !== undefined && m[5] !== undefined) {
      const href = safeHref(m[5]);
      out.push(
        href ? (
          <a
            className="md-link"
            href={href}
            target="_blank"
            rel="noopener noreferrer nofollow"
            key={key}
          >
            {parseInline(m[4], key)}
          </a>
        ) : (
          m[0]
        ),
      );
    } else if (m[6] !== undefined) {
      out.push(<MentionToken id={m[6].slice(1)} key={key} />);
    } else if (m[7] !== undefined) {
      // Bare URLs (a GitHub link pasted as-is, etc.) become clickable too.
      // Trailing punctuation stays outside the link: "见 https://x.com/a。"
      const url = m[7].replace(/[.,;:!?，。；：！？、]+$/, "");
      const rest = m[7].slice(url.length);
      out.push(
        <a
          className="md-link"
          href={url}
          target="_blank"
          rel="noopener noreferrer nofollow"
          key={key}
        >
          {url}
        </a>,
      );
      if (rest) out.push(rest);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

// safeHref only lets through schemes that can't execute script. Anything else
// (javascript:, data:, etc.) returns null and the link renders as plain text.
function safeHref(url: string): string | null {
  const u = url.trim();
  if (/^(https?:\/\/|mailto:|\/)/i.test(u)) return u;
  return null;
}
