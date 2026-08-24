package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// GeoDB 是主控持有的那份 GeoIP 库。
type GeoDB struct {
	MMDB      []byte
	SHA256    string
	Source    string // maxmind | manual
	UpdatedAt time.Time
}

// GetGeoDB 读出当前的库。没有时回 ErrNotFound。
//
// **withBytes 为 false 时不读那几 MB**：心跳比对每几秒一次，
// 而它只需要那个哈希。每次都把库读出来会让一个每秒都在发生的动作
// 变成一次几 MB 的内存拷贝。
func (s *Store) GetGeoDB(ctx context.Context, withBytes bool) (GeoDB, error) {
	var g GeoDB
	var err error
	if withBytes {
		err = s.Pool.QueryRow(ctx,
			`SELECT mmdb, sha256, source, updated_at FROM geoip_db WHERE id = 1`).
			Scan(&g.MMDB, &g.SHA256, &g.Source, &g.UpdatedAt)
	} else {
		err = s.Pool.QueryRow(ctx,
			`SELECT sha256, source, updated_at FROM geoip_db WHERE id = 1`).
			Scan(&g.SHA256, &g.Source, &g.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// PutGeoDB 换上一份库。
func (s *Store) PutGeoDB(ctx context.Context, g GeoDB) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO geoip_db (id, mmdb, sha256, source, updated_at)
		 VALUES (1, $1, $2, $3, now())
		 ON CONFLICT (id) DO UPDATE SET
		   mmdb = EXCLUDED.mmdb, sha256 = EXCLUDED.sha256,
		   source = EXCLUDED.source, updated_at = now()`,
		g.MMDB, g.SHA256, g.Source)
	return err
}
