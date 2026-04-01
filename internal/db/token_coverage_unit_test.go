package db

import "testing"

func TestComputeSessionCoverageUpdates(t *testing.T) {
	tests := []struct {
		name        string
		candidates  []SessionCoverageCandidate
		msgCoverage map[string][2]bool
		want        []SessionCoverageUpdate
	}{
		{
			name: "basic: three candidates with mixed updates",
			candidates: []SessionCoverageCandidate{
				// gets both flags from message coverage
				{ID: "a", TotalOutputTokens: 0, PeakContextTokens: 0,
					HasTotal: false, HasPeak: false},
				// gets hasTotal from non-zero total tokens
				{ID: "b", TotalOutputTokens: 100, PeakContextTokens: 0,
					HasTotal: false, HasPeak: false},
				// already has both flags — no update needed
				{ID: "c", TotalOutputTokens: 50, PeakContextTokens: 200,
					HasTotal: true, HasPeak: true},
			},
			msgCoverage: map[string][2]bool{
				"a": {true, true}, // hasContext=true, hasOutput=true
			},
			want: []SessionCoverageUpdate{
				{ID: "a", HasTotal: true, HasPeak: true},
				{ID: "b", HasTotal: true, HasPeak: false},
			},
		},
		{
			name:        "empty: nil candidates and nil coverage returns empty",
			candidates:  nil,
			msgCoverage: nil,
			want:        []SessionCoverageUpdate{},
		},
		{
			name: "no updates needed: all candidates already correct",
			candidates: []SessionCoverageCandidate{
				{ID: "x", TotalOutputTokens: 10, PeakContextTokens: 20,
					HasTotal: true, HasPeak: true},
				{ID: "y", TotalOutputTokens: 0, PeakContextTokens: 0,
					HasTotal: false, HasPeak: false},
			},
			msgCoverage: map[string][2]bool{},
			want:        []SessionCoverageUpdate{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSessionCoverageUpdates(tc.candidates, tc.msgCoverage)

			if len(got) != len(tc.want) {
				t.Fatalf("len(got) = %d, want %d; got = %v",
					len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].ID != w.ID {
					t.Errorf("[%d] ID = %q, want %q", i, got[i].ID, w.ID)
				}
				if got[i].HasTotal != w.HasTotal {
					t.Errorf("[%d] HasTotal = %v, want %v",
						i, got[i].HasTotal, w.HasTotal)
				}
				if got[i].HasPeak != w.HasPeak {
					t.Errorf("[%d] HasPeak = %v, want %v",
						i, got[i].HasPeak, w.HasPeak)
				}
			}
		})
	}
}
