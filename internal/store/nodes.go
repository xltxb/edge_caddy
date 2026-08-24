package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Node 是边缘节点在主控这边的记录。
type Node struct {
	ID       string `json:"id"`
	City     string `json:"city"`
	Vendor   string `json:"vendor"`
	Line     string `json:"line"`
	PublicIP string `json:"public_ip"`
	// GeoDBSha 是这台节点上 GeoIP 库的哈希，节点自己报的。
	// 空表示它还没有库 —— 那时它上面的地域规则不生效。
	GeoDBSha string `json:"-"`
	// VerifyKinds 是这台节点的校验端点认得哪些规则类型。
	//
	// **空表示旧 Agent**（它不报这个），不是「一种都不认得」——
	// 主控按「只认得 service_secret / jwt_bearer」处理。
	VerifyKinds []string `json:"-"`
	// Status 取 StatusOK / StatusWarn / StatusDown 之一。
	Status     string     `json:"status"`
	CfgVersion string     `json:"cfg_version"`
	DNSEnabled bool       `json:"dns_enabled"`
	LastHBAt   *time.Time `json:"last_hb_at"`
	CreatedAt  time.Time  `json:"created_at"`
	// DrainedAt 非 nil 表示这台机器是**被人下线的**，与 Status 无关（ADR-0014）。
	// nil 时前端不该显示任何下线痕迹 —— 那会跟「它自己挂了」混成一件事。
	DrainedAt *time.Time `json:"drained_at"`

	// DNSReason / DNSActor / DNSChangedAt 记着**最近一次解析开关是谁改的**。
	//
	// 只有一个 dns_enabled 布尔的时候，三条关掉它的路径（人手动、系统自动摘、
	// 人下线）在数据里长得一模一样，界面只能说「未参与解析」。而三者的处置
	// 完全不同：自己关的想开就开，系统摘的要先去修那台机器，下线的要先重新上线。
	DNSReason    string     `json:"dns_reason"` // manual | auto_offline | drained
	DNSActor     string     `json:"dns_actor"`  // 操作人；系统自动摘除时为空
	DNSChangedAt *time.Time `json:"dns_changed_at"`

	// AgentVersion 是节点上跑的 Agent 版本，接入时由 Hello 带上来。
	// **灰度时人最先问的就是「我推上去的那一版到底上没上」。**
	AgentVersion string `json:"agent_version"`
}

// 节点的三档健康状态。**它是观察，不是意图**——「被人下线」是另一件事，
// 记在 DrainedAt 上（ADR-0014）。
//
// 这三个值此前散落成裸字符串（health 里一处、dnssched 里一处、SQL 里几处）。
// 两处用同一个字面量而没有共同来源，是「改一处漏一处」的标准形状。
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusDown = "down"
)

