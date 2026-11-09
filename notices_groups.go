package main

import (
	"fmt"
	"strconv"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// Notices about groups: approval, leadership and who may see what. The rule
// for notice writers: each lives in notices_<domain>.go for the domain it
// tells people about, reads its recipients and names with the error-returning
// helpers in notice_people.go, and returns any failure so the caller's
// transaction rolls back rather than skip a recipient.

const approvalsLink = "/admin?tab=approvals"

func notifyGroupPending(tx *gorm.DB, g *models.Group, actor uint) error {
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	actorName, err := nameOf(tx, actor)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.GroupPending, To: admins, Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: approvalsLink,
		Vars:      notify.Vars{Actor: actorName, Group: g.Name}, // group-ok: admin tasks go to admins
		Data:      map[string]any{"groupId": g.ID},
		DedupeKey: fmt.Sprintf("group.pending:%d", g.ID),
	})
	return err
}

// notifyGroupDecided tells a pending group's leaders how an admin decided
// it (DEC-96), in the decision's transaction. A leader may always see their
// own group, approved or not.
func notifyGroupDecided(tx *gorm.DB, g *models.Group, actor uint, approved bool, note string) error {
	kind := notify.GroupRejected
	if approved {
		kind = notify.GroupApproved
	}
	leaders, err := leaderIDs(tx, g.ID)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: kind, To: leaders, Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: "/groups/" + strconv.FormatUint(uint64(g.ID), 10),
		Vars: notify.Vars{Group: g.Name, Note: note}, // group-ok: its own leaders
		// No dedupe key: the pending → decided transition sends it once, and a
		// group set back to pending and decided again earns a new notice.
	})
	return err
}

func notifyDeletionRequested(tx *gorm.DB, g *models.Group, actor uint) error {
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	actorName, err := nameOf(tx, actor)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.GroupDeletion, To: admins, Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: approvalsLink,
		Vars: notify.Vars{Actor: actorName, Group: g.Name}, // group-ok: admin tasks go to admins
		Data: map[string]any{"groupId": g.ID},
	})
	return err
}

// notifyLeadership tells the other admins about a leadership change, from
// the RoleChanged or MemberRemoved entry just written (DEC-31).
func notifyLeadership(tx *gorm.DB, e audit.Entry) error {
	meta := audit.ReadRoleMeta(e.Meta)
	userID, oldRole, newRole := meta.UserID, meta.OldRole, meta.NewRole
	var change string
	switch {
	case e.Action == audit.MemberRemoved:
		if oldRole != models.MembershipLeader {
			return nil // removing a plain member isn't a leadership change
		}
		change = "removed_from_group"
	case newRole == models.MembershipLeader:
		change = "added"
	case e.Actor != nil && *e.Actor == userID:
		change = "stepped_down"
	default:
		change = "removed"
	}
	var g models.Group
	if err := tx.Unscoped().First(&g, e.TargetID).Error; err != nil {
		return err
	}
	actor := "Fold"
	if e.Actor != nil {
		var err error
		if actor, err = nameOf(tx, *e.Actor); err != nil {
			return err
		}
	}
	person, err := nameOf(tx, userID)
	if err != nil {
		return err
	}
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.LeadershipChanged, To: admins, Actor: e.Actor,
		SubjectType: "group", SubjectID: g.ID, Link: fmt.Sprintf("/groups/%d", g.ID),
		Vars: notify.Vars{Actor: actor, Person: person, Group: g.Name, Change: change}, // group-ok: leadership notices go to admins
		Data: map[string]any{"groupId": g.ID, "userId": userID},
	})
	if err != nil {
		return err
	}
	return notifyYourRole(tx, e, &g, userID, oldRole, newRole)
}

// notifyYourRole tells the person whose role changed that they now lead
// the group, or no longer do and are still a member (DEC-134). It follows
// every RoleChanged entry (a leader's or admin's change, a handoff), never
// a removal, which has its own notices. Someone who changed their own role
// is skipped (the Actor rule in notify.Send).
func notifyYourRole(tx *gorm.DB, e audit.Entry, g *models.Group, userID uint, oldRole, newRole string) error {
	if e.Action != audit.RoleChanged || userID == 0 || g.ID == 0 {
		return nil
	}
	var change string
	switch {
	case newRole == models.MembershipLeader && oldRole != models.MembershipLeader:
		change = "leader"
	case oldRole == models.MembershipLeader && newRole == models.MembershipMember:
		change = "member"
	default:
		return nil
	}
	vars, link := notify.Vars{Change: change}, "/my-groups"
	sees, err := userCanSeeGroupIn(tx, userID, g)
	if err != nil {
		return err
	}
	if sees {
		vars.Group, link = g.Name, fmt.Sprintf("/groups/%d", g.ID) // group-ok: only when userCanSeeGroupIn says the person may see it
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.YourRole, To: []uint{userID}, Actor: e.Actor,
		SubjectType: "group", SubjectID: g.ID, Link: link, Vars: vars,
	})
	return err
}

// notifyNotRestored tells a re-enabled person they were left out of a full
// group (DEC-47). Their membership is already gone in tx, so an
// invite-only group's name and link are left out.
func notifyNotRestored(tx *gorm.DB, actor *uint, g *models.Group, userID uint) error {
	vars, link := notify.Vars{}, "/"
	sees, err := userCanSeeGroupIn(tx, userID, g)
	if err != nil {
		return err
	}
	if sees {
		vars.Group, link = g.Name, fmt.Sprintf("/groups/%d", g.ID) // group-ok: only when userCanSeeGroupIn says the person may see it
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.MemberNotRestored, To: []uint{userID}, Actor: actor,
		SubjectType: "group", SubjectID: g.ID, Link: link, Vars: vars,
	})
	return err
}

// notifyVisibilityOverride tells the group's other leaders and the admins
// that an event is more open than its group (DEC-17, DEC-28).
func notifyVisibilityOverride(tx *gorm.DB, actor uint, ev *models.Event, g *models.Group, newVis, note string) error {
	leaders, err := leaderIDs(tx, g.ID)
	if err != nil {
		return err
	}
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	actorName, err := nameOf(tx, actor)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.VisibilityOverride, To: append(leaders, admins...), Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: fmt.Sprintf("/groups/%d", g.ID),
		Vars: notify.Vars{Actor: actorName, Event: firstNonEmpty(ev.Title, g.Name), // group-ok: leaders and admins only
			Visibility: visWord(newVis), GroupVis: visWord(g.Visibility), Note: note},
		Data: map[string]any{"eventId": ev.ID, "groupId": g.ID},
	})
	return err
}
