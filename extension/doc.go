// Package extension provides the Forge extension for mounting Relay.
//
// The extension implements [forge.Extension] and integrates Relay into the
// Forge application framework by:
//   - Initializing the Relay engine with a configured store
//   - Running database migrations on registration
//   - Mounting admin API routes with OpenAPI metadata under a configurable prefix
//   - Starting the delivery engine on application start
//   - Gracefully stopping the engine on application shutdown
//   - Providing health checks via store.Ping
//   - Registering the Relay instance in Forge's DI container
//
// Usage:
//
//	app := forge.New(
//	    forge.WithExtensions(
//	        extension.New(
//	            extension.WithStore(postgresStore),
//	            extension.WithPrefix("/webhooks"),
//	        ),
//	    ),
//	)
//	app.Run()
package extension
