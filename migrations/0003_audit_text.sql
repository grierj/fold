-- R30: the audit log keeps the text of what one person sends another. Rows
-- written before this have empty text and keep their format-1 hash.
ALTER TABLE audit_log ADD COLUMN text TEXT NOT NULL DEFAULT '';

-- R31: a group decision's reason, from a fixed list, and an "Other" note.
-- The note is about the group and is also kept in the audit log.
ALTER TABLE groups ADD COLUMN decision_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN decision_note TEXT NOT NULL DEFAULT ''; -- text
