import { setupWorker } from 'msw/browser'
import { handlers } from './handlers'

export const worker = setupWorker(...handlers)

/** dev 下启动 mock；VITE_USE_MOCK=false 时整层不介入，直连真主控。 */
export async function startMocks(): Promise<void> {
  await worker.start({
    onUnhandledRequest: 'bypass',
    quiet: true,
  })
}
