package agent_test

import (
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/model"
)

// 默认就起校验端点。
//
// 这条是一个真实漏洞的反面：cmd/agent 从没设过 VerifyAddr，而 Config 的约定是
// 「为空则不起」——于是访问规则在生产上完全不工作。受保护域名的 fail-closed
// 依赖校验端点存活（ADR-0003），默认不起等于把这个能力默默关掉。
func TestVerifyEndpointIsOnByDefault(t *testing.T) {
	cfg, cmd, err := agent.ParseArgs([]string{"run", "--node-id", "node-a", "--master", "m.test:9000"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "run" {
		t.Errorf("子命令应为 run，实际 %q", cmd)
	}
	if cfg.VerifyAddr != model.DefaultVerifyAddr {
		t.Fatalf("校验端点默认应监听 %s，实际 %q", model.DefaultVerifyAddr, cfg.VerifyAddr)
	}
}

// 校验端点只能在回环上：它没有任何鉴权，能连上就能替别人做鉴权决策。
func TestVerifyAddrMustBeLoopback(t *testing.T) {
	for _, bad := range []string{"0.0.0.0:2021", ":2021", "10.0.0.5:2021", "[::]:2021"} {
		if _, _, err := agent.ParseArgs([]string{
			"run", "--node-id", "n", "--master", "m.test:9000", "--verify-addr", bad,
		}); err == nil {
			t.Errorf("校验端点 %q 对外监听，应被拒绝", bad)
		}
	}
	for _, good := range []string{"127.0.0.1:2021", "127.0.0.1:9999", "[::1]:2021"} {
		if _, _, err := agent.ParseArgs([]string{
			"run", "--node-id", "n", "--master", "m.test:9000", "--verify-addr", good,
		}); err != nil {
			t.Errorf("回环地址 %q 应被接受，实际 %v", good, err)
		}
	}
}

// 显式关掉是允许的，但要说出口——不能靠「留空」这种看不出意图的方式。
func TestVerifyEndpointCanBeDisabledExplicitly(t *testing.T) {
	cfg, _, err := agent.ParseArgs([]string{
		"run", "--node-id", "n", "--master", "m.test:9000", "--verify-addr", "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyAddr != "" {
		t.Errorf("显式关闭后不该监听，实际 %q", cfg.VerifyAddr)
	}
}

// server-name 缺省取 master 的主机名——证书是签给域名的。
func TestServerNameDefaultsToMasterHost(t *testing.T) {
	cfg, _, err := agent.ParseArgs([]string{"run", "--node-id", "n", "--master", "master.example.com:9000"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "master.example.com" {
		t.Errorf("应取主控主机名，实际 %q", cfg.ServerName)
	}
}

// 必填项缺失时报错，且说清楚缺了什么。
func TestMissingRequiredArgs(t *testing.T) {
	cases := map[string][]string{
		"缺节点 ID": {"run", "--master", "m.test:9000"},
		"缺主控地址":  {"run", "--node-id", "n"},
		"没有子命令":  {},
		"未知子命令":  {"frobnicate", "--node-id", "n", "--master", "m.test:9000"},
	}
	for name, args := range cases {
		if _, _, err := agent.ParseArgs(args); err == nil {
			t.Errorf("%s 时应报错", name)
		}
	}
}

// Caddy Admin 默认也只连回环：它没有鉴权，能访问的人等于拥有这台节点。
func TestCaddyAdminDefaultsToLoopback(t *testing.T) {
	cfg, _, err := agent.ParseArgs([]string{"run", "--node-id", "n", "--master", "m.test:9000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.CaddyAdmin, "http://127.0.0.1:") {
		t.Errorf("Caddy Admin 默认应指向回环，实际 %q", cfg.CaddyAdmin)
	}
}
