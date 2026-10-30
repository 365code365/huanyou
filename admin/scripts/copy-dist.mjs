// Copies the static export (out/) into the Go API's embed directory.
import { cpSync, existsSync, rmSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const src = resolve(root, 'out')
const dst = resolve(root, '../server/internal/api/adminui/dist')
if (!existsSync(src)) throw new Error('out/ not found; did next build succeed?')
rmSync(dst, { recursive: true, force: true })
cpSync(src, dst, { recursive: true })
console.log('copied', src, '->', dst)
