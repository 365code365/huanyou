'use client'
import { Fragment, type ReactNode } from 'react'

/**
 * Minimal Markdown renderer for our own docs (headings, paragraphs, lists,
 * tables, fenced code, inline code / bold, rules). Builds React nodes, never
 * injects HTML, so the content can't run scripts.
 */
export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r/g, '').split('\n')
  const out: ReactNode[] = []
  let i = 0
  const key = () => out.length
  while (i < lines.length) {
    const l = lines[i]
    if (l.startsWith('```')) {
      const body: string[] = []
      for (i++; i < lines.length && !lines[i].startsWith('```'); i++) body.push(lines[i])
      i++
      out.push(<pre key={key()} className="md-code">{body.join('\n')}</pre>)
      continue
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(l)
    if (h) {
      const level = h[1].length
      const Tag = (['h2', 'h3', 'h4', 'h5'] as const)[level - 1]
      out.push(<Tag key={key()} className="md-h">{inline(h[2])}</Tag>)
      i++
      continue
    }
    if (/^---+$/.test(l.trim())) {
      out.push(<hr key={key()} />)
      i++
      continue
    }
    if (l.trim().startsWith('|')) {
      const rows: string[][] = []
      for (; i < lines.length && lines[i].trim().startsWith('|'); i++) {
        const cells = lines[i].trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim())
        if (!cells.every((c) => /^:?-+:?$/.test(c))) rows.push(cells)
      }
      const [head, ...body] = rows
      out.push(
        <table key={key()} className="md-table">
          <thead><tr>{head.map((c, j) => <th key={j}>{inline(c)}</th>)}</tr></thead>
          <tbody>{body.map((r, k) => <tr key={k}>{r.map((c, j) => <td key={j}>{inline(c)}</td>)}</tr>)}</tbody>
        </table>,
      )
      continue
    }
    if (/^\s*([-*]|\d+\.)\s+/.test(l)) {
      const ordered = /^\s*\d+\./.test(l)
      const items: string[] = []
      for (; i < lines.length && /^\s*([-*]|\d+\.)\s+/.test(lines[i]); i++) items.push(lines[i].replace(/^\s*([-*]|\d+\.)\s+/, ''))
      const L = ordered ? 'ol' : 'ul'
      out.push(<L key={key()}>{items.map((t, j) => <li key={j}>{inline(t)}</li>)}</L>)
      continue
    }
    if (!l.trim()) {
      i++
      continue
    }
    const para: string[] = []
    for (; i < lines.length && lines[i].trim() && !/^(#|```|\||\s*([-*]|\d+\.)\s)/.test(lines[i]); i++) para.push(lines[i])
    out.push(<p key={key()}>{inline(para.join(' '))}</p>)
  }
  return <div className="md">{out}</div>
}

function inline(s: string): ReactNode {
  // `code` and **bold**
  const parts = s.split(/(`[^`]+`|\*\*[^*]+\*\*)/g)
  return parts.map((p, i) =>
    p.startsWith('`') && p.endsWith('`') ? <code key={i}>{p.slice(1, -1)}</code>
      : p.startsWith('**') && p.endsWith('**') ? <b key={i}>{p.slice(2, -2)}</b>
        : <Fragment key={i}>{p}</Fragment>,
  )
}
