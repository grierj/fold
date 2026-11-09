package audit

// RoleMeta is the part of a RoleChanged or MemberRemoved entry's meta that
// leadership notices read back: who, and their role before and after. The
// writers build their meta with With and the notice reads it with
// ReadRoleMeta, so the keys are spelled once.
type RoleMeta struct {
	UserID  uint
	OldRole string
	// NewRole is "" for a removal, which has no new role.
	NewRole string
}

// With is the entry meta: the role facts plus any extra keys the writer adds.
func (m RoleMeta) With(extra map[string]any) map[string]any {
	meta := map[string]any{"userId": m.UserID, "oldRole": m.OldRole}
	if m.NewRole != "" {
		meta["newRole"] = m.NewRole
	}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

// ReadRoleMeta reads the role facts back from an entry's meta (in memory, as
// the writer built it).
func ReadRoleMeta(meta map[string]any) RoleMeta {
	var m RoleMeta
	switch v := meta["userId"].(type) {
	case uint:
		m.UserID = v
	case int:
		m.UserID = uint(v)
	}
	m.OldRole, _ = meta["oldRole"].(string)
	m.NewRole, _ = meta["newRole"].(string)
	return m
}
