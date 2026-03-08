package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

// Engine exposes the underlying Gin engine for fork-specific route extensions.
func (s *Server) Engine() *gin.Engine {
	if s == nil {
		return nil
	}
	return s.engine
}

// RequestAuthMiddleware exposes the standard request auth middleware for custom routes.
func (s *Server) RequestAuthMiddleware() gin.HandlerFunc {
	if s == nil {
		return func(c *gin.Context) { c.AbortWithStatus(http.StatusInternalServerError) }
	}
	return AuthMiddleware(s.accessManager)
}

// ManagementHandler exposes the management handler for fork-specific routes.
func (s *Server) ManagementHandler() *managementHandlers.Handler {
	if s == nil {
		return nil
	}
	return s.mgmt
}

// ManagementAvailabilityMiddleware exposes the management availability gate for custom routes.
func (s *Server) ManagementAvailabilityMiddleware() gin.HandlerFunc {
	if s == nil {
		return func(c *gin.Context) { c.AbortWithStatus(http.StatusNotFound) }
	}
	return s.managementAvailabilityMiddleware()
}

// APIHandlers exposes the shared API handler base for fork-specific wiring.
func (s *Server) APIHandlers() *apiHandlers.BaseAPIHandler {
	if s == nil {
		return nil
	}
	return s.handlers
}
