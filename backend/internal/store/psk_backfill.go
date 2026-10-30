package store

import (
	"context"

	"wgpanel-api/internal/wgkeys"
)

// BackfillPresharedKeys gives every account that has none (created before migration
// 0029) a PresharedKey, encrypted with encryptionKey. SQL cannot do it - only the app
// holds ACCOUNT_KEY_ENCRYPTION_KEY - so the API runs this at start. It works in batches
// and is idempotent: SetAccountPSKIfMissing only writes while the column is NULL, so a
// restart halfway, a second replica, or a concurrent create never overwrites a key.
// Returns how many accounts this call filled.
//
// Generating a key does NOT change what any node requires: migration 0029 marks every
// existing peer plain_served, so those peers never require a PSK; the keys only matter
// for peers created later (new nodes backfilled to an old account).
func (s *Store) BackfillPresharedKeys(ctx context.Context, encryptionKey string) (int, error) {
	const batch = 500
	total := 0
	for {
		ids, err := s.ListAccountsMissingPSK(ctx, batch)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		wrote := 0
		for _, id := range ids {
			psk, err := wgkeys.GeneratePresharedKey()
			if err != nil {
				return total, err
			}
			enc, err := wgkeys.Encrypt(encryptionKey, psk)
			if err != nil {
				return total, err
			}
			ok, err := s.SetAccountPSKIfMissing(ctx, id, enc)
			if err != nil {
				return total, err
			}
			if ok {
				wrote++
			}
		}
		total += wrote
		// Another writer filled the whole batch first: nothing here left to progress on.
		if wrote == 0 {
			return total, nil
		}
	}
}
