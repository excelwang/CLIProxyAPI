package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/codexmodels"
)

func (s *Server) codexModels(c *gin.Context) {
	c.JSON(http.StatusOK, codexmodels.AvailableCodexModels())
}
