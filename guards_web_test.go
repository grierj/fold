package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/notify"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The SPA's types in web/src/types.ts are written by hand. A Go JSON rename
// that misses them broke the web tests after M1.1a (4e7111b). Each pair
// must name exactly the same fields.
var tsContracts = []struct {
	goType any
	tsName string
}{
	{personSearchJSON{}, "PersonSearchResult"},
	{peopleSearchJSON{}, "PeopleSearchResponse"},
	{contactHistoryPersonJSON{}, "ContactHistoryPerson"},
	{contactHistoryPeopleJSON{}, "ContactHistoryPeopleResponse"},
	{contactHistoryEntryJSON{}, "ContactHistoryEntry"},
	{contactHistoryJSON{}, "ContactHistoryResponse"},
	{contactRequestJSON{}, "ContactRequest"},
	{addRequestJSON{}, "AddRequest"},
	{contactGrantJSON{}, "ContactGrant"},
	{contactRevealJSON{}, "ContactReveal"},
	{userJSON{}, "User"},
	{attemptMintJSON{}, "AttemptMintResponse"},
	{attemptSettleJSON{}, "AttemptSettleResponse"},
	{emailSuppressionJSON{}, "EmailSuppression"},
	{groupSummaryJSON{}, "GroupSummary"},
	{groupDetailJSON{}, "GroupDetailResponse"},
	{groupEventsJSON{}, "GroupEventsResponse"},
	{memberJSON{}, "Member"},
	{departureKeepableDateJSON{}, "DepartureKeepableDate"},
	{departurePreviewJSON{}, "DeparturePreview"},
	{departureConfirmationJSON{}, "DepartureConfirmation"},
	{notificationJSON{}, "Notification"},
	{notificationSettingsJSON{}, "NotificationSettings"},
	{roomJSON{}, "Room"},
	{emailKindJSON{}, "EmailKind"},
	{pushSettingsJSON{}, "PushSettings"},
	{pushDeviceJSON{}, "PushDevice"},
	{pushConfigJSON{}, "PushConfig"},
	{pushSubscribeJSON{}, "PushSubscribeResponse"},
	{pushTestJSON{}, "PushTestResponse"},
	{adminJobJSON{}, "AdminJob"},
	{adminDeliverySummaryJSON{}, "AdminDeliverySummary"},
	{roomSuggestionJSON{}, "RoomSuggestion"},
	{roomSuggestionsJSON{}, "RoomSuggestions"},
	{roomFeedJSON{}, "RoomFeed"},
	{roomCalendarJSON{}, "RoomCalendar"},
	{roomCalendarRoomJSON{}, "RoomCalendarRoom"},
	{roomSlotJSON{}, "RoomSlot"},
	{occurrenceRoomJSON{}, "OccurrenceRoom"},
	{roomPickerJSON{}, "RoomPickerItem"},
	{roomBookingJSON{}, "RoomBooking"},
	{seriesRoomJSON{}, "SeriesRoom"},
	{needsRoomJSON{}, "NeedsRoom"},
	{seriesBookingJSON{}, "SeriesBooking"},
	{roomRequestJSON{}, "RoomRequest"},
	{typedLocationJSON{}, "TypedLocation"},
	{matchBusyJSON{}, "MatchBusy"},
	{resolutionJSON{}, "NotificationResolution"},
	{occurrenceJSON{}, "EventOccurrence"}, // Includes the public childcareFull flag; totals stay optional.
	{calendarOccurrenceJSON{}, "CalendarOccurrence"},
	{dateOccurrenceJSON{}, "EventDate"},
	{eventJSON{}, "EventSeries"},
	{hostJSON{}, "EventHostRow"},
	{impactEventJSON{}, "VisibilityImpactEvent"},
	{visibilityImpactJSON{}, "VisibilityImpact"},
	{householdJSON{}, "Household"},
	{householdPersonJSON{}, "HouseholdPerson"}, // Includes the teen conversion block reason.
	{householdAccountJSON{}, "HouseholdAccount"},
	{guardianLinkJSON{}, "GuardianLink"},
	{erasureReportJSON{}, "ErasureReport"},
	{reasonItemJSON{}, "ErasureReasonItem"},
	{reasonPageJSON{}, "ErasureReasonPage"},
	{guardianNominationJSON{}, "GuardianNomination"},
	{guardianTeenJSON{}, "GuardianTeen"},
	{guardianEmailPreviewJSON{}, "GuardianEmailPreview"},
	{teenConsentJSON{}, "TeenConsent"},
	{invitationJSON{}, "Invitation"},
	{conversionJSON{}, "HouseholdConversion"}, // Includes pending adult approval metadata.
	{invitationAcceptanceJSON{}, "InvitationAcceptance"},
	{invitationContextJSON{}, "InvitationContext"},
	{accountRequestJSON{}, "AccountRequest"},
	{guestRegistrationJSON{}, "GuestRegistration"},
	{attendeeJSON{}, "Attendee"},
	{registrationJSON{}, "RegistrationResponse"},
	{registrationBatchJSON{}, "RegistrationBatch"},
	{seriesRegistrationState{}, "SeriesRegistrationState"},
	{seriesRegistrationResult{}, "SeriesRegistrationResult"},
	{seriesRegistrationRefusal{}, "SeriesRegistrationRefusal"},
	{seriesRegistrationResponse{}, "SeriesRegistrationResponse"},
	{householdAnswer{}, "HouseholdAnswer"},
	{registrationBatchInput{}, "RegistrationCommand"},
	{householdRegistrationJSON{}, "HouseholdRegistration"},
	{myHouseholdRegistrationJSON{}, "MyHouseholdRegistration"},
	{registeredByJSON{}, "RegisteredBy"},
	{householdAttendeeJSON{}, "HouseholdAttendee"},
	{calendarFeedJSON{}, "CalendarFeed"},
	{messageJSON{}, "Message"},
	{messageListJSON{}, "MessageListResponse"},
	{audienceJSON{}, "MessageAudience"},
	{audienceSummaryJSON{}, "AudienceSummary"},
	{audienceRecipientJSON{}, "AudienceRecipient"},
	{composeOptionsJSON{}, "ComposeOptions"},
	{composeGroupJSON{}, "ComposeGroup"},
	{composeEventJSON{}, "ComposeEvent"},
	{composeDateJSON{}, "ComposeDate"},
	{renderedMessage{}, "MessagePreview"},
	{messageRecipientsJSON{}, "MessageRecipients"},
	{recipientCountsJSON{}, "MessageRecipientCounts"},
	{historyRecipientJSON{}, "MessageHistoryRecipient"},
	{pushCountsJSON{}, "MessagePushCounts"},
	{pushWaitJSON{}, "MessagePushWait"},
	{historyPushJSON{}, "MessageHistoryPush"},
	{messageReadJSON{}, "MessageRead"},
	{messageReminderJSON{}, "MessageReminder"},
	{dateRemindersJSON{}, "DateReminders"},
	{dateReminderJSON{}, "DateReminder"},
	{successorJSON{}, "HandoffCandidate"},
	{soleHostEventJSON{}, "SoleHostedEvent"},
	{soleHostEventsJSON{}, "SoleHostedEventsResponse"},
	{soleLedGroupJSON{}, "SoleLedGroup"},
	{sessionJSON{}, "SessionResponse"},
	{siteJSON{}, "SiteInfo"},
	{accountRequestConfirmJSON{}, "AccountRequestConfirmation"},
	{accountRequestDecisionJSON{}, "AccountRequestDecision"},
	{calendarResponseJSON{}, "CalendarResponse"},
	{guestConfirmationJSON{}, "GuestConfirmation"},
	{profileUpdateJSON{}, "ProfileUpdateResponse"},
	{emailSettingsLinkJSON{}, "EmailSettingsLinkResponse"},
	{teenConsentsJSON{}, "TeenConsentsResponse"},
	{guardianLinksResponseJSON{}, "GuardianLinksResponse"},
	{adminJobsJSON{}, "AdminJobsResponse"},
	{adminRoomsJSON{}, "AdminRoomsResponse"},
	{matchLocationJSON{}, "MatchLocationResponse"},
	{roomRequestConfirmationJSON{}, "RoomRequestConfirmation"},
	{dateRoomsJSON{}, "DateRoomsResponse"},
	{seriesRoomsJSON{}, "SeriesRoomsResponse"},
	{roomsCheckJSON{}, "RoomsCheckResponse"},
	{badgeJSON{}, "Badge"},
}

