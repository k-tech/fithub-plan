import { useMemo } from 'react'
import { renderMarkdown, type RenderOptions } from './markdown-render'
import './markdown.css'

// Renders user-written Markdown: issue descriptions, comments, pull request
// descriptions.
//
// dangerouslySetInnerHTML is used on purpose. The value has already been escaped
// by the parser and then filtered against an explicit allowlist, so this is not
// the place the safety is decided — see markdown.ts, which documents both layers
// and is covered by tests that feed real attack payloads through it.
export function Markdown({ source, issueHref, className }: {
  source?: string
  issueHref?: RenderOptions['issueHref']
  className?: string
}) {
  const html = useMemo(() => renderMarkdown(source ?? '', { issueHref }), [source, issueHref])
  if (!html) {
    return null
  }
  return <div className={`md-body${className ? ' ' + className : ''}`} dangerouslySetInnerHTML={{ __html: html }} />
}
