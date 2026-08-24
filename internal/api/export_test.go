package api

// NodeRespForTest 给外部测试包一个空的节点项，用来比字段集。
//
// **导出一个零值而不是把那张 want 表挪进来**：表放在 routes_test.go 里
// 与别的契约表在一起，读的人在同一处看到「这些端点的字段集都有人钉着」。
func NodeRespForTest() any { return nodeResp{} }

// FieldErrorForTest 给外部测试包一个空的字段错误，用来比键名。
func FieldErrorForTest() any { return FieldError{} }
