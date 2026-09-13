package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// TestOversizedDBIsRejectedNotTruncated：超限要报错，不能静默截断。
//
// 两处 `io.ReadAll(io.LimitReader(r, maxDBBytes))` 在超限时**返回前
// maxDBBytes 个字节且 err == nil**（issue #35）——于是一份超大的库被剪掉尾巴
// 之后当成正常库落盘。
//
// 症状离原因很远：截断的 mmdb 在加载时报的是一个**格式错误**，
// 而人会去查下载源、查 MaxMind 的账号，而不是想到「限额把它剪了」。
//
// 判据是**报错，而且那句错要说出是「太大了」**——只断言「报错」的话，
// 一个把限额设成 0 的实现也能绿，而那时每一份库都失败。
func TestOversizedDBIsRejectedNotTruncated(t *testing.T) {
	// 造一个解压后超过限额的 tar.gz。
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	big := bytes.Repeat([]byte("x"), maxDBBytes+1024)
	if err := tw.WriteHeader(&tar.Header{
		Name: "GeoLite2-Country_2026/GeoLite2-Country.mmdb",
		Mode: 0o644, Size: int64(len(big)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(big); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	zw.Close()

	got, err := extractMMDB(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatalf("超限的库应当被拒，实际拿到 %d 字节 —— "+
			"它会被当成正常库落盘，而加载时报的是一个格式错误，"+
			"人会去查下载源而不是想到限额", len(got))
	}
	if !strings.Contains(err.Error(), "超过") {
		t.Errorf("报错没说出是「太大了」：%v —— "+
			"一句笼统的失败会让人查错方向", err)
	}
}

// 正常大小的库照常解出来 —— 少了这条，一个「一律报错」的实现也能让上面绿。
func TestNormalSizedDBStillExtracts(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	content := []byte("a small but valid-looking mmdb")
	_ = tw.WriteHeader(&tar.Header{
		Name: "GeoLite2-Country_2026/GeoLite2-Country.mmdb",
		Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(content)
	tw.Close()
	zw.Close()

	got, err := extractMMDB(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("正常大小的库不该被拒：%v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("解出来的内容不对：%q", got)
	}
}
