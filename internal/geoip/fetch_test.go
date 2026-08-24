package geoip

import (
	"context"
	"strings"
	"testing"
)

// TestFetchErrorDoesNotLeakLicenseKey：**下载失败的报错里不能有 license key。**
//
// key 拼在 URL 查询串里，而 http.Client.Do 失败时返回的 *url.Error 带完整
// URL —— 这个错误每天由 once() 打进 Error 日志，一次 DNS 抖动、一次超时，
// key 就落进日志（issue #28）。日志是运维会复制出去排障的东西，
// secret 进了日志，传播就不受控了。
//
// 用「连接被拒」制造失败：那是最常见的一类网络错误，
// 也正是 *url.Error 会带 URL 的那条路。
func TestFetchErrorDoesNotLeakLicenseKey(t *testing.T) {
	const key = "SECRET-KEY-FOR-PROBE"
	f := &Fetcher{
		LicenseKey:   key,
		BaseOverride: "http://127.0.0.1:1/geoip_download", // 端口 1：连接必然被拒
	}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("装置坏了：打一个必然拒绝的端口居然成功了")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("license key 出现在错误文本里，它会被原样打进日志：%v", err)
	}
	// 报错还得有用：至少说得出打的是哪儿、错在哪类。
	if !strings.Contains(err.Error(), "127.0.0.1:1") && !strings.Contains(err.Error(), "connect") {
		t.Errorf("剥掉 key 不能把定位信息也剥没了：%v", err)
	}
}
