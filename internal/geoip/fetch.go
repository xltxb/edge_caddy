// Package geoip 从 MaxMind 取 GeoLite2 国家库，存进主控。
//
// **节点不自己去下载。** 那要把 license key 散到每台边缘机器上，
// 而边缘机器是最可能被拿下的那些 —— 与「节点不持有 DNS 凭据」
// （ADR-0010）是同一条。
package geoip

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
)

// maxDBBytes 是解压后允许的最大体积。
//
// **不设上限的话，一个被改了的下载地址可以让主控把内存吃光** ——
// 而那不需要攻破 MaxMind，改掉一次 DNS 解析就够。
// GeoLite2-Country 约 9MB，留三倍余量。
const maxDBBytes = 32 << 20

type Fetcher struct {
	Store      *store.Store
	LicenseKey string
	Log        *slog.Logger
	HTTP       *http.Client
	// BaseOverride 让测试指到本地服务器。
	BaseOverride string
}

func (f *Fetcher) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *Fetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Fetch 下载一次，如果内容变了就存进库。回 (变了没有, 错误)。
func (f *Fetcher) Fetch(ctx context.Context) (bool, error) {
	if f.LicenseKey == "" {
		return false, errors.New("没有配置 MaxMind license key")
	}
	base := f.BaseOverride
	if base == "" {
		base = "https://download.maxmind.com/app/geoip_download"
	}
	dlURL := fmt.Sprintf("%s?edition_id=GeoLite2-Country&license_key=%s&suffix=tar.gz",
		base, f.LicenseKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		// **剥掉 URL 再包装。** Do 失败回的 *url.Error 带完整 URL，
		// 而 URL 里拼着 license key —— 这个错误每天由 once() 打进日志，
		// 一次网络抖动 key 就进了日志，而日志是运维会复制出去排障的
		// 东西（issue #28）。只留 base（它不含 key）与底层错误：
		// 「打的是哪儿、错在哪类」都在，丢掉的只有查询串。
		var ue *url.Error
		if errors.As(err, &ue) {
			return false, fmt.Errorf("下载 GeoLite2（%s %s）: %w", ue.Op, base, ue.Err)
		}
		return false, fmt.Errorf("下载 GeoLite2: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// **401 要说清是 license key 的问题。** MaxMind 回的是一段 HTML，
		// 原样带出去只会让人去查网络。
		if resp.StatusCode == http.StatusUnauthorized {
			return false, errors.New("MaxMind 拒绝了这个 license key（401）—— " +
				"检查 EC_MAXMIND_LICENSE_KEY，或者那个 key 是不是已经被吊销")
		}
		return false, fmt.Errorf("下载 GeoLite2 失败：HTTP %d", resp.StatusCode)
	}

	mmdb, err := extractMMDB(io.LimitReader(resp.Body, maxDBBytes))
	if err != nil {
		return false, err
	}

	sum := sha256.Sum256(mmdb)
	sha := hex.EncodeToString(sum[:])

	// **内容没变就不写库。** 写了的话 updated_at 会变，而节点靠哈希比对，
	// 它们不会重下 —— 于是界面上「刚更新过」而节点上的库其实是几周前那份，
	// 两句都对而对不上账。
	if cur, err := f.Store.GetGeoDB(ctx, false); err == nil && cur.SHA256 == sha {
		return false, nil
	}

	if err := f.Store.PutGeoDB(ctx, store.GeoDB{
		MMDB: mmdb, SHA256: sha, Source: "maxmind",
	}); err != nil {
		return false, err
	}
	f.log().Info("GeoIP 库已更新", "sha256", sha[:12], "字节", len(mmdb))
	return true, nil
}

// extractMMDB 从 tar.gz 里挑出那个 .mmdb。
//
// MaxMind 的包里还有 LICENSE、COPYRIGHT 之类，而目录名带日期
// （GeoLite2-Country_20260824/），所以**按后缀找，不按路径**：
// 按路径的话每周更新一次目录名就变了。
func extractMMDB(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("解压 GeoLite2 包: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读 GeoLite2 包: %w", err)
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".mmdb") {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDBBytes))
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, errors.New("包里的 .mmdb 是空的")
		}
		return b, nil
	}
	return nil, errors.New("包里没有 .mmdb —— 下载到的可能不是 GeoLite2 的包")
}

// Run 定期取一次。GeoLite2 每周二更新，一天查一次足够。
func (f *Fetcher) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 24 * time.Hour
	}
	// 启动时先取一次：主控可能停了很久，而一份过期的库会把
	// 新分配的 IP 段判错国家。
	f.once(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.once(ctx)
		}
	}
}

func (f *Fetcher) once(ctx context.Context) {
	changed, err := f.Fetch(ctx)
	if err != nil {
		// **说出来但不退出。** 取不到库不该让主控起不来：
		// 已有的那份还在用，而地域规则照常生效。
		f.log().Error("取 GeoIP 库失败", "err", err)
		return
	}
	if !changed {
		f.log().Debug("GeoIP 库没有变化")
	}
}
