import { execFile } from 'node:child_process'
import { readFile, writeFile } from 'node:fs/promises'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const tag = process.argv[2]
const apiKey = process.env.OPENROUTER_API_KEY

if (!tag || !/^v\d+\.\d+\.\d+$/.test(tag)) {
  throw new Error('A semantic release tag is required')
}
if (!apiKey) throw new Error('OPENROUTER_API_KEY is required')

async function git(args) {
  const { stdout } = await execFileAsync('git', args, {
    maxBuffer: 4 * 1024 * 1024,
  })
  return stdout.trim()
}

const tags = (await git(['tag', '--list', 'v*', '--sort=-version:refname']))
  .split('\n')
  .filter(Boolean)
const index = tags.indexOf(tag)
if (index < 0) throw new Error(`Release tag does not exist: ${tag}`)

const previousTag = tags[index + 1] ?? null
const range = previousTag ? `${previousTag}..${tag}` : tag
const emptyTree = await git(['hash-object', '-t', 'tree', '/dev/null'])
const diffArgs = previousTag ? [range] : [emptyTree, tag]
const [prompt, commits, files, stat] = await Promise.all([
  readFile('.github/prompts/github-create-release.md', 'utf8'),
  git(['log', '--reverse', '--format=%h %s', range]),
  git(['diff', '--name-only', ...diffArgs]),
  git(['diff', '--stat', ...diffArgs]),
])

const context = [
  `Target tag: ${tag}`,
  `Previous tag: ${previousTag ?? '(none)'}`,
  `Git range: ${range}`,
  '',
  'Commits:',
  commits || '(none)',
  '',
  'Changed files:',
  files || '(none)',
  '',
  'Diff stat:',
  stat || '(none)',
].join('\n')

const response = await fetch('https://openrouter.ai/api/v1/chat/completions', {
  method: 'POST',
  headers: {
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
    'HTTP-Referer': 'https://github.com/markwylde/gitea-runner-lume',
    'X-Title': 'Gitea Runner Lume release notes',
  },
  body: JSON.stringify({
    model: 'anthropic/claude-haiku-4.5',
    messages: [
      { role: 'system', content: prompt },
      { role: 'user', content: context },
    ],
    temperature: 0.2,
  }),
  signal: AbortSignal.timeout(120_000),
})

if (!response.ok) {
  throw new Error(`OpenRouter request failed with status ${response.status}`)
}
const payload = await response.json()
const notes = payload?.choices?.[0]?.message?.content?.trim()
if (!notes || notes.length > 64 * 1024 || notes.startsWith('```')) {
  throw new Error('OpenRouter returned invalid release notes')
}
await writeFile('RELEASE.md', `${notes}\n`, { flag: 'wx', mode: 0o600 })
