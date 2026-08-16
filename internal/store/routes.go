package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
)

// ListRoutes 按域名升序返回全部路由。
//
// 排序不是为了好看：渲染器要求确定性输出，虽然它自己也会排一次，
// 但让存储层就返回稳定顺序能让「预览两次结果不同」这类问题少一个来源。
func (s *Store) ListRoutes(ctx context.Context) ([]model.Route, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT domain, upstream, block_mode, mtls, compress, body_max, whitelist, version
		FROM proxy_routes ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("查询路由: %w", err)
	}
	defer rows.Close()

	out := make([]model.Route, 0, 8)
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRoute(ctx context.Context, domain string) (model.Route, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT domain, upstream, block_mode, mtls, compress, body_max, whitelist, version
		FROM proxy_routes WHERE domain = ?`, domain)
	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Route{}, ErrNotFound
	}
	return r, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRoute(sc scanner) (model.Route, error) {
	var (
		r        model.Route
		mtls     int
		compress int
		wl       string
	)
	if err := sc.Scan(&r.Domain, &r.Upstream, &r.Block, &mtls, &compress, &r.BodyMax, &wl, &r.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Route{}, err
		}
		return model.Route{}, fmt.Errorf("读取路由行: %w", err)
	}
	r.MTLS = mtls == 1
	r.Compress = compress == 1
	if err := decodeJSON(wl, &r.Whitelist); err != nil {
		return model.Route{}, fmt.Errorf("路由 %s 的白名单: %w", r.Domain, err)
	}
	if r.Whitelist == nil {
		r.Whitelist = []string{}
	}
	return r, nil
}

// PutRoute 写入或覆盖一条路由。
func (s *Store) PutRoute(ctx context.Context, r model.Route) error {
	wl, err := encodeJSON(nonNil(r.Whitelist))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO proxy_routes (domain, upstream, block_mode, mtls, compress, body_max, whitelist, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
			upstream=excluded.upstream, block_mode=excluded.block_mode, mtls=excluded.mtls,
			compress=excluded.compress, body_max=excluded.body_max,
			whitelist=excluded.whitelist, version=excluded.version`,
		r.Domain, r.Upstream, string(r.Block), boolToInt(r.MTLS), boolToInt(r.Compress),
		r.BodyMax, wl, r.Version)
	if err != nil {
		return fmt.Errorf("写入路由 %s: %w", r.Domain, err)
	}
	return nil
}

// DeleteRoute 删除一条路由。目标不存在时返回 ErrNotFound——
// 静默成功会让「删错了域名」看起来像删对了。
func (s *Store) DeleteRoute(ctx context.Context, domain string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxy_routes WHERE domain = ?`, domain)
	if err != nil {
		return fmt.Errorf("删除路由 %s: %w", domain, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpRouteVersions 把指定域名的版本号 +1，用于下发成功后推进资源版本。
func (s *Store) BumpRouteVersions(ctx context.Context, domains []string) error {
	for _, d := range domains {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE proxy_routes SET version = version + 1 WHERE domain = ?`, d); err != nil {
			return fmt.Errorf("推进 %s 的版本号: %w", d, err)
		}
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
