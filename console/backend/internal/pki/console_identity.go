package pki

// Console client identities that an agent treats as privileged.
//
// The console mints a short-lived client certificate per conversation, with
// RoleConsole and a common name that says which conversation it is. For most
// of them the name is only for the agent's logs. For these two it is also
// authorization: the agent runs its disk-key and first-identity services only
// for a caller presenting exactly this name with the console role
// (transport/grpc authorizePrivilegedServiceCaller), so a probe, renewal or
// relay certificate — each also CA-signed — cannot format a disk or answer a
// bootstrap.
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
)