// UpsertNode 在接入时写入或更新节点。同一台机器重新接入时更新元信息，
// 不清掉 cfg_version —— 它记的是节点上生效的版本，重新接入并不改变那个事实。
func (s *Store) UpsertNode(ctx context.Context, spec NodeSpec) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO edge_nodes (id, city, vendor, line, public_ip)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET
		   city = EXCLUDED.city, vendor = EXCLUDED.vendor,
		   line = EXCLUDED.line, public_ip = EXCLUDED.public_ip`,
		spec.NodeID, spec.City, spec.Vendor, spec.Line, spec.PublicIP)
	return err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, city, vendor, line, host(public_ip), status::text,
		        cfg_version, dns_enabled, last_hb_at, created_at, drained_at,
		        coalesce(dns_reason::text, ''), coalesce(dns_actor, ''), dns_changed_at,
		        agent_version, geo_db_sha, verify_kinds
		 FROM edge_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.City, &n.Vendor, &n.Line, &n.PublicIP,
			&n.Status, &n.CfgVersion, &n.DNSEnabled, &n.LastHBAt, &n.CreatedAt,
			&n.DrainedAt, &n.DNSReason, &n.DNSActor, &n.DNSChangedAt,
			&n.AgentVersion, &n.GeoDBSha, &n.VerifyKinds); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TouchHeartbeat 记下一次心跳。cfg_version 来自节点上报——它是**节点说的**
// 自己在跑哪一版，主控拿它与基线比对得出配置漂移（ADR-0002）。
//
// status 只在 ok / warn 之间取：判定离线是 health 模块的事，
// 而一次到达的心跳按定义就说明它没离线。
func (s *Store) TouchHeartbeat(ctx context.Context, nodeID, cfgVersion, status string) error {
	return s.touchHeartbeat(ctx, nodeID, cfgVersion, status, nil, nil)
}

// TouchHeartbeatWithGeo 同上，外加节点报的 GeoIP 库哈希。
//
// **分成两个函数而不是加一个参数**：加参数的话每个调用方都要想一下传什么，
// 而只有隧道那一处知道这个值。别处传空串会把一个已有库的节点标成没有。
func (s *Store) TouchHeartbeatWithGeo(ctx context.Context, nodeID, cfgVersion, status, geoSHA string, kinds []string) error {
	return s.touchHeartbeat(ctx, nodeID, cfgVersion, status, &geoSHA, kinds)
}

func (s *Store) touchHeartbeat(ctx context.Context, nodeID, cfgVersion, status string, geoSHA *string, kinds []string) error {
	if status != "warn" {
		status = "ok"
	}
	// **这条 SQL 不碰 drained_at，那是 ADR-0014 的核心论据**，
	// 由 scripts/probes.py 的「心跳不冲掉下线标记」盯着。
	// 往这里加一句「节点回来了就清掉下线标记」听起来很合理，而那会让
	// 下线在心跳到达的那一刻静默失效。
	if geoSHA != nil {
		if kinds == nil {
			kinds = []string{}
		}
		_, err := s.Pool.Exec(ctx,
			`UPDATE edge_nodes SET last_hb_at = now(), status = $3::node_status,
			        cfg_version = $2, geo_db_sha = $4, verify_kinds = $5
			 WHERE id = $1`, nodeID, cfgVersion, status, *geoSHA, kinds)
		return err
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET last_hb_at = now(), status = $3::node_status, cfg_version = $2
		 WHERE id = $1`, nodeID, cfgVersion, status)
	return err
}

// CountNodesByStatus 返回 ok / warn / down 三档的数量。
//
// **三档由同一条语句产出**，而不是让调用方各算各的：总览上「在线 N · 异常 M ·
// 离线 K」必须等于总数，两处分别推导迟早会算不平——而一处口径错会在界面上
// 冒出来两次，那比单个错数字更让人怀疑整个系统。
func (s *Store) CountNodesByStatus(ctx context.Context) (ok, warn, down, total int, err error) {
	err = s.Pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status = 'ok'),
		        count(*) FILTER (WHERE status = 'warn'),
		        count(*) FILTER (WHERE status = 'down'),
		        count(*)
		 FROM edge_nodes`).Scan(&ok, &warn, &down, &total)
	return
}

// SetAgentVersion 记下节点上跑的 Agent 版本。
//
// 每次接入都写：Agent 升级之后重连，那一刻的版本才是当前值。
// SetAgentVersion 记下接入时报的版本与**它认得哪些规则类型**。
//
// **两件事一起写，因为它们来自同一条 Hello。** 分成两次的话，
// 中间那一瞬间会有「版本是新的、能力还是空的」这种状态，
// 而下发那道门读的正是能力 —— 它会把一台刚升级完的节点判成旧的。
func (s *Store) SetAgentVersion(ctx context.Context, nodeID, version string, kinds []string) error {
	if kinds == nil {
		kinds = []string{}
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET agent_version = $2, verify_kinds = $3 WHERE id = $1`,
		nodeID, version, kinds)
	return err
}

