package audit

// RoleMeta is the part of a RoleChanged or MemberRemoved entry's meta that
// leadership notices read back: who, and their role before and after. The
// writers (changeRoleIn, handOffLeadership, demoteEverywhere, applyDeparture)
// spell these keys in their entries' meta; the audit writer census pins those
// literals, so a rename there fails it and must come here.
type RoleMeta struct {
	UserID  uint
	OldRole string
	// NewRole is "" for a removal, which has no new role.
	NewRole string
}

// ReadRoleMeta reads the role facts back from an entry's meta (in memory, as
// the writer built it: userId is a uint or an int).
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
