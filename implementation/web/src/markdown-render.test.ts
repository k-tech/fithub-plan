import assert from 'node:assert/strict'
import { test, before } from 'node:test'
import { JSDOM } from 'jsdom'

// The sanitiser needs a DOM. It is the layer that actually guarantees safety, so
// the tests must exercise it rather than only the parser: a test that ran without
// a DOM would see the sanitiser quietly pass everything through and would prove
// nothing. The window is installed before the module is imported, because
// DOMPurify binds to the global window when it is first loaded.
const dom = new JSDOM('<!doctype html><html><body></body></html>')
const globals = globalThis as unknown as { window: unknown; document: unknown; Node: unknown }
globals.window = dom.window
globals.document = dom.window.document
globals.Node = dom.window.Node

let renderMarkdown: typeof import('./markdown-render.ts').renderMarkdown
let markdownOptions: typeof import('./markdown-render.ts').markdownOptions

before(async () => {
  const module = await import('./markdown-render.ts')
  renderMarkdown = module.renderMarkdown
  markdownOptions = module.markdownOptions
})

test('raw HTML in user content is never rendered as markup', () => {
  // The classic payloads. Checking the parsed DOM rather than the output string
  // is the point: escaped text that merely mentions "onerror" is safe, whereas
  // an element that carries the handler is not.
  const payloads = [
    '<script>alert(1)</script>',
    '<img src=x onerror="alert(1)">',
    '<svg/onload=alert(1)>',
    '<iframe src="https://evil.example"></iframe>',
    '<a href="javascript:alert(1)">click</a>',
    '<div style="position:fixed;inset:0">overlay</div>',
    '<style>body{display:none}</style>',
    '<form action="https://evil.example"><input name="x"></form>',
    '<p onclick="alert(1)">text</p>',
    '<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">data</a>',
  ]
  for (const payload of payloads) {
    const html = renderMarkdown(payload)
    const host = dom.window.document.createElement('div')
    host.innerHTML = html
    for (const element of host.querySelectorAll('*')) {
      const tag = element.tagName.toLowerCase()
      assert.equal(
        ['script', 'iframe', 'form', 'style', 'object', 'embed'].includes(tag),
        false,
        `forbidden element <${tag}> survived: ${payload} -> ${html}`,
      )
      for (const attribute of element.getAttributeNames()) {
        assert.equal(attribute.startsWith('on'), false, `event handler ${attribute} survived: ${html}`)
        assert.notEqual(attribute, 'style', `style attribute survived: ${html}`)
        for (const url of [element.getAttribute('href'), element.getAttribute('src')]) {
          assert.equal(/^\s*(javascript|data|vbscript):/i.test(url ?? ''), false, `dangerous URL ${url} survived: ${html}`)
        }
      }
    }
  }
})

test('the parser is configured to escape raw HTML', () => {
  // Belt and braces: if this setting is ever flipped, the sanitiser still runs,
  // but the setting itself is part of the promise and is pinned here.
  assert.equal(markdownOptions.html, false)
})

test('markdown basics render as expected', () => {
  const html = renderMarkdown([
    '# Title',
    '',
    'A paragraph with **bold**, *italic* and `code`.',
    '',
    '- one',
    '- two',
    '',
    '1. first',
    '',
    '> quoted',
    '',
    '```go',
    'func main() {}',
    '```',
  ].join('\n'))
  assert.match(html, /<h1>Title<\/h1>/)
  assert.match(html, /<strong>bold<\/strong>/)
  assert.match(html, /<em>italic<\/em>/)
  assert.match(html, /<code>code<\/code>/)
  assert.match(html, /<ul>/)
  assert.match(html, /<ol>/)
  assert.match(html, /<blockquote>/)
  assert.match(html, /<pre><code class="language-go">/)
})

test('line breaks written by a person are preserved', () => {
  // Someone typing a comment expects the line they ended to stay ended.
  const html = renderMarkdown('first line\nsecond line')
  assert.match(html, /first line<br>\s*second line/)
})

test('autolinks become links and are sanitised', () => {
  const html = renderMarkdown('see https://example.com/docs for details')
  assert.match(html, /<a href="https:\/\/example\.com\/docs"/)
  // A link target that is not a real protocol must not become clickable.
  const bad = renderMarkdown('[x](javascript:alert(1))')
  assert.equal(/href="javascript:/i.test(bad), false, bad)
})

test('#123 becomes a link when the caller can resolve it', () => {
  const html = renderMarkdown('fixed in #42', { issueHref: number => `/engineering/demo/issues/${number}` })
  assert.match(html, /<a href="\/engineering\/demo\/issues\/42" class="md-reference">#42<\/a>/)
  // Without a resolver it stays plain text rather than becoming a dead link.
  const plain = renderMarkdown('fixed in #42')
  assert.match(plain, /#42/)
  assert.equal(/<a /.test(plain), false, plain)
})

test('@mentions are marked without becoming dead links', () => {
  const html = renderMarkdown('thanks @alice for the fix')
  assert.match(html, /<span class="md-mention">@alice<\/span>/)
  assert.equal(/<a /.test(html), false, html)
  // A bare @ with nothing after it is ordinary punctuation.
  assert.equal(renderMarkdown('email me @ home').includes('md-mention'), false)
})

test('a mention inside a code span stays literal', () => {
  const html = renderMarkdown('use `@alice` as the handle')
  assert.equal(html.includes('md-mention'), false, html)
  assert.match(html, /<code>@alice<\/code>/)
})

test('task lists render as read-only checkboxes', () => {
  const html = renderMarkdown('- [x] done\n- [ ] not done')
  const boxes = html.match(/<input class="md-task"[^>]*>/g) ?? []
  assert.equal(boxes.length, 2, html)
  // Exactly one is ticked, and neither is interactive: toggling would have to
  // rewrite the source, so a box that looks clickable would be misleading.
  assert.equal(boxes.filter(box => box.includes('checked')).length, 1, html)
  assert.equal(boxes.every(box => box.includes('disabled')), true, html)
  assert.match(html, /md-task-item/)
})

test('a checkbox-looking line that is not a task item is left alone', () => {
  const html = renderMarkdown('- [maybe] not a task')
  assert.equal(html.includes('md-task'), false, html)
})

test('tables render', () => {
  const html = renderMarkdown('| a | b |\n| - | - |\n| 1 | 2 |')
  assert.match(html, /<table>/)
  assert.match(html, /<th>a<\/th>/)
  assert.match(html, /<td>1<\/td>/)
})

test('an empty body renders nothing rather than an empty wrapper', () => {
  assert.equal(renderMarkdown(''), '')
})
