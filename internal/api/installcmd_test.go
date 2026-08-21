package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// install_cmd 里用到的每一个参数，部署脚本都必须认。
//
// 这是**两处知识**：控制台拼出一条命令字符串，脚本解析参数。它们不一致时，
// 人复制粘贴执行会拿到「未知参数」——而那发生在一台远程机器上、在他以为
// 一切就绪的时候。
//
// 这跟 contractEndpoints 那张表是同一类做法：把「必须一致的两件事」
// 变成一条会红的断言，而不是靠谁记得去核。
func TestInstallCommandFlagsAreAcceptedByDeployScript(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	_, env := do(t, r, "POST", "/api/v1/nodes/token", map[string]string{
		"node_id": "node-sg-01", "city": "新加坡", "vendor": "V.PS",
		"line": "CMIN2", "public_ip": "203.0.113.9",
	}, func(req *http.Request) { req.AddCookie(ck) })

	var d struct {
		InstallCmd string `json:"install_cmd"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	cmd := d.InstallCmd

	// 必须是部署脚本，不是裸的 edge-agent。
	//
	// 裸命令跑起来是前台进程、没有 systemd、没有 Restart=always ——
	// 而 fail-closed 依赖 Agent 存活（ADR-0003）。发一条绕过它的命令，
	// 等于让人有机会省掉脚本存在的理由。
	if !strings.Contains(cmd, "edge-node.sh install") {
		t.Fatalf("install_cmd 应当是部署脚本形式，实际:\n  %s", cmd)
	}
	if strings.HasPrefix(strings.TrimSpace(cmd), "edge-agent ") {
		t.Fatal("裸的 edge-agent 命令没有 systemd，也就没有 Restart=always")
	}

	script := readDeployScript(t)
	for _, flag := range flagsIn(cmd) {
		// 脚本的参数解析是 case 分支：--master) …
		if !strings.Contains(script, flag+")") {
			t.Errorf("install_cmd 用了 %s，而部署脚本不认这个参数", flag)
		}
	}

	// 反过来：脚本里那几个**必填**的参数，命令里一个都不能少。
	for _, must := range []string{"--master", "--node-id", "--token", "--ca-pin"} {
		if !strings.Contains(cmd, must+" ") {
			t.Errorf("install_cmd 少了必填参数 %s", must)
		}
	}
	// --agent-bin 留在命令里并给占位：它是唯一一个「你必须自己先办好」的
	// 参数，写在命令里比藏在文档里更难被跳过。
	if !strings.Contains(cmd, "--agent-bin ") {
		t.Error("install_cmd 应当带上 --agent-bin 占位，提醒人二进制要自己准备")
	}
}

func flagsIn(cmd string) []string {
	re := regexp.MustCompile(`--[a-z-]+`)
	seen := map[string]bool{}
	var out []string
	for _, f := range re.FindAllString(cmd, -1) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func readDeployScript(t *testing.T) string {
	t.Helper()
	// 从 internal/api 往上两级到仓库根。
	path := filepath.Join("..", "..", "deploy", "edge-node.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读部署脚本: %v", err)
	}
	return string(b)
}
