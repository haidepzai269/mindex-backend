package controllers

import (
	"encoding/json"
	"mindex-backend/internal/tools"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func GetMoreNews(c *gin.Context) {
	category := strings.TrimSpace(c.DefaultQuery("category", "top"))
	query := strings.TrimSpace(c.Query("query"))
	pageToken := strings.TrimSpace(c.Query("page_token"))
	language := strings.TrimSpace(c.DefaultQuery("language", "vi"))
	country := strings.TrimSpace(c.DefaultQuery("country", "vn"))

	if len(category) > 50 || len(query) > 200 || len(pageToken) > 500 || len(language) > 5 || len(country) > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "INVALID_PARAMS"})
		return
	}

	if !tools.ValidNewsCategory(category) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "INVALID_CATEGORY"})
		return
	}

	data, err := tools.FetchNewsPage(c.Request.Context(), category, query, pageToken, language, country)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "UPSTREAM_ERROR"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": json.RawMessage(data)})
}
