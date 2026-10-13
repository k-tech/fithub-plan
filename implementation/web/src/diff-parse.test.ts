import assert from 'node:assert/strict'
import { test } from 'node:test'
import { parsePatch } from './diff-parse.ts'

// The line numbers produced here are exactly what the server re-derives to
// validate an inline comment anchor, so a wrong count would make every comment
// on an affected file fail or, worse, land on the wrong line.
const patch = `diff --git a/internal/a.go b/internal/a.go
index 1111111..2222222 100644
--- a/internal/a.go
+++ b/internal/a.go
@@ -1,3 +1,4 @@
 package a
 
+import "fmt"
 var x = 1
`

test('tracks old and new line numbers across a hunk', () => {
  const lines = parsePatch(patch)
  const added = lines.find(line => line.kind === 'add')
  assert.deepEqual(
    { text: added?.text, newLine: added?.newLine, path: added?.path },
    { text: 'import "fmt"', newLine: 3, path: 'internal/a.go' },
  )
  // The context line after the addition sits on different old/new numbers:
  // the inserted line shifts only the new side.
  const context = lines.filter(line => line.kind === 'context').at(-1)
  assert.deepEqual({ text: context?.text, oldLine: context?.oldLine, newLine: context?.newLine }, { text: 'var x = 1', oldLine: 3, newLine: 4 })
  // A blank context line keeps both counters in step.
  const blank = lines.filter(line => line.kind === 'context')[1]
  assert.deepEqual({ oldLine: blank?.oldLine, newLine: blank?.newLine }, { oldLine: 2, newLine: 2 })
})

test('keeps removal anchors on the old side only', () => {
  const removal = ['diff --git a/old.txt b/old.txt', '--- a/old.txt', '+++ b/old.txt', '@@ -1,2 +1 @@', '-first', ' keep', ''].join('\n')
  const deleted = parsePatch(removal).find(line => line.kind === 'del')
  assert.deepEqual({ text: deleted?.text, oldLine: deleted?.oldLine, newLine: deleted?.newLine }, { text: 'first', oldLine: 1, newLine: undefined })
})

test('never produces an anchor for headers or hunk markers', () => {
  const lines = parsePatch(patch)
  const header = lines.find(line => line.kind === 'file')
  const hunk = lines.find(line => line.kind === 'hunk')
  const meta = lines.find(line => line.kind === 'meta' && line.text.startsWith('+++'))
  assert.equal(header?.newLine, undefined)
  assert.equal(hunk?.newLine, undefined)
  assert.equal(meta?.path, 'internal/a.go')
})

test('a new file has new-side anchors but no old-side ones', () => {
  const added = ['diff --git a/new.txt b/new.txt', 'new file mode 100644', '--- /dev/null', '+++ b/new.txt', '@@ -0,0 +1,2 @@', '+one', '+two', ''].join('\n')
  const lines = parsePatch(added).filter(line => line.kind === 'add')
  assert.deepEqual(lines.map(line => [line.text, line.newLine, line.oldLine]), [['one', 1, undefined], ['two', 2, undefined]])
})