var tsFieldRe = regexp.MustCompile(`^\s+(\w+)\??:`)

func tsFields(src, name string) map[string]bool {
	out := map[string]bool{}
	in := false
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "export interface "+name+" "):
			in = true
		case in && strings.HasPrefix(line, "}"):
			return out
		case in:
			if m := tsFieldRe.FindStringSubmatch(line); m != nil {
				out[m[1]] = true
			}
		}
	}
	return out
}

func goFields(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			out[tag] = true
		}
	}
	return out
}

func contractDiff(goSide, tsSide map[string]bool) (missingInTS, extraInTS []string) {
	for k := range goSide {
		if !tsSide[k] {
			missingInTS = append(missingInTS, k)
		}
	}
	for k := range tsSide {
		if !goSide[k] {
			extraInTS = append(extraInTS, k)
		}
	}
	sort.Strings(missingInTS)
	sort.Strings(extraInTS)
	return
}

func TestWebTypesMatchGoJSON(t *testing.T) {
	src, err := os.ReadFile("web/src/types.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tsContracts {
		ts := tsFields(string(src), c.tsName)
		if len(ts) == 0 {
			t.Errorf("web/src/types.ts: interface %s not found", c.tsName)
			continue
		}
		missing, extra := contractDiff(goFields(c.goType), ts)
		if len(missing)+len(extra) > 0 {
			t.Errorf("%T vs web/src/types.ts %s: add %v to the TS interface, remove or rename %v",
				c.goType, c.tsName, missing, extra)
		}
	}
}

