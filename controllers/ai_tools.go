package controllers

import (
	"log"
	"net/http"

	"mindex-backend/internal/tools"

	"github.com/gin-gonic/gin"
)

// ConfirmToolCall resolves a side-effect tool call that the dispatcher held
// pending (FR-009, SC-008: side-effect tools never auto-execute). The
// frontend calls this after the user explicitly approves/declines the
// "pending_confirmation" surfaced in a chat "done" event payload.
func ConfirmToolCall(c *gin.Context) {
	userID := c.GetString("user_id")

	var req struct {
		PendingID string `json:"pending_id" binding:"required"`
		Confirm   bool   `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Thiếu pending_id"})
		return
	}

	if !req.Confirm {
		tools.CancelPendingConfirmation(c.Request.Context(), req.PendingID)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Đã hủy hành động"})
		return
	}

	rec, err := tools.NewDispatcher(tools.Default).ResolvePendingConfirmation(c.Request.Context(), req.PendingID, userID)
	if err != nil {
		log.Printf("[ConfirmToolCall] resolve failed for pending_id=%s: %v", req.PendingID, err)
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":   rec.Status == "success",
		"status":    rec.Status,
		"tool_name": rec.ToolName,
		"error":     rec.ErrorMessage,
	})
}
