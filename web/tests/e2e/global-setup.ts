import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const REPO = join(process.cwd(), '..')
export const ADMIN_PASSWORD = 'correct horse battery staple'

let dir = ''
let master: ReturnType<typeof spawn> | null = null

export default async function globalSetup() {
  dir = mkdtempSync(join(tmpdir(), 'edge-e2e-'))
  const bin = join(dir, 'master')
  execSync(`go build -o ${bin} ./cmd/master`, { cwd: REPO, stdio: 'inherit' })

  master = spawn(bin, ['-http', '127.0.0.1:8099', '-grpc', '127.0.0.1:9099', '-db', join(dir, 'e.sqlite')], {
    env: { ...process.env, EDGE_SECRET_KEY: 'e2e-secret', EDGE_ADMIN_PASSWORD: ADMIN_PASSWORD },
    stdio: 'ignore',
  })

  // 等主控就绪。轮询而不是固定 sleep：固定 sleep 要么白等要么在慢机器上偶发失败。
  const deadline = Date.now() + 20_000
  for (;;) {
    try {
      const r = await fetch('http://127.0.0.1:8099/api/v1/nodes')
      if (r.status === 401) break // 401 = 起来了且鉴权已启用
    } catch {
      /* 还没起来 */
    }
    if (Date.now() > deadline) throw new Error('主控未能在 20 秒内就绪')
    await new Promise((r) => setTimeout(r, 200))
  }

  return async () => {
    master?.kill()
    if (dir) rmSync(dir, { recursive: true, force: true })
  }
}
