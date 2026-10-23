-- +goose Up
-- Approved conversions keep a stable account association while consent is pending.
CREATE UNIQUE INDEX idx_account_requests_approved_conversion_person ON account_requests(household_person_id)
    WHERE source = 'household_conversion' AND status = 'approved' AND approved_user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_account_requests_approved_conversion_user ON account_requests(approved_user_id)
    WHERE source = 'household_conversion' AND status = 'approved' AND approved_user_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_account_requests_approved_conversion_user;
DROP INDEX idx_account_requests_approved_conversion_person;
