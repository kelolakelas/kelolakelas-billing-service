-- KEL-163: bound ordered history scans to the two manual terminal states.
CREATE INDEX IF NOT EXISTS idx_withdrawals_decided_history
    ON withdrawals (decided_at DESC, id DESC)
    WHERE status IN ('paid', 'rejected');
