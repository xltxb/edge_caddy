package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/secret"
)

// CommitDeploy 是一次下发落库的**全部**动作，在一个事务里。
//
// # 为什么必须是一个事务
//
// 这七步原先是七个独立的调用，而只有第一步（合入 live）的失败会中止流水线
// ——那是 issue #31 的修复。后面五步各自只 log.Error 然后继续往下走：
//
//   - SetBaseline 报错 → 基线停在旧版，而草稿照样被删。随后 retry.go 读到
//     cur != job.cfgVersion，把所有掉队节点标成「已被新的下发取代」——假话。
//   - 合入 live 自己也是逐条 Upsert：第 3 条失败时前 2 条已经落库，而
//     deploy.go 那句注释写着「停下来之后的状态是真话：草稿还在、基线没动」。
//     live 已经半更新了，那句话不成立（issue #43）。
//
// docs/agents/domain.md:722「多处改动写在一个脚本里，一处失败全盘不生效」，
// 以及 :380「丢弃错误是安全的，**当且仅当后果会被后续检查捕获**」——
// 这里后续没有任何检查。
//
// # 顺序
//
// 先合入 live，再确立基线：基线宣称的是「live 渲染出来就是这一版」，
// 它不该早于那件事成立。版本号推进与删草稿放在最后，它们都以前两步为前提。
type CommitDeploy struct {
	// ResKeys 是这次下发选中的资源。live 只合入它们，版本号也只推进它们。
	ResKeys []string
	Routes  []model.Route
	Rules   []model.Rule
	// Policies 是合并了草稿之后的全局策略。
	//
	// **它原先不在这里**，而 `global:` 的版本照样被推进、草稿照样被删——
	// 只有 live 那一行的 spec 没动。下一次任何下发重新渲染时，全局策略
	// 退回旧值，而工作台上没有草稿、版本号是新的、基线是新的，
	// **没有任何一个页面会说这件事**。issue #31 的形状，在 global: 上原样成立。
	Policies []model.Policy

	CfgVersion string
	DeployID   int64

	// Sealer 供规则的共享密钥落库。这一步传空串（密钥不在草稿里，
	// 也不该被这一步碰），所以它只在规则本来就带密钥时才用得上。
	Sealer *secret.Sealer

	// Fault 仅供测试：在 live 已经合入、基线尚未确立的那一刻返回错误，
	// 用来验证整批真的回滚了。生产路径上它永远是 nil。
	Fault func() error
}

// CommitDeploy 执行一次下发的落库。任何一步失败，整批不生效。
func (s *Store) CommitDeploy(ctx context.Context, in CommitDeploy) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	selected := map[string]bool{}
	for _, k := range in.ResKeys {
		selected[k] = true
	}

	for _, r := range in.Routes {
		if !selected["route:"+r.Domain] {
			continue
		}
		if err := upsertRoute(ctx, tx, r); err != nil {
			return fmt.Errorf("合入路由 %s: %w", r.Domain, err)
		}
	}
	for _, r := range in.Rules {
		if !selected["rule:"+r.ID] {
			continue
		}
		// 密钥传空串表示保持不变——它不在草稿里，也不该被这一步碰。
		if err := upsertRule(ctx, tx, r, "", in.Sealer); err != nil {
			return fmt.Errorf("合入规则 %s: %w", r.ID, err)
		}
	}

	for _, p := range in.Policies {
		if !selected["global:"+p.ID] {
			continue
		}
		if err := upsertPolicy(ctx, tx, p); err != nil {
			return fmt.Errorf("合入全局策略 %s: %w", p.ID, err)
		}
	}

	if in.Fault != nil {
		if err := in.Fault(); err != nil {
			return err
		}
	}

	if err := setBaseline(ctx, tx, in.CfgVersion, in.DeployID); err != nil {
		return fmt.Errorf("确立基线: %w", err)
	}
	if err := bumpVersions(ctx, tx,
		`UPDATE proxy_routes SET version = version + 1 WHERE domain = ANY($1)`,
		keysWithPrefix(in.ResKeys, "route:")); err != nil {
		return fmt.Errorf("推进路由版本: %w", err)
	}
	if err := bumpVersions(ctx, tx,
		`UPDATE access_rules SET version = version + 1 WHERE id = ANY($1)`,
		keysWithPrefix(in.ResKeys, "rule:")); err != nil {
		return fmt.Errorf("推进规则版本: %w", err)
	}
	if err := bumpVersions(ctx, tx,
		`UPDATE global_policies SET version = version + 1 WHERE id = ANY($1)`,
		keysWithPrefix(in.ResKeys, "global:")); err != nil {
		return fmt.Errorf("推进策略版本: %w", err)
	}
	if err := deleteDrafts(ctx, tx, in.ResKeys); err != nil {
		return fmt.Errorf("清空草稿: %w", err)
	}
	return tx.Commit(ctx)
}

// keysWithPrefix 把 "route:api.example.com" 这样的资源键剥成裸 id。
//
// 放在 store 而不是 deploy：事务内的三条 Bump 需要它，而让 store 去依赖
// deploy 是反的。deploy 里曾经有一份同名的副本，`CommitDeploy` 接手那三条
// Bump 之后它就没有调用方了——私有函数没人用编译器不会说话，所以它在仓库里
// 又活了一阵。
func keysWithPrefix(resKeys []string, prefix string) []string {
	var out []string
	for _, k := range resKeys {
		if id, ok := strings.CutPrefix(k, prefix); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}

func bumpVersions(ctx context.Context, q querier, sql string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, sql, ids)
	return err
}