// The contract check catches the M1.1a rename (nextMeetingAt -> nextEventAt).
func TestWebTypesGuardCatchesFieldRename(t *testing.T) {
	old := "export interface GroupSummary {\n  nextMeetingAt: string | null;\n}\n"
	missing, extra := contractDiff(map[string]bool{"nextEventAt": true}, tsFields(old, "GroupSummary"))
	if len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("missing %v extra %v", missing, extra)
	}
}

// DEC-16: video is link-out only — a plain anchor to the call, in a new
// tab. Nothing in web/src may embed a frame.
func TestNoVideoEmbed(t *testing.T) {
	err := filepath.WalkDir("web/src", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// The composer's email preview is the one iframe allowed: our own
		// page, sandboxed with no scripts (M3.4c). It lives only in the
		// composer and says why with "iframe-ok:" on the line before it.
		allowed := filepath.ToSlash(p) == "web/src/pages/MessageComposer.tsx"
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), "<iframe") && (!allowed || i == 0 || !strings.Contains(lines[i-1], "iframe-ok:")) {
				t.Errorf("%s:%d embeds an <iframe>; video is a link out in a new tab (DEC-16)", p, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Colors come from the theme tokens in web/src/index.css (UI makeover PR 3),
// so a token change reaches every screen. A hex color anywhere else, or a
// class from Tailwind's own palette (bg-red-600; it builds nothing now that
// the theme switches that palette off), fails. A line that must keep a hex
// says why with "color-ok:".
var (
	hexColorRe     = regexp.MustCompile(`#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b`)
	paletteClassRe = regexp.MustCompile(`\b(?:bg|text|border|ring|ring-offset|divide|outline|from|via|to|fill|stroke|accent|caret|decoration|placeholder|shadow)-(?:slate|gray|zinc|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|brand|neutral|pending|accent)(?:-\d{2,3})?\b|\b(?:bg|text|border|ring|divide|outline|from|via|to|fill|stroke|accent|decoration)-(?:primary|danger|success|warning|info)-\d{2,3}\b`)
	// colorScanDirs hold everything that styles a page.
	colorScanDirs = []string{"web/src", "templates"}
	// themeFile is the one place hex values live, inside its @theme block.
	themeFile = "web/src/index.css"
)

// colorViolations checks one file. Inside the theme file, hex values are
// allowed between "@theme {" and its closing "}".
func colorViolations(path, src string) []string {
	var bad []string
	inTheme := false
	for i, line := range strings.Split(src, "\n") {
		if path == themeFile {
			if strings.HasPrefix(line, "@theme") {
				inTheme = true
			} else if inTheme && strings.HasPrefix(line, "}") {
				inTheme = false
				continue
			}
		}
		if strings.Contains(line, "color-ok:") {
			continue
		}
		where := path + ":" + itoa(uint(i+1)) + ": "
		if !inTheme {
			for _, h := range hexColorRe.FindAllString(line, -1) {
				bad = append(bad, where+h+" is a hard-coded color. Use a theme token (bg-primary, text-danger) or add one to @theme in "+themeFile)
			}
		}
		for _, c := range paletteClassRe.FindAllString(line, -1) {
			bad = append(bad, where+c+" is a palette color or scale step, which the theme does not build. Use a role from the theme in web/src/index.css: primary, page, surface, sunken, border, text, text-muted, danger, success, warning, info (with -hover, -subtle, -on-subtle, -border, -foreground)")
		}
	}
	return bad
}

func TestColorsComeFromThemeTokens(t *testing.T) {
	var bad []string
	for _, dir := range colorScanDirs {
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".ts", ".tsx", ".css", ".html":
			default:
				return nil
			}
			if strings.Contains(p, ".test.") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			bad = append(bad, colorViolations(filepath.ToSlash(p), string(b))...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("colors outside the theme (AGENTS.md, Styling):\n%s", strings.Join(bad, "\n"))
	}
}

// The color check catches what this rule replaced: the old static/style.css
// red, a Tailwind palette class, and a hex outside @theme in index.css.
func TestColorGuardCatchesHardCodedColors(t *testing.T) {
	cases := map[string]string{
		"templates/setup.html":        `<style>header { background-color: #e51937; }</style>`,
		"web/src/pages/Admin.tsx":     `<button className="btn-secondary text-red-600!">`,
		"web/src/components/Card.tsx": `<div className="bg-brand-dark">`,
		"web/src/index.css":           "@theme {\n  --color-primary: #f64060;\n}\n.btn { color: #fff; }",
	}
	for path, src := range cases {
		if len(colorViolations(path, src)) == 0 {
			t.Errorf("%s: %q passed the color check", path, src)
		}
	}
	ok := map[string]string{
		"web/src/index.css":       "@theme {\n  --color-primary: #f64060;\n}",
		"web/src/pages/Admin.tsx": `<span className="bg-danger-subtle text-text-muted">PR #41</span>`,
		"web/src/x.ts":            `const c = "#7c3aed"; // color-ok: placeholder cover`,
	}
	for path, src := range ok {
		if v := colorViolations(path, src); len(v) != 0 {
			t.Errorf("%s flagged: %v", path, v)
		}
	}
}

// Pages show errors through errorText (web/src/lib/errors.ts), which words
// the API's code; the server's English never reaches the screen (DEC-61,
// UI makeover PR 4). Reading .message on a caught error anywhere else
// fails. errors.ts itself and tests are exempt.
var errorMessageRe = regexp.MustCompile(`\b(e|err|error|ex|caught|reason)\.message\b`)

func errorMessageViolations(path, src string) []string {
	var bad []string
	for i, line := range strings.Split(src, "\n") {
		if errorMessageRe.MatchString(line) {
			bad = append(bad, path+":"+itoa(uint(i+1))+": "+strings.TrimSpace(line))
		}
	}
	return bad
}

func TestPagesNeverShowServerErrorText(t *testing.T) {
	var bad []string
	err := filepath.WalkDir("web/src", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) || strings.Contains(p, ".test.") || filepath.ToSlash(p) == "web/src/lib/errors.ts" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		bad = append(bad, errorMessageViolations(filepath.ToSlash(p), string(b))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("show errors with errorText(e, \"We couldn't ...\") from web/src/lib/errors.ts, not the server's text:\n%s", strings.Join(bad, "\n"))
	}
}

// The guard catches the pattern this PR replaced (the home page's "boom").
func TestErrorTextGuardCatchesRawMessages(t *testing.T) {
	for _, src := range []string{
		`setError(e instanceof Error ? e.message : "Failed to load groups");`,
		`setError(err.message);`,
		`alert(error.message)`,
	} {
		if len(errorMessageViolations("web/src/pages/Home.tsx", src)) == 0 {
			t.Errorf("%q passed the error-text check", src)
		}
	}
	if v := errorMessageViolations("web/src/pages/Admin.tsx", `{request.message && <p>{request.message}</p>}`); len(v) != 0 {
		t.Errorf("an account request's own message was flagged: %v", v)
	}
}

// --- UI ratchets (DEC-60 to DEC-66) ---
//
// Each rule counts its pattern per file in web/src (tests and
// components/ui excluded). ui-ratchet.json holds today's counts: a file may
// not go above its count, and when a file gets better its count must come
// down in the same change, so the ratchet only turns one way.
// UPDATE_UI_RATCHET=1 go test -run TestUIRatchets rewrites the file.

type uiRule struct {
	name, fix string
	re        *regexp.Regexp
	skip      func(path, line string) bool
}

var uiRules = []uiRule{
	{
		name: "rawControls",
		fix:  "use <Button variant=…>, <Chip selected=…> or <Field label=…><Input/></Field> from components/ui (AGENTS.md, UI controls)",
		re:   regexp.MustCompile(`<(button|input|select|textarea)\b|className=["{][^"}]*\b(btn-[a-z-]+|chip)\b`),
	},
	{
		name: "importantOverrides",
		fix:  "pick a Button or Chip size instead of overriding the component class with `!`",
		re:   regexp.MustCompile(`\b[a-z][\w:.-]*!(\s|"|$)`),
	},
	{
		name: "danger",
		fix:  "red means an error or a destructive action; neutral states use text-text-muted or <EmptyState>. A real danger use ends with `// danger-ok: <reason>`",
		re:   regexp.MustCompile(`\b(text|bg|border)-danger`),
		skip: func(path, line string) bool {
			return strings.HasSuffix(path, "ErrorNotice.tsx") || strings.Contains(line, "danger-ok:")
		},
	},
	{
		name: "styleLiterals",
		fix:  "use a token class; style={…} is for computed positions only (objectPosition, left, top, width from a variable)",
		re:   regexp.MustCompile(`style=\{\{[^}]*\b(color|background|font\w*|margin\w*|padding\w*|border\w*)\s*:`),
	},
	{
		name: "dateFormatting",
		fix:  "format dates and times with lib/when.ts so they show in the viewer's zone",
		re:   regexp.MustCompile(`\.toLocale(Date|Time)?String\(|Intl\.DateTimeFormat`),
		skip: func(path, _ string) bool {
			return strings.HasSuffix(path, "lib/when.ts")
		},
	},
}

func uiRuleCounts(path, src string) map[string]int {
	out := map[string]int{}
	for _, r := range uiRules {
		for _, line := range strings.Split(src, "\n") {
			if r.skip != nil && r.skip(path, line) {
				continue
			}
			out[r.name] += len(r.re.FindAllString(line, -1))
		}
	}
	return out
}

func uiSourceFiles(t *testing.T) map[string]string {
	files := map[string]string{}
	err := filepath.WalkDir("web/src", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "web/src/components/ui" {
				return filepath.SkipDir
			}
			return nil
		}
		if !(strings.HasSuffix(p, ".tsx") || strings.HasSuffix(p, ".ts")) || strings.Contains(p, ".test.") || strings.HasPrefix(p, "web/src/test/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(p)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func uiRatchetViolations(ratchet map[string]map[string]int, files map[string]string) []string {
	var out []string
	for p, src := range files {
		got := uiRuleCounts(p, src)
		for _, r := range uiRules {
			want := ratchet[r.name][p]
			switch {
			case got[r.name] > want:
				out = append(out, fmt.Sprintf("%s: %s went from %d to %d; %s", p, r.name, want, got[r.name], r.fix))
			case got[r.name] < want:
				out = append(out, fmt.Sprintf("%s: %s fell from %d to %d. Good: lower it in ui-ratchet.json (UPDATE_UI_RATCHET=1 go test -run TestUIRatchets)", p, r.name, want, got[r.name]))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestUIRatchets(t *testing.T) {
	files := uiSourceFiles(t)
	if os.Getenv("UPDATE_UI_RATCHET") != "" {
		ratchet := map[string]map[string]int{}
		for _, r := range uiRules {
			ratchet[r.name] = map[string]int{}
		}
		for p, src := range files {
			for name, n := range uiRuleCounts(p, src) {
				if n > 0 {
					ratchet[name][p] = n
				}
			}
		}
		b, _ := json.MarshalIndent(ratchet, "", "  ")
		if err := os.WriteFile("ui-ratchet.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile("ui-ratchet.json")
	if err != nil {
		t.Fatal(err)
	}
	var ratchet map[string]map[string]int
	if err := json.Unmarshal(b, &ratchet); err != nil {
		t.Fatal(err)
	}
	for _, v := range uiRatchetViolations(ratchet, files) {
		t.Error(v)
	}
}

// Each rule fails on the real past mistake it is for.
func TestUIRatchetsCatchPastMistakes(t *testing.T) {
	cases := []struct{ rule, path, line string }{
		// PR3-REVIEW M1: GroupCard's hand-rolled tag drifted from .chip.
		{"rawControls", "web/src/components/GroupCard.tsx", `<button className="rounded-full px-2 py-0.5" onClick={pick}>`},
		// Admin.tsx in PR #60.
		{"importantOverrides", "web/src/pages/Admin.tsx", `<button className="btn-danger-outline py-1.5! px-3! text-sm">`},
		// GroupCard.tsx at 0e6b7a7, in today's tokens.
		{"danger", "web/src/components/GroupCard.tsx", `<p className="text-danger">No events scheduled yet</p>`},
		{"styleLiterals", "web/src/pages/Home.tsx", `<div style={{ color: "#333", objectPosition: x }}>`},
		// Notifications.tsx:79 before this change.
		{"dateFormatting", "web/src/pages/Notifications.tsx", `{new Date(n.createdAt).toLocaleString()}`},
	}
	for _, c := range cases {
		if got := uiRatchetViolations(map[string]map[string]int{}, map[string]string{c.path: c.line}); len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), c.rule) {
			t.Errorf("%s not caught on %q: %v", c.rule, c.line, got)
		}
	}
	ok := `<Chip selected={on}>tag</Chip> <img style={{ objectPosition: pos }} /> <p className="text-danger">{error} {/* danger-ok: form error */}</p>`
	if got := uiRatchetViolations(map[string]map[string]int{}, map[string]string{"web/src/pages/X.tsx": ok}); len(got) != 0 {
		t.Errorf("false positives: %v", got)
	}
}

// --- Form controls (DEC-65) ---
//
// Every <input>, <select> and <textarea> lives in web/src/components/ui
// (Field, Input, Select, Textarea, ZonePicker, Radio, Checkbox), so every
// control gets a programmatic label, the hint above it and the error under
// it. Pages and other components use those instead of raw controls.

var (
	reactFormStart      = regexp.MustCompile(`<form(?:\s|>)`)
	reactFormNoValidate = regexp.MustCompile(`^<form\s+noValidate(?:\s|>)`)
	rawFormControl      = regexp.MustCompile(`<(input|select|textarea)\b`)
)

func reactFormValidationViolations(files map[string]string) []string {
	const sharedFormPath = "web/src/components/ui/Form.tsx"
	var out []string
	for p, src := range files {
		for _, loc := range reactFormStart.FindAllStringIndex(src, -1) {
			line := strings.Count(src[:loc[0]], "\n") + 1
			if p != sharedFormPath {
				out = append(out, fmt.Sprintf("%s:%d: raw `<form>`; use <Form> from components/ui/Form", p, line))
				continue
			}
			if !reactFormNoValidate.MatchString(src[loc[0]:]) {
				out = append(out, fmt.Sprintf("%s:%d: the shared native form must start with `<form noValidate ` (or `<form noValidate>`)", p, line))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestReactFormsUseCustomValidation(t *testing.T) {
	files := map[string]string{}
	err := filepath.WalkDir("web/src", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".tsx") || strings.HasSuffix(p, ".test.tsx") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(p)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range reactFormValidationViolations(files) {
		t.Error(v)
	}
}

func TestReactFormValidationGuard(t *testing.T) {
	const sharedFormPath = "web/src/components/ui/Form.tsx"
	badSharedForms := map[string]string{
		"missing":     `<form onSubmit={save}>`,
		"not first":   `<form className="box" noValidate>`,
		"true value":  `<form noValidate={true}>`,
		"false value": `<form noValidate={false}>`,
	}
	for name, src := range badSharedForms {
		if got := reactFormValidationViolations(map[string]string{sharedFormPath: src}); len(got) != 1 {
			t.Errorf("%s: want one violation, got %v", name, got)
		}
	}
	if got := reactFormValidationViolations(map[string]string{"web/src/pages/Login.tsx": `<form noValidate>`}); len(got) != 1 {
		t.Errorf("raw form outside the shared component: want one violation, got %v", got)
	}
	good := map[string]string{
		sharedFormPath:             "<form noValidate onSubmit={save}>\n<form\n  noValidate\n  onSubmit={save}\n>",
		"web/src/pages/Format.tsx": `<format /> </form>`,
	}
	if got := reactFormValidationViolations(good); len(got) != 0 {
		t.Errorf("valid shared forms or non-forms were flagged: %v", got)
	}
}

func rawFormControlViolations(files map[string]string) []string {
	var out []string
	for p, src := range files {
		for i, line := range strings.Split(src, "\n") {
			if rawFormControl.MatchString(line) {
				out = append(out, fmt.Sprintf("%s:%d: raw form control; use <Field label=…> with <Input>, <Select>, <Textarea>, <ZonePicker>, or <Choices> with <Radio>/<Checkbox> from components/ui (DEC-65)", p, i+1))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestFormControlsComeFromComponentsUI(t *testing.T) {
	for _, v := range rawFormControlViolations(uiSourceFiles(t)) {
		t.Error(v)
	}
}

func TestFormControlGuardCatchesPlaceholderLabel(t *testing.T) {
	// Home.tsx before PR 7: a search box labeled only by its placeholder.
	bad := map[string]string{"web/src/pages/Home.tsx": `            <input value={search} placeholder="Search groups…" className="input" />`}
	if got := rawFormControlViolations(bad); len(got) != 1 {
		t.Errorf("raw <input> not caught: %v", got)
	}
	ok := map[string]string{"web/src/pages/Home.tsx": `<Field label="Search groups"><Input type="search" /></Field>`}
	if got := rawFormControlViolations(ok); len(got) != 0 {
		t.Errorf("false positive: %v", got)
	}
}

// --- Notification categories are one list ---
//
// notify.Categories, the notifications.category CHECK (the baseline migration)
// and the TS Notification["category"] union must name the same set (#79).

func TestNotificationCategoriesMatch(t *testing.T) {
	want := append([]string(nil), notify.Categories...)
	sort.Strings(want)
	quoted := regexp.MustCompile(`["']([a-z_]+)["']`)
	pick := func(file string, re *regexp.Regexp) []string {
		t.Helper()
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := re.FindStringSubmatch(string(src))
		if m == nil {
			t.Fatalf("%s: category list not found", file)
		}
		var out []string
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
		sort.Strings(out)
		return out
	}
	// The baseline is a pg_dump, so Postgres renders the CHECK as an ANY(ARRAY[...]).
	sqlCats := pick("migrations/00001_baseline.sql", regexp.MustCompile(`notifications_category_check CHECK \(\(category = ANY \(\(?ARRAY\[([^\]]*)\]`))
	tsCats := pick("web/src/types.ts", regexp.MustCompile(`(?s)interface Notification \{.*?category: ([^;]*);`))
	if !reflect.DeepEqual(sqlCats, want) {
		t.Errorf("SQL CHECK categories %v, notify.Categories %v", sqlCats, want)
	}
	if !reflect.DeepEqual(tsCats, want) {
		t.Errorf("TS Notification category %v, notify.Categories %v", tsCats, want)
	}
}