func (s *Store) SetNodeCfgVersion(ctx context.Context, nodeID, cfgVersion string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET cfg_version = $2 WHERE id = $1`, nodeID, cfgVersion)
	return err
}

// SetNodeDown 标记节点离线并停掉它的解析。
//
// 两件事一条语句：分开写会出现「已判定离线但还在解析里」的中间态，
// 而流量恰恰在那个窗口里继续往一台死机器上打。
func (s *Store) SetNodeDown(ctx context.Context, nodeID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes
		    SET status = 'down',
		        dns_enabled = FALSE,
		        -- **只有解析当时是开着的，才算「系统把它摘了」。**
		        --
		        -- 无条件改写的话，一台**人手动关掉解析**的机器掉线之后，
		        -- reason 会被覆盖成 auto_offline —— 人的决定就此消失，
		        -- 而心跳一恢复系统又会把解析开回去（见 ReattachAfterRecovery）。
		        -- **一次掉线撤销了一个人为的决定，而没有任何地方记下这件事。**
		        --
		        -- 这是 ADR-0014 那条「意图与观察分开」的一个漏洞：
		        -- 观察（掉线）不该覆盖意图（人关的）。
		        dns_reason = CASE WHEN dns_enabled
		                          THEN 'auto_offline'::dns_change_reason
		                          ELSE dns_reason END,
		        dns_actor = CASE WHEN dns_enabled THEN NULL ELSE dns_actor END,
		        dns_changed_at = CASE WHEN dns_enabled THEN now() ELSE dns_changed_at END
		  WHERE id = $1`, nodeID)
	return err
}

// SetNodeDrained 记下或撤销一次下线。
//
// 下线同时关掉解析：这两件事在一条语句里，理由与 SetNodeDown 相同——
// 分开写会出现「已下线但还在解析里」的中间态，而流量恰恰在那个窗口里
// 继续往一台正在退出的机器上打。
//
// **重新上线不自动打开解析。** 一台机器能接入不等于它该马上分流量：
// 它刚回来，配置可能还是旧的。解析由人另外点，或者由下一次成功下发带起来。
func (s *Store) SetNodeDrained(ctx context.Context, nodeID string, drained bool, actor string) error {
	if drained {
		_, err := s.Pool.Exec(ctx,
			`UPDATE edge_nodes
			    SET drained_at = now(), dns_enabled = FALSE,
			        dns_reason = 'drained', dns_actor = nullif($2, ''), dns_changed_at = now()
			  WHERE id = $1`, nodeID, actor)
		return err
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET drained_at = NULL WHERE id = $1`, nodeID)
	return err
}

// IsNodeDrained 回答「这台机器是不是被人下线了」。
//
// 接入路径要用它挡住重连：Agent 断了就重连，不挡的话「关闭隧道」是个假动作。
// 节点不存在时返回 false —— 那是「没见过」，不是「下线了」，
// 两者的处置不同（前者该走正常的接入流程去创建）。
func (s *Store) IsNodeDrained(ctx context.Context, nodeID string) (bool, error) {
	var at *time.Time
	err := s.Pool.QueryRow(ctx,
		`SELECT drained_at FROM edge_nodes WHERE id = $1`, nodeID).Scan(&at)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return at != nil, nil
}

// 解析开关变更的原因。**每一次改都要带上一个**——
// 一个不带原因的开关，界面上就只剩「未参与解析」四个字。
const (
	DNSManual      = "manual"       // 人在控制台点的
	DNSAutoOffline = "auto_offline" // 心跳超时，系统自动摘的
	DNSDrained     = "drained"      // 人把这台节点下线了
)

// SetNodeDNS 改一个节点的解析标志位，并记下**是谁在什么时候改的**。
//
// actor 是操作人；系统自动摘除时传空 —— 空与「某个叫 system 的用户」
// 是两回事，而后者会在审计页上冒出一个不存在的账号。
func (s *Store) SetNodeDNS(ctx context.Context, nodeID string, enabled bool,
	reason, actor string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes
		    SET dns_enabled = $2,
		        dns_reason = $3::dns_change_reason,
		        dns_actor = nullif($4, ''),
		        dns_changed_at = now()
		  WHERE id = $1`, nodeID, enabled, reason, actor)
	return err
}

