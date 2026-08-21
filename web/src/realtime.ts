import { createEdgeSocket, type EdgeSocket } from './api/ws'
import type { WsFrame } from './api/types'
import { useDeployStore } from './stores/deploy'
import { useEventsStore } from './stores/events'
import { useLinkStore } from './stores/link'
import { useNodesStore } from './stores/nodes'

/**
 * 把实时通道接到各 store 上。
 *
 * 接线放在 store 外面，是为了让 store 之间不互相 import：socket 只认识
 * 「帧」这一件事，谁消费它由这里决定。测试里可以不启动它，直接调
 * `applyHeartbeat` / `applyEvent`。
 */

let socket: EdgeSocket | null = null

function dispatch(frame: WsFrame): void {
  useLinkStore().markFrame()

  switch (frame.type) {
    case 'heartbeat':
      useNodesStore().applyHeartbeat(frame)
      break
    case 'event':
      useEventsStore().applyEvent(frame)
      break
    case 'deploy_progress':
      useDeployStore().applyProgress(frame)
      break
  }
}

export function startRealtime(): void {
  if (socket) return
  const link = useLinkStore()
  const deploy = useDeployStore()
  socket = createEdgeSocket({
    onFrame: dispatch,
    onState: (s) => {
      link.setState(s)
      // 实时断了就把进行中的下发降级为 2s 轮询；恢复后停掉轮询回到 WS。
      // 不这么做的话，一次下发会永远停在「热重载中」，而且看不出来是断了。
      if (s === 'polling' || s === 'reconnecting') deploy.startPolling()
      else deploy.stopPolling()
    },
  })
  socket.start()
}

export function stopRealtime(): void {
  socket?.stop()
  socket = null
}
