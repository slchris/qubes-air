package proxmoxnetwork

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckNetworkBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, iface, extra string
		tag                int
		want               string
	}{
		{name: "untagged", iface: `"type":"bridge","active":1`},
		{name: "tagged", tag: 42, iface: `"type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"2 40-42 44"`},
		{name: "boolean fields", tag: 42, iface: `"type":"bridge","active":true,"bridge_vlan_aware":true,"bridge_vids":"42"`},
		{name: "OVS tags", tag: 42, iface: `"type":"OVSBridge","active":1`},
		{name: "wrong interface type", iface: `"type":"eth","active":1`, want: "not a supported"},
		{name: "inactive", iface: `"type":"bridge","active":0`, want: "not active"},
		{name: "missing active", iface: `"type":"bridge"`, want: "not active"},
		{name: "invalid active", iface: `"type":"bridge","active":"yes"`, want: "not active"},
		{name: "not VLAN aware", tag: 42, iface: `"type":"bridge","active":1`, want: "VLAN aware"},
		{name: "tag outside range", tag: 43, iface: `"type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"40-42"`, want: "not in bridge_vids"},
		{name: "unspecified VLANs", tag: 42, iface: `"type":"bridge","active":1,"bridge_vlan_aware":1`, want: "no explicit bridge_vids"},
		{name: "invalid tail after match", tag: 42, iface: `"type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"42 bad"`, want: "invalid bridge_vids"},
		{name: "pending changes", iface: `"type":"bridge","active":1`, extra: `,"changes":"pending diff"`, want: "unapplied"},
		{name: "null changes", iface: `"type":"bridge","active":1`, extra: `,"changes":null`},
		{name: "empty changes", iface: `"type":"bridge","active":1`, extra: `,"changes":""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"data":[{"iface":"vmbr7",%s}]%s}`, tc.iface, tc.extra)
			err := (Requirement{Bridge: "vmbr7", VLANTag: tc.tag}).Check([]byte(raw))
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestNetworkSnapshotRefusals(t *testing.T) {
	for _, raw := range []string{
		``, `{`, `null`, `{}`, `{"data":null}`, `{"data":[]}`, `{"data":{}}`,
		`{"data":[{"iface":"other","type":"bridge","active":1}]}`,
		`{"data":[{"iface":"vmbr7","type":"bridge","active":1},{"iface":"vmbr7","type":"bridge","active":1}]}`,
	} {
		require.Error(t, (Requirement{Bridge: "vmbr7"}).Check([]byte(raw)), "%s", raw)
	}
}

func TestVLANRangeBoundaries(t *testing.T) {
	for _, spec := range []string{"0", "4095", "42-2", "-1", "+42", "42--43", "1-2-3", "99999", "42,43", "42 0", ""} {
		_, err := permitsVLAN(spec, 42)
		require.Error(t, err, "%q", spec)
	}
	for _, tag := range []int{1, 2, 42, 4094} {
		allowed, err := permitsVLAN("1-4094", tag)
		require.NoError(t, err)
		require.True(t, allowed)
	}
}

func TestNetworkInputAllowlist(t *testing.T) {
	for _, name := range []string{"", "../node", "node/other", "node?x", "node%2fother", "node\n", "-node", strings.Repeat("n", 64)} {
		_, err := Path(name)
		require.Error(t, err, "%q", name)
	}
	path, err := Path("node-a.example")
	require.NoError(t, err)
	require.Equal(t, "/api2/json/nodes/node-a.example/network", path)
	for _, bridge := range []string{"", "../bridge", "br/0", "vmbr0,tag=4", "br\n", "-br", strings.Repeat("b", 16)} {
		require.Error(t, (Requirement{Bridge: bridge}).Validate(), "%q", bridge)
	}
	for _, tag := range []int{-1, 4095} {
		require.Error(t, (Requirement{Bridge: "vmbr7", VLANTag: tag}).Validate())
	}
}
