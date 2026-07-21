package kubernetes

import (
	"net"
	"testing"

	"github.com/k8snetworkplumbingwg/whereabouts/pkg/allocate"
	whereaboutstypes "github.com/k8snetworkplumbingwg/whereabouts/pkg/types"
)

func TestIPPoolName(t *testing.T) {
	cases := []struct {
		name           string
		poolIdentifier PoolIdentifier
		expectedResult string
	}{
		{
			name: "No node name, unnamed network",
			poolIdentifier: PoolIdentifier{
				NetworkName: UnnamedNetwork,
				IpRange:     "10.0.0.0/8",
			},
			expectedResult: "10.0.0.0-8",
		},
		{
			name: "No node name, named network",
			poolIdentifier: PoolIdentifier{
				NetworkName: "test",
				IpRange:     "10.0.0.0/8",
			},
			expectedResult: "test-10.0.0.0-8",
		},
		{
			name: "Node name, unnamed network",
			poolIdentifier: PoolIdentifier{
				NetworkName: UnnamedNetwork,
				NodeName:    "testnode",
				IpRange:     "10.0.0.0/8",
			},
			expectedResult: "testnode-10.0.0.0-8",
		},
		{
			name: "Node name, named network",
			poolIdentifier: PoolIdentifier{
				NetworkName: "testnetwork",
				NodeName:    "testnode",
				IpRange:     "10.0.0.0/8",
			},
			expectedResult: "testnetwork-testnode-10.0.0.0-8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := IPPoolName(tc.poolIdentifier)
			if result != tc.expectedResult {
				t.Errorf("Expected result: %s, got result: %s", tc.expectedResult, result)
			}
		})
	}
}

// TestNarrowRangeToNodeSlice covers the node-slice rewrite in isolation: the
// allocatable window must clamp to the slice, while Range (the parent pool) and
// AssignPrefix must survive so the stamped prefix stays tied to the pool/override
// rather than the slice.
func TestNarrowRangeToNodeSlice(t *testing.T) {
	cases := []struct {
		name           string
		ipRange        whereaboutstypes.RangeConfiguration
		nodeSliceRange string
		wantRange      string
		wantStart      string
		wantEnd        string
		wantPrefix     int
	}{
		{
			name:           "carries assign_prefix and pool range across a mid-pool slice",
			ipRange:        whereaboutstypes.RangeConfiguration{Range: "10.68.0.0/22", AssignPrefix: 32},
			nodeSliceRange: "10.68.0.64/27",
			wantRange:      "10.68.0.0/22",
			wantStart:      "10.68.0.65",
			wantEnd:        "10.68.0.94",
			wantPrefix:     32,
		},
		{
			name:           "no override leaves assign_prefix zero (pool prefix wins downstream)",
			ipRange:        whereaboutstypes.RangeConfiguration{Range: "10.68.0.0/22"},
			nodeSliceRange: "10.68.0.0/27",
			wantRange:      "10.68.0.0/22",
			wantStart:      "10.68.0.1",
			wantEnd:        "10.68.0.30",
			wantPrefix:     0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := narrowRangeToNodeSlice(tc.ipRange, tc.nodeSliceRange)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Range != tc.wantRange {
				t.Errorf("Range: want %s, got %s", tc.wantRange, got.Range)
			}
			if got.RangeStart.String() != tc.wantStart {
				t.Errorf("RangeStart: want %s, got %s", tc.wantStart, got.RangeStart)
			}
			if got.RangeEnd.String() != tc.wantEnd {
				t.Errorf("RangeEnd: want %s, got %s", tc.wantEnd, got.RangeEnd)
			}
			if got.AssignPrefix != tc.wantPrefix {
				t.Errorf("AssignPrefix: want %d, got %d", tc.wantPrefix, got.AssignPrefix)
			}
		})
	}

	if _, err := narrowRangeToNodeSlice(whereaboutstypes.RangeConfiguration{Range: "10.68.0.0/22"}, "not-a-cidr"); err == nil {
		t.Errorf("expected an error for an invalid node slice CIDR, got nil")
	}
}

// TestNodeSliceAssignPrefixInteraction exercises the full contract: the value
// narrowRangeToNodeSlice produces, fed to allocate.AssignIP (exactly as
// IPManagementKubernetesUpdate does), must allocate *inside the node slice* yet
// stamp the *assign_prefix* mask — not the slice prefix, not the pool prefix.
func TestNodeSliceAssignPrefixInteraction(t *testing.T) {
	const pool = "10.68.0.0/22"
	const slice = "10.68.0.64/27"

	_, sliceNet, err := net.ParseCIDR(slice)
	if err != nil {
		t.Fatalf("parsing slice: %v", err)
	}

	narrowed, err := narrowRangeToNodeSlice(
		whereaboutstypes.RangeConfiguration{Range: pool, AssignPrefix: 32}, slice)
	if err != nil {
		t.Fatalf("narrowRangeToNodeSlice: %v", err)
	}

	out, _, err := allocate.AssignIP(narrowed, nil, "0xcafe", "default/pod1", "")
	if err != nil {
		t.Fatalf("AssignIP: %v", err)
	}

	if !sliceNet.Contains(out.IP) {
		t.Errorf("allocated IP %s is outside the node slice %s", out.IP, slice)
	}
	if ones, bits := out.Mask.Size(); ones != 32 || bits != 32 {
		t.Errorf("stamped mask: want /32, got /%d (bits %d) — slice or pool prefix leaked", ones, bits)
	}

	// Sanity: without the override the same slice yields the pool prefix (/22).
	narrowedNoOverride, err := narrowRangeToNodeSlice(
		whereaboutstypes.RangeConfiguration{Range: pool}, slice)
	if err != nil {
		t.Fatalf("narrowRangeToNodeSlice (no override): %v", err)
	}
	out2, _, err := allocate.AssignIP(narrowedNoOverride, nil, "0xcafe", "default/pod2", "")
	if err != nil {
		t.Fatalf("AssignIP (no override): %v", err)
	}
	if ones, _ := out2.Mask.Size(); ones != 22 {
		t.Errorf("without assign_prefix, stamped mask: want /22 (pool), got /%d", ones)
	}
}
