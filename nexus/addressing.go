package main

const (
	subnetClientsA = 127
	subnetClientsB = 1
	subnetClientsC = 1

	defaultServerPort = 26000

	// Reserved for future "admin" control plane.
	subnetAdminsA = 127
	subnetAdminsB = 13
	subnetAdminsC = 37

	// Dedicated servers (one per mod dir).
	subnetServersA = 127
	subnetServersB = 255
	subnetServersC = 255

	// Nexus/orchestration entities (pollers, future agents).
	subnetNexusA       = 127
	subnetNexusB       = 127
	subnetNexusC       = 127
	nexusPollerHostOct = 127

	firstClientHostOct = 1
	lastClientHostOct  = 254
)
