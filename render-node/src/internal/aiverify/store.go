package aiverify

// store.go — aiverify.db 資料表 DDL（WAL、單連線，與 aimarket 同款寫法）。

import "fmt"

// migrate 建表＋索引（CREATE TABLE IF NOT EXISTS，可重複執行）。
func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS aiv_verifiers (
  address TEXT PRIMARY KEY,
  stake REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  reputation INTEGER NOT NULL DEFAULT 0,
  slashed_count INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS aiv_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_chain TEXT NOT NULL DEFAULT 'TACm',
  model_id INTEGER NOT NULL DEFAULT 0,
  model_version TEXT NOT NULL DEFAULT '',
  input_cid TEXT NOT NULL DEFAULT '',
  input_hash TEXT NOT NULL DEFAULT '',
  submitter TEXT NOT NULL,
  expected_output_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  task_fee REAL NOT NULL DEFAULT 0,
  reward_pool REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  challenge_deadline INTEGER NOT NULL,
  settled_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_aiv_tasks_status ON aiv_tasks(status);
CREATE INDEX IF NOT EXISTS idx_aiv_tasks_submitter ON aiv_tasks(submitter);

CREATE TABLE IF NOT EXISTS aiv_attestations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL,
  verifier TEXT NOT NULL,
  decision TEXT NOT NULL,
  claim_hash TEXT NOT NULL DEFAULT '',
  signature TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(task_id, verifier)
);
CREATE INDEX IF NOT EXISTS idx_aiv_att_task ON aiv_attestations(task_id);
CREATE INDEX IF NOT EXISTS idx_aiv_att_verifier ON aiv_attestations(verifier);

CREATE TABLE IF NOT EXISTS aiv_settlements (
  task_id INTEGER PRIMARY KEY,
  status TEXT NOT NULL,
  consensus_hash TEXT NOT NULL DEFAULT '',
  total_votes INTEGER NOT NULL DEFAULT 0,
  quorum INTEGER NOT NULL DEFAULT 0,
  reward_pool REAL NOT NULL DEFAULT 0,
  reward_per_verifier REAL NOT NULL DEFAULT 0,
  correct_verifiers TEXT NOT NULL DEFAULT '',
  slashed_verifiers TEXT NOT NULL DEFAULT '',
  slash_per_verifier REAL NOT NULL DEFAULT 0,
  settled_at INTEGER NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("aiverify: 初始化 schema: %w", err)
	}
	return nil
}
