package dashboard

import (
	"github.com/xraph/forge/extensions/dashboard/contributor"
)

// NewManifest builds a contributor.Manifest for the relay dashboard.
func NewManifest() *contributor.Manifest {
	return &contributor.Manifest{
		Name:        "relay",
		DisplayName: "Relay",
		Icon:        "webhook",
		Version:     "1.0.0",
		Layout:      "extension",
		ShowSidebar: boolPtr(true),
		TopbarConfig: &contributor.TopbarConfig{
			Title:       "Relay",
			LogoIcon:    "webhook",
			AccentColor: "#3b82f6",
			ShowSearch:  true,
			Actions: []contributor.TopbarAction{
				{Label: "API Docs", Icon: "file-text", Href: "/docs", Variant: "ghost"},
			},
		},
		Nav:      baseNav(),
		Widgets:  baseWidgets(),
		Settings: baseSettings(),
		Capabilities: []string{
			"searchable",
		},
	}
}

// baseNav returns the core navigation items for the relay dashboard.
func baseNav() []contributor.NavItem {
	return []contributor.NavItem{
		// Overview
		{Label: "Overview", Path: "/", Icon: "layout-dashboard", Group: "Overview", Priority: 0},

		// Catalog
		{Label: "Event Types", Path: "/event-types", Icon: "list-tree", Group: "Catalog", Priority: 0},

		// Webhooks
		{Label: "Endpoints", Path: "/endpoints", Icon: "webhook", Group: "Webhooks", Priority: 0},
		{Label: "Events", Path: "/events", Icon: "zap", Group: "Webhooks", Priority: 1},

		// Delivery
		{Label: "Deliveries", Path: "/deliveries", Icon: "send", Group: "Delivery", Priority: 0},
		{Label: "Dead Letter Queue", Path: "/dlq", Icon: "alert-triangle", Group: "Delivery", Priority: 1},

		// Configuration
		{Label: "Settings", Path: "/settings", Icon: "settings", Group: "Configuration", Priority: 0},
	}
}

// baseWidgets returns the core widget descriptors for the relay dashboard.
func baseWidgets() []contributor.WidgetDescriptor {
	return []contributor.WidgetDescriptor{
		{
			ID:          "relay-stats",
			Title:       "Webhook Stats",
			Description: "Delivery pipeline metrics",
			Size:        "md",
			RefreshSec:  30,
			Group:       "Relay",
		},
		{
			ID:          "relay-recent-deliveries",
			Title:       "Recent Deliveries",
			Description: "Latest webhook delivery attempts",
			Size:        "lg",
			RefreshSec:  15,
			Group:       "Relay",
		},
	}
}

// baseSettings returns the core settings descriptors for the relay dashboard.
func baseSettings() []contributor.SettingsDescriptor {
	return []contributor.SettingsDescriptor{
		{
			ID:          "relay-config",
			Title:       "Relay Configuration",
			Description: "Webhook engine settings",
			Group:       "Relay",
			Icon:        "webhook",
		},
	}
}

func boolPtr(b bool) *bool { return &b }
