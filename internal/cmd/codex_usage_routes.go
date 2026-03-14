package cmd

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
)

func withCodexUsageRoutes(builder *cliproxy.Builder) *cliproxy.Builder {
	if builder == nil {
		return nil
	}
	return builder.WithServerOptions(api.WithServerConfigurator(registerCodexUsageRoutes))
}

func registerCodexUsageRoutes(s *api.Server) {
	if s == nil {
		return
	}
	engine := s.Engine()
	mgmt := s.ManagementHandler()
	if engine == nil || mgmt == nil {
		return
	}

	if base := s.APIHandlers(); base != nil {
		base.SetSelectedAuthIDCallback(mgmt.SelectedAuthIDCallback())
		base.SetObservedServiceTierCallback(mgmt.ObservedServiceTierCallback())
	}
	apihandlers.SetDefaultObservedServiceTierCallback(mgmt.ObservedServiceTierCallback())

	authMiddleware := s.RequestAuthMiddleware()
	engine.GET("/api/codex/usage", authMiddleware, mgmt.GetCodexUsageCompat)
	engine.GET("/wham/usage", authMiddleware, mgmt.GetCodexUsageCompat)
	engine.GET("/backend-api/wham/usage", authMiddleware, mgmt.GetCodexUsageCompat)

	v1 := engine.Group("/v1")
	v1.Use(authMiddleware)
	v1.GET("/backend-api/wham/usage", mgmt.GetCodexUsageCompat)

	managementGroup := engine.Group("/v0/management")
	managementGroup.Use(s.ManagementAvailabilityMiddleware(), mgmt.Middleware())
	managementGroup.GET("/codex-usage-summary", mgmt.GetCodexUsageSummary)
}
