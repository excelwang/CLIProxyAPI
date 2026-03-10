package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/codexplugin"
)

func (s *Server) downloadCodexPluginAuthJSON(c *gin.Context) {
	apiKey := strings.TrimSpace(c.GetString("apiKey"))
	if apiKey == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing api key context"})
		return
	}

	email := strings.TrimSpace(c.Query("email"))
	planType := strings.TrimSpace(c.Query("plan_type"))
	ttl := codexplugin.DefaultTTL
	if rawDays := strings.TrimSpace(c.Query("ttl_days")); rawDays != "" {
		days, err := strconv.Atoi(rawDays)
		if err != nil || days <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid ttl_days"})
			return
		}
		if days > 3650 {
			days = 3650
		}
		ttl = time.Duration(days) * 24 * time.Hour
	}

	doc, err := codexplugin.BuildAuthJSON(apiKey, codexplugin.IssueOptions{
		Email:    email,
		PlanType: planType,
		TTL:      ttl,
		Now:      time.Now(),
	})
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="auth.json"`)
	c.String(http.StatusOK, string(doc))
}