// UpdateNodeMeta 改一个节点的元数据。**只改人填的那几项。**
//
// `id` 不在里面：它是这台机器的身份，写在隧道证书的 CN 里（ADR-0009）。
// 改它等于换一台机器，而那是「删掉再接一台」，不是「编辑」。
//
// status / dns_enabled / drained_at 也不在里面：那些是观察和意图，
// 各有自己的写入路径（ADR-0014）。**一个能改 status 的编辑接口，
// 会让人以为可以手工把一台死机器改成在线。**
//
// 返回 ErrNotFound —— 不存在与「改了但没变化」要分得开：
// 前者是路径错了，后者是一次正常的空操作。
func (s *Store) UpdateNodeMeta(ctx context.Context, spec NodeSpec) error {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET city=$2, vendor=$3, line=$4, public_ip=$5 WHERE id=$1`,
		spec.NodeID, spec.City, spec.Vendor, spec.Line, spec.PublicIP)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNode 删掉一个节点的记录。
//
// **只删记录，不碰那台机器。** 机器上的 Agent 与 Caddy 还在跑，
// 要真正撤掉得在那台机器上跑 `edge-node.sh uninstall`。
// 这个区分要在界面上说清——否则人会以为点了删除机器就干净了。
//
// # 跟着删的与留下的
//
// `dns_weights` / `cert_nodes` / `node_logs` 由外键级联删除：它们是
// **关于这个节点此刻的安排**，节点没了就没有意义。
//
// 而 `deploy_results` / `events` / `audit_logs` **没有外键，因此留着**。
// 那是历史：「那次下发推到了哪几台」「谁在什么时候下线了它」——
// 删掉一台机器不该让过去发生过的事从记录里消失。
//
// 这不是疏忽，是这张表设计时就分开的两类：**当前安排会跟着走，
// 已经发生的事不会。**
func (s *Store) DeleteNode(ctx context.Context, nodeID string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM edge_nodes WHERE id = $1`, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetNode 读一个节点。不存在返回 ErrNotFound —— 与「读到了一行空值」分开：
// 后者会让调用方以为节点在、只是没填。
func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	var n Node
	err := s.Pool.QueryRow(ctx,
		`SELECT id, city, vendor, line, host(public_ip), status::text,
		        cfg_version, dns_enabled, last_hb_at, created_at, drained_at,
		        coalesce(dns_reason::text, ''), coalesce(dns_actor, ''), dns_changed_at,
		        agent_version, geo_db_sha, verify_kinds
		 FROM edge_nodes WHERE id = $1`, id).
		Scan(&n.ID, &n.City, &n.Vendor, &n.Line, &n.PublicIP,
			&n.Status, &n.CfgVersion, &n.DNSEnabled, &n.LastHBAt, &n.CreatedAt,
			&n.DrainedAt, &n.DNSReason, &n.DNSActor, &n.DNSChangedAt, &n.AgentVersion, &n.GeoDBSha, &n.VerifyKinds)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// ReattachAfterRecovery 在心跳恢复时把**系统自己摘掉的**解析放回去。
// 返回是否真的放回了。
//
// **摘和恢复此前不对称，而那是个真 bug。** SetNodeDown 在 SQL 里直接把
// dns_enabled 置 false，而恢复那一侧只调 dnsops.Attach —— 那个函数
// 「只负责让服务商侧跟上」，不写库。于是标志位一旦被自动摘掉就**再也回不来**。
//
// 后果比「一台机器掉出解析」大得多：**主控每重启一次，所有节点都会被
// 自动摘掉**（重启窗口里它们必然错过几个心跳），而重启是例行操作。
// 灰度上就是这么发生的——部署完新版本，节点几秒后就重连回来了，
// 而它已经不在解析里，且没有任何东西会把它放回去。
//
// # 只放回系统自己摘的那一种
//
//	auto_offline  系统摘的 → 系统放回
//	manual        人摘的   → **不碰**，否则一次心跳抖动就撤销了人的决定
//	drained       人下线的 → 不碰，它本来就不该在解析里（ADR-0014）
//
// **「系统摘的系统放回，人摘的人放回」** 是这条 SQL 里 WHERE 子句的全部内容，
// 而它同时也是 ADR-0014 那条「意图与观察分开」在这一处的具体形态：
// auto_offline 是观察驱动的，观察变了就该跟着变；manual 是意图，不随观察动。
//
// 恢复之后 reason / actor 一起清空 —— 回到「没有人为干预」的状态。
// 留着 auto_offline 而 dns_enabled 是 true，是一句自相矛盾的记录，
// 而界面会照着 reason 引导人「先去修那台机器」，那时已经没什么要修的了。
func (s *Store) ReattachAfterRecovery(ctx context.Context, nodeID string) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes
		    SET dns_enabled = TRUE, dns_reason = NULL, dns_actor = NULL,
		        dns_changed_at = now()
		  WHERE id = $1 AND dns_reason = 'auto_offline' AND drained_at IS NULL`,
		nodeID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
