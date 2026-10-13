package cold

// Maintain folds WAL pages back and refreshes planner statistics. Same two
// pragmas as Close, safe any time no write runs.
func (v *Vault) Maintain() error {
	if v.db == nil {
		return nil
	}
	_, _ = v.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	_, _ = v.db.Exec(`PRAGMA optimize`)
	return nil
}
