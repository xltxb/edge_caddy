package agent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// buildTestDB 造一份最小的 mmdb。
//
// **必须是真库，不能是打桩的接口。** mmdb 是一种二进制格式，
// 而这一层的价值正在于「我们读对了那个格式」——
// 打桩验的是我们自己的想象。
func buildTestDB(t *testing.T, entries map[string]string) string {
	t.Helper()
	// **别用文档保留段**（203.0.113.0/24 之类）：mmdbwriter 拒绝往保留网络里插，
	// 报的是「which is a reserved network」——而那句话不会让人想到
	// 「我挑的示例地址正好是 RFC 5737 里的」。
	w, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: "GeoLite2-Country", RecordSize: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	for cidr, iso := range entries {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		rec := mmdbtype.Map{"country": mmdbtype.Map{"iso_code": mmdbtype.String(iso)}}
		if err := w.Insert(n, rec); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "test.mmdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := w.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGeoLookupReadsRealMMDB 钉的是**我们读对了那个二进制格式**。
func TestGeoLookupReadsRealMMDB(t *testing.T) {
	path := buildTestDB(t, map[string]string{
		"1.2.3.0/24":  "CN",
		"5.6.7.0/24": "US",
	})
	g := newGeoDB()
	if err := g.Load(path); err != nil {
		t.Fatal(err)
	}

	for ip, want := range map[string]string{
		"1.2.3.7":   "CN",
		"5.6.7.42": "US",
		"10.0.0.1":      "", // 库里没有 —— 与「没有库」是两回事
	} {
		got, err := g.Country(ip)
		if err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		if got != want {
			t.Errorf("%s → %q，想要 %q", ip, got, want)
		}
	}
}

// TestNoDBIsDistinctFromNotFound 钉的是**这两件事必须分得开**。
//
// 「查不到这个 IP」（内网、保留段）应当放行；
// 「本机没有库」是配置没到位 —— 混成一个的话，
// 一台还没收到库的节点会把所有内网地址当成同一档处理，
// 而那两档的正确处置恰好相反。
func TestNoDBIsDistinctFromNotFound(t *testing.T) {
	g := newGeoDB()
	if _, err := g.Country("1.2.3.7"); !errors.Is(err, ErrNoGeoDB) {
		t.Fatalf("没有库时该回 ErrNoGeoDB，实际 %v", err)
	}

	g2 := newGeoDB()
	if err := g2.Load(buildTestDB(t, map[string]string{"1.2.3.0/24": "CN"})); err != nil {
		t.Fatal(err)
	}
	c, err := g2.Country("10.0.0.1")
	if err != nil {
		t.Fatalf("库里没有这个 IP 不该报错，实际 %v", err)
	}
	if c != "" {
		t.Errorf("库里没有的 IP 该回空国家，实际 %q", c)
	}
}

// TestGeoAllowsDirections 钉的是**两个方向的默认相反**。
//
// 而「查不到国家」在两个方向下的正确处置也相反：
// 一个「只放行中国」的规则遇到查不到的地址应当**拦**，
// 一个「封禁中国」的规则遇到同一个地址应当**放**。
// 合成一个「方向开关」的话，这一档几乎必然被写错一半。
func TestGeoAllowsDirections(t *testing.T) {
	cases := []struct {
		country, mode string
		list          []string
		want          bool
		why           string
	}{
		{"CN", "block", []string{"CN"}, false, "封禁名单里的国家要拦"},
		{"US", "block", []string{"CN"}, true, "不在封禁名单里的要放"},
		{"CN", "allow", []string{"CN"}, true, "放行名单里的国家要放"},
		{"US", "allow", []string{"CN"}, false, "不在放行名单里的要拦"},
		{"", "block", []string{"CN"}, true, "查不到国家时，封禁模式该放行"},
		{"", "allow", []string{"CN"}, false, "查不到国家时，只放行模式该拦 —— " +
			"一个「只放行中国」的规则不能因为查不到就放进来"},
	}
	for _, c := range cases {
		if got := geoAllows(c.country, c.mode, c.list); got != c.want {
			t.Errorf("country=%q mode=%q → %v，想要 %v（%s）",
				c.country, c.mode, got, c.want, c.why)
		}
	}
}

// TestReloadSwapsWithoutClosingInUse 钉的是**先换再关**。
//
// 反过来的话，正在查库的请求会拿到一个已关闭的 reader —— 那是一次 panic，
// 而它只在换库那一瞬间发生，复现不出来。
func TestReloadSwapsWithoutClosingInUse(t *testing.T) {
	g := newGeoDB()
	a := buildTestDB(t, map[string]string{"1.2.3.0/24": "CN"})
	b := buildTestDB(t, map[string]string{"1.2.3.0/24": "US"})
	if err := g.Load(a); err != nil {
		t.Fatal(err)
	}
	if c, _ := g.Country("1.2.3.7"); c != "CN" {
		t.Fatalf("装置坏了：第一份库该回 CN，实际 %q", c)
	}
	if err := g.Load(b); err != nil {
		t.Fatal(err)
	}
	if c, _ := g.Country("1.2.3.7"); c != "US" {
		t.Errorf("换库之后该回 US，实际 %q —— 新库没生效", c)
	}
	// 卸载。
	if err := g.Load(""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Country("1.2.3.7"); !errors.Is(err, ErrNoGeoDB) {
		t.Error("卸载之后该回 ErrNoGeoDB")
	}
}
