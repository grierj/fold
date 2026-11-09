package events

// A date's place is its own when it has one (even an explicitly empty one:
// the date has no place), else the event's, else the group's. The rule is
// written once in Go and once in SQL; place_test.go keeps them equal.

// SeriesPlace is the place of an event's dates that have none of their own:
// the event's, else the group's (groupPlace is "" for a church-wide event or
// a group the viewer may not see).
func SeriesPlace(eventPlace, groupPlace string) string {
	if eventPlace != "" {
		return eventPlace
	}
	return groupPlace
}

// EffectivePlace is one date's place: override when set (even if empty),
// else SeriesPlace.
func EffectivePlace(eventPlace string, override *string, groupPlace string) string {
	if override != nil {
		return *override
	}
	return SeriesPlace(eventPlace, groupPlace)
}

// SeriesPlaceSQL is SeriesPlace over two SQL expressions: the event's place
// column and the group's (a column of a left-joined groups row, or a scalar
// subquery; null for a church-wide event).
func SeriesPlaceSQL(eventPlace, groupPlace string) string {
	return "COALESCE(NULLIF(" + eventPlace + ", ''), " + groupPlace + ", '')"
}

// EffectivePlaceSQL is EffectivePlace over SQL expressions, the date's
// override column first.
func EffectivePlaceSQL(override, eventPlace, groupPlace string) string {
	return "COALESCE(" + override + ", " + SeriesPlaceSQL(eventPlace, groupPlace) + ")"
}
