// Keeps the dependency registry honest.
//
// Adding a dependency without registering it means nothing follows upstream
// security fixes, and registering one that is not used makes the file look
// complete when it is not. Both mistakes were made in this project, so the
// check is mechanical rather than a promise to remember.
import { readFileSync, existsSync } from 'node:fs'

const pkg = JSON.parse(readFileSync('package.json', 'utf8'))
const registry = readFileSync('../THIRD_PARTY.md', 'utf8')
const declared = [...Object.keys(pkg.dependencies || {}), ...Object.keys(pkg.devDependencies || {})]

// Table rows in the registry look like "| name | version | ...".
const escape = name => name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
const problems = []

for (const name of declared) {
  const manifest = `node_modules/${name}/package.json`
  if (!existsSync(manifest)) {
    problems.push(`${name} is declared but not installed`)
    continue
  }
  const version = JSON.parse(readFileSync(manifest, 'utf8')).version
  const row = registry.match(new RegExp(`\\|\\s*${escape(name)}\\s*\\|\\s*([^|]+?)\\s*\\|`))
  if (!row) {
    problems.push(`${name} is declared but not registered in THIRD_PARTY.md`)
  } else if (row[1].trim() !== version) {
    problems.push(`${name}: registry says ${row[1].trim()}, ${version} is installed`)
  }
}

// Anything registered must still be a real dependency; a stale row is how the
// file started claiming a package that was never used.
for (const line of registry.split('\n')) {
  const cell = line.match(/^\|\s*([^\s|]+)\s*\|\s*[0-9]/)
  if (!cell) continue
  const name = cell[1]
  if (!/^@?[a-z]/.test(name)) continue
  if (!declared.includes(name) && !/^(react|react-dom|vite|typescript|tailwindcss)$/.test(name)) {
    problems.push(`${name} is registered but not declared in package.json`)
  }
}

if (problems.length > 0) {
  console.error('dependency registry check failed:')
  for (const problem of problems) console.error('  -', problem)
  process.exit(1)
}
console.log(`dependency registry check passed (${declared.length} packages)`)
