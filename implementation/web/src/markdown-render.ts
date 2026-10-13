// Markdown rendering for user-written content: issue descriptions, comments,
// pull request descriptions.
//
// Two independent layers keep this safe, because everything here is text the
// user does not control:
//
//   1. Raw HTML in the source is never parsed. markdown-it is configured with
//      html:false, so a tag written in a comment is escaped and displayed as
//      text rather than becoming markup.
//   2. The output is still passed through a sanitiser. Layer one is a setting
//      that a future change could flip; the sanitiser is what makes the promise
//      hold regardless of how the parser is configured later.
//
// Link targets are validated by markdown-it and again by the sanitiser, so a
// `javascript:` URL cannot become a clickable link.
import MarkdownIt, { type Env } from 'markdown-it'
import DOMPurify from 'dompurify'

export interface RenderOptions {
  // Builds the href for a `#123` reference. Returning null leaves the text as
  // plain text rather than producing a link that would go nowhere.
  issueHref?: IssueHref
}

// The parser is stateless and safe to share, but the issue-link resolver is a
// per-render decision, so the rule reads it from the environment instead of
// closing over it.
type IssueHref = (number: number) => string | null

const markdown = new MarkdownIt({
  // See the note above: this is the first of the two defences.
  html: false,
  linkify: true,
  // A person writing a comment expects their line breaks to survive; collapsing
  // them is what makes plain prose look like it was reformatted behind their back.
  breaks: true,
})

// --- references ---------------------------------------------------------------
//
// `@someone` and `#123`. Both are recognised only in ordinary text: inside a
// code span or an existing link the characters are literal, which is what a
// person quoting code expects.

const REFERENCE_PATTERN = /^(@[A-Za-z0-9](?:[A-Za-z0-9._-]{0,38})|#(\d{1,9}))(?![A-Za-z0-9._-])/

markdown.inline.ruler.before('emphasis', 'fithub_reference', (state, silent) => {
  const start = state.pos
  const first = state.src.charCodeAt(start)
  if (first !== 0x40 && first !== 0x23) {
    return false // '@' or '#'
  }
  // Nested inside a link, these characters are part of the link text.
  if (state.linkLevel > 0) {
    return false
  }
  const match = REFERENCE_PATTERN.exec(state.src.slice(start))
  if (!match) {
    return false
  }
  const raw = match[1]
  if (silent) {
    state.pos += raw.length
    return true
  }
  if (raw.charCodeAt(0) === 0x40) {
    // A mention has no destination yet because there is no profile page to open.
    // It is still marked so it reads as a person rather than as stray text; the
    // name itself goes through a text token, so it is escaped like any other text.
    const open = state.push('html_inline', '', 0)
    open.content = '<span class="md-mention">'
    const name = state.push('text', '', 0)
    name.content = raw
    const close = state.push('html_inline', '', 0)
    close.content = '</span>'
  } else {
    const number = Number(match[2])
    const href = (state.env as { issueHref?: IssueHref }).issueHref?.(number) ?? null
    if (href) {
      const open = state.push('link_open', 'a', 1)
      open.attrs = [['href', href], ['class', 'md-reference']]
      const text = state.push('text', '', 0)
      text.content = raw
      state.push('link_close', 'a', -1)
    } else {
      const text = state.push('text', '', 0)
      text.content = raw
    }
  }
  state.pos += raw.length
  return true
})

// --- task lists ---------------------------------------------------------------
//
// A GitHub-style checklist. The boxes are rendered disabled: ticking one would
// have to rewrite the Markdown source, which is a separate capability from
// displaying it, and a checkbox that looks interactive but silently does nothing
// is worse than one that is visibly read-only.

markdown.core.ruler.push('fithub_task_lists', state => {
  const tokens = state.tokens
  for (let index = 0; index < tokens.length; index += 1) {
    const inline = tokens[index]
    if (inline.type !== 'inline' || !inline.children || inline.children.length === 0) {
      continue
    }
    // The shape is list_item_open → paragraph_open → inline.
    if (tokens[index - 1]?.type !== 'paragraph_open' || tokens[index - 2]?.type !== 'list_item_open') {
      continue
    }
    const first = inline.children[0]
    if (first.type !== 'text') {
      continue
    }
    const marker = /^\[([ xX])\][ \t]+/.exec(first.content)
    if (!marker) {
      continue
    }
    first.content = first.content.slice(marker[0].length)
    const item = tokens[index - 2]
    item.attrJoin('class', 'md-task-item')
    // The renderer reads this to place the box. It is recorded on the token
    // rather than injected as markup here, so the checkbox is written by our own
    // renderer code instead of passing through the parser.
    item.meta = { task: marker[1].toLowerCase() === 'x' }
  }
})

// The default rule for this token does not exist, so the renderer's own token
// writer is used and the box prepended to it.
markdown.renderer.rules.list_item_open = (tokens, index, options, _env, self) => {
  const rendered = self.renderToken(tokens, index, options)
  const task = tokens[index].meta?.task
  if (typeof task !== 'boolean') {
    return rendered
  }
  // The string is a constant written here, never user input.
  return rendered + (task
    ? '<input class="md-task" type="checkbox" checked disabled> '
    : '<input class="md-task" type="checkbox" disabled> ')
}

// --- sanitising ---------------------------------------------------------------
//
// An explicit allowlist rather than a list of things to remove: anything not
// named here is dropped, so a tag introduced by a future parser change cannot
// slip through unnoticed.
const SANITIZE_CONFIG = {
  ALLOWED_TAGS: [
    'p', 'br', 'hr', 'strong', 'em', 'del', 'code', 'pre', 'blockquote',
    'ul', 'ol', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
    'a', 'img', 'span', 'input',
    'table', 'thead', 'tbody', 'tr', 'th', 'td',
  ],
  ALLOWED_ATTR: [
    'href', 'title', 'src', 'alt', 'class', 'type', 'checked', 'disabled',
    'colspan', 'rowspan', 'align', 'start',
  ],
  // data-* attributes carry no meaning here and are a common way to smuggle
  // values into client-side code.
  ALLOW_DATA_ATTR: false,
  // A named target on a link in user content is a reverse-tabnabbing vector, and
  // nothing here needs a new tab.
  ADD_ATTR: [] as string[],
  FORBID_ATTR: ['target', 'style', 'srcset'],
  USE_PROFILES: { html: true },
}

export function renderMarkdown(source: string, options: RenderOptions = {}): string {
  if (!source) {
    return ''
  }
  const env: Env = { issueHref: options.issueHref }
  const rendered = markdown.render(source, env)
  return DOMPurify.sanitize(rendered, SANITIZE_CONFIG)
}

// Exposed so the tests can pin the parser configuration itself rather than only
// its output.
export const markdownOptions = markdown.options
