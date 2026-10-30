package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"wgpanel-api/internal/obfuscation"
)

// TestObfuscationStore covers the SQL behind obfuscation profiles: revision rules,
// concurrent rotation, assignment and the "applied" gate that decides whether client
// configs may carry AWG lines. Skipped without WGPANEL_TEST_POSTGRES_DSN.
func TestObfuscationStore(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run store integration tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	pub := "pk-" + sfx
	node, err := s.CreateNode(ctx, "obfstore-"+sfx, "obfstore-"+sfx, "", "h:51820", "10.99.0.0/24", 10, &pub)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID)
		_, _ = s.pool.Exec(bg, `DELETE FROM obfuscation_profiles WHERE name LIKE $1`, "obfstore-"+sfx+"%")
	})

	params, compat, err := obfuscation.FromPreset(obfuscation.PresetAWG1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateObfuscationProfile(ctx, "obfstore-"+sfx, compat, params)
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 1 || p.Params != params || p.NodeCount != 0 {
		t.Fatalf("created: %+v", p)
	}
	if _, err := s.CreateObfuscationProfile(ctx, "obfstore-"+sfx, compat, params); !errors.Is(err, ErrObfuscationNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	// A rename or an identical re-save keeps the revision; a parameter change bumps it.
	renamed := "obfstore-" + sfx + "-r"
	if got, err := s.UpdateObfuscationProfile(ctx, p.ID, &renamed, nil, &params); err != nil || got.Revision != 1 || got.Name != renamed {
		t.Fatalf("rename: %+v %v", got, err)
	}
	changed := params
	changed.Jc++
	if got, err := s.UpdateObfuscationProfile(ctx, p.ID, nil, nil, &changed); err != nil || got.Revision != 2 {
		t.Fatalf("param change: %+v %v", got, err)
	}

	// Concurrent rotations each get their own revision (row lock), none is lost.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.RotateObfuscationProfile(ctx, p.ID, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	cur, err := s.GetObfuscationProfile(ctx, p.ID)
	if err != nil || cur.Revision != 7 {
		t.Fatalf("after 5 rotations: revision %d, %v", cur.Revision, err)
	}
	if err := cur.Params.Validate(cur.Compat); err != nil {
		t.Fatalf("rotated params invalid: %v", err)
	}

	// Assignment and the applied gate.
	if err := s.SetNodeObfuscationProfile(ctx, node.ID, &cur.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetObfuscationProfile(ctx, cur.ID); got.NodeCount != 1 {
		t.Fatalf("node_count %d", got.NodeCount)
	}
	if applied, err := s.AppliedObfuscationForNode(ctx, node.ID); err != nil || applied != nil {
		t.Fatalf("a freshly assigned node must not count as applied: %v %v", applied, err)
	}
	report := NodeObfuscationReport{Mode: "awg", ProfileID: cur.ID, Revision: cur.Revision - 1, Supported: true}
	if err := s.RecordNodeObfuscation(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	if applied, _ := s.AppliedObfuscationForNode(ctx, node.ID); applied != nil {
		t.Fatal("a stale revision must not count as applied")
	}
	report.Revision = cur.Revision
	_ = s.RecordNodeObfuscation(ctx, node.ID, report)
	if applied, _ := s.AppliedObfuscationForNode(ctx, node.ID); applied == nil || applied.Params != cur.Params {
		t.Fatalf("matching revision must count as applied: %+v", applied)
	}
	// A report naming a profile that does not exist is stored as "none", not an error.
	if err := s.RecordNodeObfuscation(ctx, node.ID, NodeObfuscationReport{Mode: "awg", ProfileID: "00000000-0000-0000-0000-000000000000", Revision: 1}); err != nil {
		t.Fatalf("unknown applied profile: %v", err)
	}
	if err := s.RecordNodeObfuscation(ctx, node.ID, NodeObfuscationReport{Mode: "awg", ProfileID: "not-a-uuid", Revision: 1}); err != nil {
		t.Fatalf("malformed applied profile: %v", err)
	}
	if st, _ := s.GetNodeObfuscation(ctx, node.ID); st.Applied() || st.AppliedProfileID != nil {
		t.Fatalf("state after bogus report: %+v", st)
	}

	// In-use profiles cannot be deleted; unassigned ones can.
	if err := s.DeleteObfuscationProfile(ctx, cur.ID); !errors.Is(err, ErrObfuscationProfileInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	if n, err := s.SetGroupObfuscationProfile(ctx, node.NodeGroup, nil); err != nil || n != 1 {
		t.Fatalf("clear group: %d %v", n, err)
	}
	if err := s.DeleteObfuscationProfile(ctx, cur.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObfuscationProfile(ctx, cur.ID); !errors.Is(err, ErrObfuscationProfileNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.SetNodeObfuscationProfile(ctx, node.ID, &cur.ID); !errors.Is(err, ErrObfuscationProfileNotFound) {
		t.Fatalf("assign deleted profile: %v", err)
	}
}
