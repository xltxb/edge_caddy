package agent

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

// ErrNoGeoDB 表示本机还没有 GeoIP 库。
//
// **它与「查不到这个 IP」是两回事**，而这两件事的处置相反：
// 查不到（内网地址、保留段）应当放行；没有库是配置没到位，
// 那时**每个受地域规则保护的域名都会拒绝所有人**，而那不是任何人想要的。
var ErrNoGeoDB = errors.New("本机还没有 GeoIP 库")

// geoDB 是本机的 GeoIP 库。**可以为空**：库由主控下发，
// 而下发到达之前节点已经在服务了。
type geoDB struct {
	mu     sync.RWMutex
	reader *maxminddb.Reader
	path   string
}

func newGeoDB() *geoDB { return &geoDB{} }

// Load 打开一个 mmdb 文件。空路径表示卸载。
func (g *geoDB) Load(path string) error {
	if path == "" {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.reader != nil {
			_ = g.reader.Close()
		}
		g.reader, g.path = nil, ""
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// **先换再关。** 反过来的话，正在查库的请求会拿到一个已关闭的 reader
	// —— 那是一次 panic，而它只在换库那一瞬间发生，复现不出来。
	old := g.reader
	g.reader, g.path = r, path
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Country 回这个 IP 属于哪个国家（ISO 3166-1 alpha-2）。
//
// 三种结果要分开，因为处置不同：
//
//	("CN", nil)            查到了
//	("",   nil)            **库里没有这个 IP** —— 内网、保留段、或者库覆盖不到
//	("",   ErrNoGeoDB)     本机没有库
func (g *geoDB) Country(ip string) (string, error) {
	g.mu.RLock()
	r := g.reader
	g.mu.RUnlock()
	if r == nil {
		return "", ErrNoGeoDB
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		// 解析不出来的地址不该让请求失败：那多半是我们自己的头填错了，
		// 而把它变成 403 会把整个域名封掉。
		return "", nil
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := r.Lookup(addr).Decode(&rec); err != nil {
		return "", nil
	}
	return rec.Country.ISOCode, nil
}

func (g *geoDB) Path() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.path
}

// geoAllows 说这次请求过不过得了这条地域规则。
//
// # 库缺失时放行，而且要说出来
//
// **这是这一层最要紧的决定。** 拒绝的话，库还没下发到的那段时间里
// （新装的节点、下发失败、文件被删），每个受地域规则保护的域名
// **对所有人都是 403** —— 而配置看起来完全正常。
//
// 这与校验端点整体的 fail-closed 不同，而区别是有理由的：
// 服务密钥验不过说明**这个请求**没有凭据，而没有库说明**我们**没准备好。
// 把我们的问题变成所有访问者的 403，是把一次运维疏忽放大成一次全站故障。
//
// 代价说在明处：**库没到之前地域规则形同虚设**。所以它必须被看见——
// 调用方会把这件事记进日志并让节点报上去。
func geoAllows(country string, mode string, list []string) bool {
	in := false
	for _, c := range list {
		if c == country {
			in = true
			break
		}
	}
	if mode == "allow" {
		// 白名单：不在名单里的拦。**查不到国家的算不在名单里**——
		// 一个「只放行中国」的规则，遇到查不到的地址应当拦，不是放。
		return in
	}
	// 黑名单：在名单里的拦。查不到国家的放行。
	return !in
}

// GeoDBPath 是 GeoIP 库在节点上的落点。
//
// 与回源证书同一个目录：那个目录已经是 Agent 独占且权限收好的，
// 而多开一个目录意味着多一处要在部署脚本里对齐的路径。
func GeoDBPath(stateDir string) string {
	if stateDir == "" {
		stateDir = "/var/lib/edge-agent"
	}
	return filepath.Join(stateDir, "GeoLite2-Country.mmdb")
}

// writeGeoDB 把解压后的库原子写入落点。
//
// **先写临时文件再 rename。** 直接覆写的话，写到一半时进程挂掉会留下
// 一个截断的文件 —— 而 maxminddb 打开它会报一句关于格式的错，
// 那句话不会让人想到「上次写库被打断了」。
func writeGeoDB(path string, mmdb []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, mmdb, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// gunzip 解开主控推来的那份。
func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	// 上限与主控那边一致：一条构造出来的消息不该让节点把内存吃光。
	return io.ReadAll(io.LimitReader(zr, 32<<20))
}
