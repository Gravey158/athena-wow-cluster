package binpack

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/walkline/ToCloud9/apps/servers-registry/repo"
)

func Test_knapsackBalancer_Distribute(t *testing.T) {
	tests := map[string]struct {
		weights MapsWeight
		servers []repo.GameServer
		want    []repo.GameServer
	}{
		"simple 2 servers 3 maps": {
			weights: map[uint32]uint32{1: 3, 0: 1, 3: 2},
			servers: []repo.GameServer{{}, {}},
			want:    []repo.GameServer{{AssignedMapsToHandle: []uint32{1}}, {AssignedMapsToHandle: []uint32{0, 3}}},
		},
		"simple 2 servers 3 maps and 1 server with exact map": {
			weights: map[uint32]uint32{1: 3, 0: 1, 3: 2},
			servers: []repo.GameServer{{}, {}, {AvailableMaps: []uint32{1}}},
			want: []repo.GameServer{
				{AvailableMaps: []uint32{1}, AssignedMapsToHandle: []uint32{1}},
				{AssignedMapsToHandle: []uint32{3}},
				{AssignedMapsToHandle: []uint32{0}},
			},
		},
		"simple 3 servers 3 maps": {
			weights: map[uint32]uint32{1: 3, 0: 1, 3: 2},
			servers: []repo.GameServer{{}, {}, {}},
			want:    []repo.GameServer{{AssignedMapsToHandle: []uint32{1}}, {AssignedMapsToHandle: []uint32{3}}, {AssignedMapsToHandle: []uint32{0}}},
		},
		"simple 1 server 3 maps": {
			weights: map[uint32]uint32{1: 3, 3: 2, 0: 1},
			servers: []repo.GameServer{{}},
			want:    []repo.GameServer{{AssignedMapsToHandle: []uint32{0, 1, 3}}},
		},
		// Hard to calculate expected output =P
		//"real example 2 servers": {
		//	weights: DefaultMapsWeight,
		//	servers: []repo.GameServer{{}, {}},
		//	want:    []repo.GameServer{},
		//},
		// Hard to calculate expected output =P
		//"real example 3 servers": {
		//	weights: DefaultMapsWeight,
		//	servers: []repo.GameServer{{}, {}, {}},
		//	want:    []repo.GameServer{},
		//},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			k := &binpackBalancer{
				weights: tt.weights,
			}
			r := k.Distribute(tt.servers)
			assert.ElementsMatch(t, tt.want, r)
		})
	}
}

// Regression test for the surplus-bins bug: when bin-packing produces more
// bins than servers (high-weight maps), the prior implementation silently
// dropped the maps in `packing[len(servers):]`. After fix, every map must
// land somewhere across the servers.
func Test_binpackBalancer_SurplusNoMapsDropped(t *testing.T) {
	weights := MapsWeight{1: 1000, 2: 1000, 3: 1000, 4: 1000, 5: 1000}
	servers := []repo.GameServer{{}, {}}
	k := &binpackBalancer{weights: weights}

	r := k.Distribute(servers)

	seen := map[uint32]bool{}
	for _, s := range r {
		for _, mapID := range s.AssignedMapsToHandle {
			seen[mapID] = true
		}
	}
	for mapID := range weights {
		assert.Truef(t, seen[mapID], "map %d was dropped by Distribute", mapID)
	}
	assert.Lenf(t, r, len(servers), "expected %d servers in result, got %d", len(servers), len(r))
}
