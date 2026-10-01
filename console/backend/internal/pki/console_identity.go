package pki

// Console client identities that an agent treats as privileged.
//
// The console mints a short-lived client certificate per conversation, with
// RoleConsole and a common name that says which conversation it is. For most
// of them the name is only for the agent's logs. For these three it is also
// authorization: the agent runs its disk-key and first-identity services, and
// opens its desktop port, only for a caller presenting exactly this name with
// the console role (transport/grpc authorizePrivilegedServiceCaller and
// authorizeStreamCaller), so a probe, renewal or relay certificate — each also
// CA-signed — cannot format a disk, answer a bootstrap or read the screen.
//
// They live here, in the package both sides import, for the reason
// AgentCommonName does: the console mints them and the agent checks them, and
// two definitions would drift into an unlock or bootstrap that the agent
// refuses on every qube.
const (
	// ConsoleBootstrapCN is the identity the console presents while issuing a
	// qube its first certificate (qubesair.BeginBootstrap/CompleteBootstrap).
	ConsoleBootstrapCN = "console-bootstrap"
	// ConsoleUnlockCN is the identity the console presents while pushing a
	// data-disk key (qubesair.UnlockData/RekeyData).
	ConsoleUnlockCN = "console-unlock"
	// ConsoleDesktopCN is the identity the console presents while reading one
	// desktop frame from a qube's Xpra port for an approved MCP request. It is
	// minted per capture and lives only as long as that capture.
	ConsoleDesktopCN = "console-desktop"
)
