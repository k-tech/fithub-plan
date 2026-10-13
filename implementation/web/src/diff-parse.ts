// A unified diff parser for the review view. It lives in a plain module so it
// can be unit-tested without JSX, and it is the only place that decides how a
// diff maps to file paths and line numbers. The same numbering is what the
// server verifies before it stores an inline comment.

export interface DiffLine {
  kind: 'file' | 'hunk' | 'add' | 'del' | 'context' | 'meta'
  text: string
  oldLine?: number
  newLine?: number
  path?: string
}

export function parsePatch(patch: string): DiffLine[] {
  const lines: DiffLine[] = []
  let path: string | undefined
  let oldLine = 0
  let newLine = 0
  let inHunk = false
  for (const raw of patch.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      inHunk = false
      path = undefined
      lines.push({ kind: 'file', text: raw.slice('diff --git '.length) })
      continue
    }
    if (raw.startsWith('+++ ')) {
      path = headerPath(raw.slice(4))
      lines.push({ kind: 'meta', text: raw, path })
      continue
    }
    if (raw.startsWith('--- ') || raw.startsWith('index ') || raw.startsWith('new file ') || raw.startsWith('deleted file ') || raw.startsWith('similarity index') || raw.startsWith('rename ')) {
      lines.push({ kind: 'meta', text: raw, path })
      continue
    }
    if (raw.startsWith('@@')) {
      const parsed = hunkStart(raw)
      if (parsed) {
        oldLine = parsed.old
        newLine = parsed.new
        inHunk = true
      }
      lines.push({ kind: 'hunk', text: raw, path })
      continue
    }
    if (!inHunk || raw === '') {
      if (raw !== '') lines.push({ kind: 'meta', text: raw, path })
      continue
    }
    if (raw.startsWith('+')) {
      lines.push({ kind: 'add', text: raw.slice(1), newLine, path })
      newLine++
    } else if (raw.startsWith('-')) {
      lines.push({ kind: 'del', text: raw.slice(1), oldLine, path })
      oldLine++
    } else if (raw.startsWith(' ')) {
      lines.push({ kind: 'context', text: raw.slice(1), oldLine, newLine, path })
      oldLine++
      newLine++
    } else {
      // "\ No newline at end of file" and anything unexpected is not a line of
      // either file, so it must never become a comment anchor.
      lines.push({ kind: 'meta', text: raw, path })
    }
  }
  return lines
}

function headerPath(value: string) {
  const trimmed = value.trim().split('\t')[0]
  if (trimmed === '/dev/null') return undefined
  return trimmed.replace(/^[ab]\//, '')
}

function hunkStart(line: string): { old: number; new: number } | null {
  const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
  if (!match) return null
  return { old: Number(match[1]), new: Number(match[2]) }
}
