package agent

import (
	"os"
	"path/filepath"
	"testing"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

func push(dir string, cert, key string) *edgev1.PushConfig {
	return &edgev1.PushConfig{
		UpstreamCertPem:  []byte(cert),
		UpstreamKeyPem:   []byte(key),
		UpstreamCertPath: filepath.Join(dir, "client.crt"),
		UpstreamKeyPath:  filepath.Join(dir, "client.key"),
	}
}

// TestUpstreamCertSurvivesAFailedWrite：一次写到一半的失败，不能留下
// 「新证书配旧私钥」。
//
// 原先是两次直接覆写（os.WriteFile → os.WriteFile）。两次之间进程被杀或机器
// 掉电，磁盘上就是新证书配旧私钥——Caddy 下次自启动按最后一份配置 provision
// 时会因为这对不上而**整份失败**，而报错与「上次写证书被打断」毫无表面关联
// （issue #62）。
//
// 同仓库的 geoip.writeGeoDB 早就建立了 tmp+rename 的范式，注释还写明了理由。
//
// **判据是磁盘上那一对还是不是原来那一对**，不是「有没有 .tmp 残留」——
// 后者在一个「写完 tmp 就 rename 第一个」的实现下同样干净，而那正是坏的那种。
func TestUpstreamCertSurvivesAFailedWrite(t *testing.T) {
	dir := t.TempDir()

	if err := writeUpstreamCert(push(dir, "OLD-CERT", "OLD-KEY")); err != nil {
		t.Fatalf("第一次写入就失败了，后面的断言没有意义: %v", err)
	}

	// **让私钥那一步写不成**：在它要用的临时路径上先放一个目录。
	// 这模拟的是「两个文件没能都落地」，而不必真的杀进程。
	if err := os.Mkdir(filepath.Join(dir, "client.key.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := writeUpstreamCert(push(dir, "NEW-CERT", "NEW-KEY")); err == nil {
		t.Fatal("私钥写不成时应当报错，而不是让证书单独换掉")
	}

	cert, err := os.ReadFile(filepath.Join(dir, "client.crt"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cert) != "OLD-CERT" || string(key) != "OLD-KEY" {
		t.Errorf("磁盘上成了 cert=%q key=%q —— 两个文件没能都落地时，"+
			"原来那一对必须原样留着，否则 Caddy 下次自启动会整份失败", cert, key)
	}
}

// TestUpstreamCertWritesBothAndLeavesNoTemp：正常情形下两个都换掉，不留残骸。
//
// 少了这条，一个「永远报错、什么都不写」的实现也能让上面那条绿。
func TestUpstreamCertWritesBothAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()

	if err := writeUpstreamCert(push(dir, "OLD-CERT", "OLD-KEY")); err != nil {
		t.Fatal(err)
	}
	if err := writeUpstreamCert(push(dir, "NEW-CERT", "NEW-KEY")); err != nil {
		t.Fatal(err)
	}

	cert, _ := os.ReadFile(filepath.Join(dir, "client.crt"))
	key, _ := os.ReadFile(filepath.Join(dir, "client.key"))
	if string(cert) != "NEW-CERT" || string(key) != "NEW-KEY" {
		t.Errorf("两个都该换成新的，实际 cert=%q key=%q", cert, key)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("留下了临时文件 %s —— 下一次写入会撞上它", e.Name())
		}
	}

	// 私钥的权限不能被这次改动放宽：它是回源身份的全部。
	fi, err := os.Stat(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("私钥权限是 %v，想要 0600", fi.Mode().Perm())
	}
}
