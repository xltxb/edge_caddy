#!/usr/bin/env python3
"""重启**本地开发用**的主控（8080 上那个 `go run ./cmd/master`）。

    python3 scripts/dev-restart-master.py

# 它解决的问题

前端那套 `check:shapes` 拿 mock 与真主控比形状，而「真主控」是那个跑着的
进程 —— 后端提交了新字段而进程没重启的话，它比出来的分歧不是「mock 错了」，
是**进程比代码旧**。这件事撞了三次，每次都要后端那边动手重启一下。

**这个脚本让任何一边都能自己来。** 它只碰 8080 上那个进程，
不碰灰度、不碰数据库。

# 它为什么要读运行中进程的环境

主控的配置全在环境变量里（EC_DATABASE_URL / EC_SECRET_KEY / …），
而它们是人在起那个进程时给的，没有落在任何文件里。
所以重启只能沿用它自己的那一份。

**EC_SECRET_KEY 是密钥**：它只从 ps 流进 os.environ 再流进子进程，
中间不经过任何会被人看到的地方 —— 这个脚本只打印变量名，不打印值。
"""
import os, signal, subprocess, sys, time

REPO = "/Users/abiu/GolandProjects/project/caddy-manage-system"

def pid_on(port):
    out = subprocess.run(["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN", "-t"],
                         capture_output=True, text=True).stdout.split()
    return int(out[0]) if out else None

def ppid(pid):
    out = subprocess.run(["ps", "-o", "ppid=", "-p", str(pid)],
                         capture_output=True, text=True).stdout.strip()
    return int(out) if out.isdigit() else None

old = pid_on(8080)
if not old:
    print("  8080 上没有主控 —— 直接起一个新的")
    env = dict(os.environ)
    names = []
else:
    out = subprocess.run(["ps", "-Eww", "-p", str(old)],
                         capture_output=True, text=True).stdout
    env = dict(os.environ)
    names = []
    for tok in out.split():
        if tok.startswith("EC_") and "=" in tok:
            k, v = tok.split("=", 1)
            env[k] = v
            names.append(k)
    if len(names) < 5:
        print(f"✗ 只取到 {len(names)} 个 EC_ 变量（{sorted(names)}）—— 不够，不敢重启")
        sys.exit(2)
    print("  取到环境变量：" + " ".join(sorted(names)))

    for pid in ([ppid(old)] if ppid(old) else []) + [old]:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    for _ in range(50):
        time.sleep(0.2)
        if pid_on(8080) is None:
            break
    else:
        print("✗ 旧进程没退干净")
        sys.exit(2)
    print(f"  停了旧的（{old}）")

log = open("/tmp/ec-master.log", "a")
subprocess.Popen(["go", "run", "./cmd/master"], cwd=REPO, env=env,
                 stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
for _ in range(150):
    time.sleep(0.2)
    if pid_on(8080):
        print(f"  起来了（{pid_on(8080)}）")
        sys.exit(0)
print("✗ 60 秒内没起来 —— 看 /tmp/ec-master.log")
sys.exit(1)
