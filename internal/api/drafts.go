package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// listDrafts 返回全部草稿。
//
// 不按操作人过滤：草稿是全局可见的（CONTEXT.md「草稿」），你要能看到 ops-bot
// 半夜改了什么。作者信息随每条带回，供确认弹层逐条标注。
func (h *handler) listDrafts(c *gin.Context) {
	ds, err := h.deps.Store.ListDrafts(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取草稿失败")
		return
	}
	out := make([]gin.H, 0, len(ds))
	for _, d := range ds {
		out = append(out, gin.H{
			"res_key": d.ResKey, "patch": d.Patch,
			"updated_by": d.UpdatedBy, "updated_at": d.UpdatedAt,
		})
	}
	ok(c, gin.H{"drafts": out})
}

type draftInput struct {
	Patch map[string]any `json:"patch"`
}

// putDraft 写入一条草稿。patch 为空表示该资源已无待下发改动，删除该行。
//
// 「改回原值就删掉草稿键」的判断在前端做（它才知道线上值是什么），
// 后端只负责「空 patch 等于没有草稿」这一条语义。
func (h *handler) putDraft(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		fail(c, http.StatusBadRequest, codeBadInput, "缺少资源键")
		return
	}
	var in draftInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	if err := h.deps.Store.PutDraft(c.Request.Context(), key, in.Patch, operatorOf(c), time.Now()); err != nil {
		h.failErr(c, err, "写入草稿失败")
		return
	}
	ok(c, nil)
}

func (h *handler) deleteDrafts(c *gin.Context) {
	ds, err := h.deps.Store.ListDrafts(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取草稿失败")
		return
	}
	keys := make([]string, 0, len(ds))
	for _, d := range ds {
		keys = append(keys, d.ResKey)
	}
	if err := h.deps.Store.DeleteDrafts(c.Request.Context(), keys); err != nil {
		h.failErr(c, err, "清除草稿失败")
		return
	}
	h.log.Info("放弃全部草稿", "operator", operatorOf(c), "count", len(keys))
	ok(c, gin.H{"cleared": len(keys)})
}
