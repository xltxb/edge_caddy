package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/model"
)

var ErrNotFound = errors.New("资源不存在")

func (s *Store) ListRoutes(ctx context.Context) ([]model.Route, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT domain, upstream, block_mode::text, mtls, compress, body_max, whitelist, version
		 FROM proxy_routes ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Route
	for rows.Next() {
		var r model.Route
		var wl []byte
		if err := rows.Scan(&r.Domain, &r.Upstream, &r.BlockMode, &r.MTLS,
			&r.Compress, &r.BodyMax, &wl, &r.Version); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(wl, &r.Whitelist); err != nil {
			return nil, fmt.Errorf("解析 %s 的白名单: %w", r.Domain, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRoute(ctx context.Context, domain string) (model.Route, error) {
	var r model.Route
	var wl []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT domain, upstream, block_mode::text, mtls, compress, body_max, whitelist, version
		 FROM proxy_routes WHERE domain = $1`, domain).
		Scan(&r.Domain, &r.Upstream, &r.BlockMode, &r.MTLS, &r.Compress, &r.BodyMax, &wl, &r.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(wl, &r.Whitelist)
}

// CreateRoute 新建一条路由。version 为 0 表示尚未下发到任何节点——
// 工作台据此把它整块显示为新增。
func (s *Store) CreateRoute(ctx context.Context, r model.Route) error {
	wl, err := json.Marshal(defaultSlice(r.Whitelist))
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO proxy_routes (domain, upstream, block_mode, mtls, compress, body_max, whitelist, version)
		 VALUES ($1,$2,$3::block_mode,$4,$5,$6,$7,0)`,
		r.Domain, r.Upstream, defaultStr(r.BlockMode, model.BlockAbort),
		r.MTLS, r.Compress, defaultStr(r.BodyMax, "5MB"), wl)
	return err
}

func (s *Store) UpsertRoute(ctx context.Context, r model.Route) error {
	wl, err := json.Marshal(defaultSlice(r.Whitelist))
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO proxy_routes (domain, upstream, block_mode, mtls, compress, body_max, whitelist)
		 VALUES ($1,$2,$3::block_mode,$4,$5,$6,$7)
		 ON CONFLICT (domain) DO UPDATE SET
		   upstream = EXCLUDED.upstream, block_mode = EXCLUDED.block_mode,
		   mtls = EXCLUDED.mtls, compress = EXCLUDED.compress,
		   body_max = EXCLUDED.body_max, whitelist = EXCLUDED.whitelist`,
		r.Domain, r.Upstream, defaultStr(r.BlockMode, model.BlockAbort),
		r.MTLS, r.Compress, defaultStr(r.BodyMax, "5MB"), wl)
	return err
}

// BumpRouteVersions 在一次下发落定后推进被下发资源的版本号。
func (s *Store) BumpRouteVersions(ctx context.Context, domains []string) error {
	if len(domains) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE proxy_routes SET version = version + 1 WHERE domain = ANY($1)`, domains)
	return err
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func defaultSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
