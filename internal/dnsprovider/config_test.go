package dnsprovider_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/store"
)

var master = []byte("test-master-key")

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sample() dnsprovider.Config {
	return dnsprovider.Config{
		Kind:            dnsprovider.KindDNSPod,
		DNSPodID:        "12345",
		DNSPodToken:     "dp-token-XXXXSECRET",
		CloudflareToken: "cf-token-YYYYSECRET",
		ACMEEmail:       "ops@example.com",
		ACMEDirectory:   dnsprovider.LetsEncryptStaging,
	}
}

func TestConfigRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	want := sample()
	if err := dnsprovider.Save(ctx, st, master, want); err != nil {
		t.Fatal(err)
	}
	got, err := dnsprovider.Load(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("取回与存入不一致\n存 %+v\n取 %+v", want, got)
	}
}

// 凭据在库里是密文。
func TestCredentialsEncryptedAtRest(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := dnsprovider.Save(ctx, st, master, sample()); err != nil {
		t.Fatal(err)
	}
	blob, err := st.GetSetting(ctx, dnsprovider.SettingKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"dp-token-XXXXSECRET", "cf-token-YYYYSECRET", "12345"} {
		if strings.Contains(string(blob), plain) {
			t.Errorf("库里出现明文 %q", plain)
		}
	}
}

// 对外表示不含凭据，只说「配没配」。
func TestPublicViewHasNoCredentials(t *testing.T) {
	view := sample().Public()
	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"dp-token-XXXXSECRET", "cf-token-YYYYSECRET"} {
		if strings.Contains(string(blob), plain) {
			t.Errorf("对外表示里出现凭据 %q：%s", plain, blob)
		}
	}
	if !view.DNSPodConfigured {
		t.Error("应告诉前端 DNSPod 配过了")
	}
	if view.ACMEEmail != "ops@example.com" {
		t.Error("邮箱不是凭据，可以回显——它是运维确认「配的是哪个账号」的唯一线索")
	}
}

// 留空表示不改动，清空要显式说。
func TestMergeKeepsUnchangedCredentials(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := dnsprovider.Save(ctx, st, master, sample()); err != nil {
		t.Fatal(err)
	}
	if err := dnsprovider.Merge(ctx, st, master, dnsprovider.Config{
		Kind: dnsprovider.KindCloudflare, ACMEEmail: "new@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := dnsprovider.Load(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != dnsprovider.KindCloudflare {
		t.Errorf("服务商应已切换，实际 %q", got.Kind)
	}
	if got.DNSPodToken != sample().DNSPodToken {
		t.Error("凭据留空应表示不改动")
	}
	if got.ACMEEmail != "new@example.com" {
		t.Error("邮箱应已更新")
	}
}

// 建出来的客户端要与所选服务商一致。
func TestNewProviderMatchesKind(t *testing.T) {
	cf, err := dnsprovider.New(dnsprovider.Config{
		Kind: dnsprovider.KindCloudflare, CloudflareToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cf.Name() != "Cloudflare" {
		t.Errorf("应是 Cloudflare，实际 %s", cf.Name())
	}

	dp, err := dnsprovider.New(dnsprovider.Config{
		Kind: dnsprovider.KindDNSPod, DNSPodID: "1", DNSPodToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dp.Name() != "DNSPod" {
		t.Errorf("应是 DNSPod，实际 %s", dp.Name())
	}
}

// 凭据没配全时**拒绝构造**，而不是造一个注定 401 的客户端。
//
// 造出来的话，第一次真调用才失败，而那时的报错是服务商给的「登录失败」——
// 跟「你还没填凭据」差得很远。
func TestNewProviderRejectsIncompleteCredentials(t *testing.T) {
	cases := map[string]dnsprovider.Config{
		"没选服务商":              {},
		"Cloudflare 缺 Token": {Kind: dnsprovider.KindCloudflare},
		"DNSPod 缺 ID":        {Kind: dnsprovider.KindDNSPod, DNSPodToken: "t"},
		"DNSPod 缺 Token":     {Kind: dnsprovider.KindDNSPod, DNSPodID: "1"},
		"不认识的服务商":            {Kind: "route53"},
	}
	for name, cfg := range cases {
		if _, err := dnsprovider.New(cfg); err == nil {
			t.Errorf("%s 时应拒绝构造", name)
		}
	}
}

// 默认走 Let's Encrypt **staging**。
//
// 生产环境的速率限制是每域名每周 5 张，而调试阶段一定会反复试。默认指向正式
// 环境等于让第一次配错就烧掉一周的配额。
func TestDefaultsToStagingDirectory(t *testing.T) {
	got, err := dnsprovider.Load(context.Background(), newStore(t), master)
	if err != nil {
		t.Fatal(err)
	}
	if got.ACMEDirectory != dnsprovider.LetsEncryptStaging {
		t.Errorf("默认应指向 staging，实际 %q", got.ACMEDirectory)
	}
	if !strings.Contains(got.ACMEDirectory, "staging") {
		t.Error("staging 地址里应含 staging，否则常量写错了没人看得出来")
	}
}
