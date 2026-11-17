import { useMemo, type ReactNode } from 'react';

/**
 * Bounded Markdown subset renderer for presentation-only text cards.
 *
 * Safety model: the server already rejects raw HTML, images and non-http(s)
 * schemes at draft admission, but this renderer never trusts that alone — it
 * builds React elements exclusively (React escapes every text child, so raw
 * markup can never become DOM) and independently validates every link target.
 * There is intentionally no dangerouslySetInnerHTML anywhere in this path.
 *
 * Supported subset: fenced code blocks, h1–h3, blockquotes, ordered/unordered
 * lists, paragraphs, inline code, bold/italic with asterisks and safe links.
 * Underscores are literal (snake_case prose must not gain emphasis) and any
 * unrecognized marker degrades to literal text rather than breaking layout.
 */

const safeLinkPattern = /^\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/;
const safeUrlPattern = /^https?:\/\/[^\s<>"]+$/;
const fencePattern = /^ {0,3}```/;
const headingPattern = /^(#{1,3})\s+(.+)$/;
const quotePattern = /^>\s?(.*)$/;
const unorderedItemPattern = /^\s{0,2}[-*+]\s+(.*)$/;
const orderedItemPattern = /^\s{0,2}\d+\.\s+(.*)$/;

type MarkdownBlock =
  | { kind: 'code'; language: string; lines: string[] }
  | { kind: 'heading'; level: 1 | 2 | 3; text: string }
  | { kind: 'quote'; lines: string[] }
  | { kind: 'list'; ordered: boolean; items: string[] }
  | { kind: 'paragraph'; lines: string[] };

export function parseTextCardBlocks(source: string): MarkdownBlock[] {
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  const blocks: MarkdownBlock[] = [];
  let paragraph: string[] = [];
  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ kind: 'paragraph', lines: paragraph });
      paragraph = [];
    }
  };
  for (let i = 0; i < lines.length;) {
    const line = lines[i]!;
    const fence = fencePattern.exec(line);
    if (fence) {
      flushParagraph();
      const language = line.slice(fence[0].length).trim();
      const code: string[] = [];
      i++;
      while (i < lines.length && !fencePattern.test(lines[i]!)) {
        code.push(lines[i]!);
        i++;
      }
      // A balanced fence is guaranteed by the server validator; if a malformed
      // payload reaches the client, treat the remainder as code defensively.
      if (i < lines.length) i++;
      blocks.push({ kind: 'code', language, lines: code });
      continue;
    }
    const heading = headingPattern.exec(line);
    if (heading) {
      flushParagraph();
      blocks.push({ kind: 'heading', level: heading[1]!.length as 1 | 2 | 3, text: heading[2]! });
      i++;
      continue;
    }
    const quote = quotePattern.exec(line);
    if (quote) {
      flushParagraph();
      const quoted: string[] = [];
      while (i < lines.length) {
        const match = quotePattern.exec(lines[i]!);
        if (!match) break;
        quoted.push(match[1]!);
        i++;
      }
      blocks.push({ kind: 'quote', lines: quoted });
      continue;
    }
    const unordered = unorderedItemPattern.exec(line);
    const ordered = orderedItemPattern.exec(line);
    if (unordered || ordered) {
      flushParagraph();
      const isOrdered = !!ordered;
      const pattern = isOrdered ? orderedItemPattern : unorderedItemPattern;
      const items: string[] = [];
      while (i < lines.length) {
        const match = pattern.exec(lines[i]!);
        if (!match) break;
        items.push(match[1]!);
        i++;
      }
      blocks.push({ kind: 'list', ordered: isOrdered, items });
      continue;
    }
    if (line.trim() === '') {
      flushParagraph();
      i++;
      continue;
    }
    paragraph.push(line);
    i++;
  }
  flushParagraph();
  return blocks;
}

/**
 * Scanner-based inline rendering. Each recognized marker needs a closing
 * delimiter; an unterminated marker falls back to literal text so malformed
 * input can never swallow subsequent content or produce broken markup.
 */
function renderInline(source: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let plain = '';
  let sequence = 0;
  const flush = () => {
    if (plain) {
      nodes.push(plain);
      plain = '';
    }
  };
  for (let i = 0; i < source.length;) {
    const ch = source[i]!;
    if (ch === '`') {
      const close = source.indexOf('`', i + 1);
      if (close > i + 1) {
        flush();
        nodes.push(
          <code key={`${keyPrefix}-code-${sequence++}`}>{source.slice(i + 1, close)}</code>,
        );
        i = close + 1;
        continue;
      }
    }
    if (ch === '[') {
      const match = safeLinkPattern.exec(source.slice(i));
      if (match) {
        const label = match[1]!;
        const url = match[2]!;
        flush();
        if (safeUrlPattern.test(url)) {
          nodes.push(
            <a key={`${keyPrefix}-link-${sequence++}`} href={url} target="_blank" rel="noreferrer">
              {renderInline(label, `${keyPrefix}-link-body-${sequence}`)}
            </a>,
          );
        } else {
          // Defense in depth: never emit a non-http(s) target, show the raw
          // Markdown text instead so nothing is silently dropped.
          nodes.push(match[0]);
        }
        i += match[0].length;
        continue;
      }
    }
    if (ch === '*') {
      const bold = source[i + 1] === '*';
      const marker = bold ? '**' : '*';
      const close = source.indexOf(marker, i + marker.length);
      if (close > i + marker.length) {
        const inner = source.slice(i + marker.length, close);
        flush();
        if (bold) {
          nodes.push(
            <strong key={`${keyPrefix}-strong-${sequence++}`}>
              {renderInline(inner, `${keyPrefix}-strong-body-${sequence}`)}
            </strong>,
          );
        } else {
          nodes.push(
            <em key={`${keyPrefix}-em-${sequence++}`}>
              {renderInline(inner, `${keyPrefix}-em-body-${sequence}`)}
            </em>,
          );
        }
        i = close + marker.length;
        continue;
      }
    }
    plain += ch;
    i++;
  }
  flush();
  return nodes;
}

function renderBlock(block: MarkdownBlock, index: number): ReactNode {
  const key = `text-card-block-${index}`;
  switch (block.kind) {
    case 'code':
      return (
        <pre key={key} className="text-card-md-code">
          <code>{block.lines.join('\n')}</code>
        </pre>
      );
    case 'heading': {
      const text = renderInline(block.text, `${key}-heading`);
      if (block.level === 1) return <h3 key={key}>{text}</h3>;
      if (block.level === 2) return <h4 key={key}>{text}</h4>;
      return <h5 key={key}>{text}</h5>;
    }
    case 'quote':
      return (
        <blockquote key={key} className="text-card-md-quote">
          {block.lines.map((line, i) => (
            <p key={i}>{renderInline(line, `${key}-quote-${i}`)}</p>
          ))}
        </blockquote>
      );
    case 'list':
      return block.ordered ? (
        <ol key={key}>
          {block.items.map((item, i) => (
            <li key={i}>{renderInline(item, `${key}-li-${i}`)}</li>
          ))}
        </ol>
      ) : (
        <ul key={key}>
          {block.items.map((item, i) => (
            <li key={i}>{renderInline(item, `${key}-li-${i}`)}</li>
          ))}
        </ul>
      );
    case 'paragraph':
      return <p key={key}>{block.lines.map((line, i) => renderInline(line, `${key}-p-${i}`))}</p>;
  }
}

export function TextCardMarkdown({ content }: { content: string }) {
  const blocks = useMemo(() => parseTextCardBlocks(content), [content]);
  return <div className="text-card-md">{blocks.map(renderBlock)}</div>;
}
