package model

import "regexp"

// resourceID 是节点 ID 与规则 ID 共用的形状，见 docs/api-contract.md §0.7。
//
// 小写字母、数字、连字符，以字母或数字开头，2–40 个字符。
//
// **这是接口的一部分，不是界面偏好。** 这两种 id 都会被拼进 URL 路径、
// 进 DNS 记录的比对键、进证书的 CN（ADR-0009：node_id 就写在隧道证书的
// CN 里）——带 `/` 或 `#` 的 id 在其中任何一处上都会安静地走偏，
// 而症状出现在离输入点很远的地方。
//
// 大小写也算形状的一部分：证书 CN 与 DNS 比对键都区分大小写，
// 而人不会觉得 `NODE-HK-01` 和 `node-hk-01` 是两台机器。
var resourceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

// ValidResourceID 说这个 id 合不合 §0.7 的格式。
//
// 控制台也挡这一条（web/src/ids.ts），而**那不够**：契约 §0.6 明说直改路
// （ops-bot、批量脚本）是给脚本的入口，它绕过控制台。这个仓库数过好几次
// 同一个形状——前端拦住不等于后端安全。
func ValidResourceID(id string) bool { return resourceID.MatchString(id) }

// ResourceIDHint 是给人看的那句话。**与前端那份是同一句**：
// 同一条规矩有两种说法的话，人会以为自己撞上的是两道不同的闸。
const ResourceIDHint = "只能用小写字母、数字和连字符，以字母或数字开头，2–40 个字符"
