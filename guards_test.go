package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/notify"
)

// Repo guards from the /correct pass (Oct 2026). Each one fails on a real
// past mistake; see the rule table in AGENTS.md.

// groupDetailRe matches reading a group's name or place, the facts an
// invite-only group must not leak through its opened events (PR #41).
var groupDetailRe = regexp.MustCompile(`\b(g|f\.group|group)\.(Location|Name)\b|\br\.Group(Location|Name)\b`)

// eventOutputFiles build event and notice output for viewers who may not
// see the event's group.
var eventOutputFiles = []string{"api_occurrence_access.go", "api_events.go", "api_event_access.go", "api_notifications.go", "api_group_visibility.go", "api_guest.go", "api_registrations.go", "api_calendar.go", "api_feeds.go"}

func groupDetailViolations(src string) []string {
	var bad []string
	for i, line := range strings.Split(src, "\n") {
		if groupDetailRe.MatchString(line) && !strings.Contains(line, "// group-ok:") {
			bad = append(bad, strings.TrimSpace(line)+" (line "+itoa(uint(i+1))+")")
		}
	}
	return bad
}

func TestGroupDetailsGoThroughGroupShownTo(t *testing.T) {
	for _, f := range eventOutputFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range groupDetailViolations(string(src)) {
			t.Errorf("%s: %s\n  read the group through a.groupShownTo(me, g, rel) (nil when hidden), "+
				"or end the line with `// group-ok: <why this viewer may see the group>`", f, v)
		}
	}
}

// The guard catches the location leak fixed in 7b50f43 (PR #41).
func TestGroupDetailGuardCatchesPR41Leak(t *testing.T) {
	before := "\t\tif g != nil {\n\t\t\tloc = g.Location\n\t\t}\n"
	if len(groupDetailViolations(before)) != 1 {
		t.Fatal("guard missed the pre-#41 code")
	}
}

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

func TestDependabotCoversEveryEcosystem(t *testing.T) {
	b, err := os.ReadFile(".github/dependabot.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, eco := range []string{"gomod", "npm", "github-actions", "docker"} {
		if !strings.Contains(string(b), "package-ecosystem: "+eco) {
			t.Errorf("dependabot.yml has no %s entry", eco)
		}
	}
	ci, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"govulncheck@v", "npm audit --omit=dev --audit-level=high", "SCREENS_DIR="} {
		if !strings.Contains(string(ci), step) {
			t.Errorf("CI has no %q step", step)
		}
	}
}

// testLayoutAllowed are the only test files with no same-named source file:
// shared helpers, these repo-wide guards, and migration tests.
var testLayoutAllowed = map[string]bool{
	"testhelpers_test.go":              true,
	"guards_test.go":                   true,
	"migrations/drift_test.go":         true,
	"migrations/reason_review_test.go": true,
}

// milestoneNameRe matches a test file named after when it was written
// (m11b_test.go, m03b_limit_test.go, milestone-2.spec.ts) instead of what it tests.
var milestoneNameRe = regexp.MustCompile(`(?i)(^|[_.-])m\d+[a-z]?([_.-]|$)|milestone`)

// testLayoutViolations checks one repo-relative test file path against the
// layout rule; exists reports whether a repo-relative path exists.
func testLayoutViolations(path string, exists func(string) bool) []string {
	dir, base := filepath.Split(path)
	var out []string
	if milestoneNameRe.MatchString(base) {
		out = append(out, path+": named after a milestone. Move its tests into the file for the code they test (X_test.go next to X.go, Foo.test.tsx next to Foo.tsx, e2e specs by feature); shared helpers go in testhelpers_test.go")
		return out
	}
	if testLayoutAllowed[path] || strings.HasPrefix(path, "web/e2e/") {
		return out
	}
	var sources []string
	switch {
	case strings.HasSuffix(base, "_test.go"):
		sources = []string{strings.TrimSuffix(base, "_test.go") + ".go"}
	case strings.Contains(base, ".test."):
		stem := base[:strings.Index(base, ".test.")]
		switch ext := filepath.Ext(base); ext {
		case ".js", ".mjs", ".cjs":
			sources = []string{stem + ext}
		default:
			sources = []string{stem + ".ts", stem + ".tsx"}
		}
	default:
		return out
	}
	for _, s := range sources {
		if exists(dir + s) {
			return out
		}
	}
	out = append(out, path+": no matching source file ("+strings.Join(sources, " or ")+" in the same directory). Move these tests into the _test file of the code they test; put shared helpers in testhelpers_test.go; only add to testLayoutAllowed in guards_test.go for helpers, integration or migration tests")
	return out
}

func TestTestFilesSitNextToTheirSource(t *testing.T) {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	var bad []string
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "test-results", "playwright-report", "data":
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		if strings.HasSuffix(n, "_test.go") || strings.Contains(n, ".test.") || strings.HasSuffix(n, ".spec.ts") {
			bad = append(bad, testLayoutViolations(filepath.ToSlash(p), exists)...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("test layout (AGENTS.md, Tests):\n%s", strings.Join(bad, "\n"))
	}
}

// The layout check catches the files this rule replaced.
func TestTestLayoutGuardCatchesMilestoneFiles(t *testing.T) {
	none := func(string) bool { return false }
	for _, p := range []string{"m11b_test.go", "m03b_limit_test.go", "web/e2e/m12.spec.ts", "milestone_test.go", "tz_guard_test.go", "web/src/Orphan.test.tsx", "web/scripts/orphan.test.mjs"} {
		if len(testLayoutViolations(p, none)) == 0 {
			t.Errorf("%s passed the layout check", p)
		}
	}
	if v := testLayoutViolations("api_events_test.go", func(p string) bool { return p == "api_events.go" }); len(v) != 0 {
		t.Errorf("api_events_test.go flagged: %v", v)
	}
	if v := testLayoutViolations("web/scripts/bundle-size.test.mjs", func(p string) bool { return p == "web/scripts/bundle-size.mjs" }); len(v) != 0 {
		t.Errorf("bundle-size.test.mjs flagged: %v", v)
	}
	if v := testLayoutViolations("web/e2e/events.spec.ts", none); len(v) != 0 {
		t.Errorf("events.spec.ts flagged: %v", v)
	}
}

// DEC-52: rate limits and account checks never reach a user on a public
// form; it answers as usual and logs. Only sign-in, authenticated password
// reauthentication (the same lockout), and a signed-in leader's invitation
// limit answer 429. Keep the exceptions function-specific so a new public
// endpoint in the same file cannot silently gain an exception.
func TestOnlyLoginAndInvitesAnswer429(t *testing.T) {
	allowed := map[string]map[string]bool{
		"api_auth.go":        {"apiLogin": true, "reauthenticate": true},
		"api_invitations.go": {"apiCreateInvitation": true},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		// api_errors.go only names the code for a 429; it sends none itself.
		if strings.HasSuffix(f, "_test.go") || f == "api_errors.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && allowed[f][fn.Name.Name] {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "StatusTooManyRequests" {
					t.Errorf("%s answers 429 outside an allowed handler; public forms answer as usual and log instead (DEC-52)", f)
				}
				return true
			})
		}
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
			bad = append(bad, where+c+" is a palette color or scale step, which the theme does not build. Use a role from TOKENS.md: primary, page, surface, sunken, border, text, text-muted, danger, success, warning, info (with -hover, -subtle, -on-subtle, -border, -foreground)")
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

// --- Python (AGENTS "No Python"; b6c132e removed python3 from verify-fold) ---

var pythonRe = regexp.MustCompile(`(?m)(^|[\s;|&(` + "`" + `$])python[23]?(\s|$)`)

func pythonViolations(path, src string) []string {
	if strings.HasSuffix(path, ".py") {
		return []string{path + ": Python file; Fold tooling is shell or Go only. Rewrite it as a shell script or a Go program"}
	}
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if pythonRe.MatchString(line) {
			out = append(out, path+":"+strconv.Itoa(i+1)+": runs Python; use shell (awk, sed, a free port via the Go binary) or Go instead")
		}
	}
	return out
}

// scriptFiles are the tracked files that run commands.
func scriptFiles(t *testing.T) []string {
	var files []string
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == "web/node_modules" || p == ".git" || p == "web/dist" || p == "data") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		ext := filepath.Ext(p)
		if ext == ".sh" || ext == ".py" || ext == ".yml" || ext == ".yaml" || ext == ".mjs" || base == "Makefile" || base == "Dockerfile" ||
			(strings.HasPrefix(p, ".cursor/skills/") && ext != ".md") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNoPython(t *testing.T) {
	for _, p := range scriptFiles(t) {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range pythonViolations(p, string(src)) {
			t.Error(v)
		}
	}
}

func TestPythonGuardCatchesVerifyFoldPython(t *testing.T) {
	// Lines from .cursor/skills/verify-fold/verify-fold before b6c132e.
	old := "  python3 - \"$1\" \"$2\" <<'PY'\n  port=\"$(python3 -c 'import socket')\"\n# No Python: shell and Go only.\n"
	if got := pythonViolations("verify-fold", old); len(got) != 2 {
		t.Fatalf("want 2 violations, got %v", got)
	}
	if got := pythonViolations("tool.py", ""); len(got) != 1 {
		t.Fatalf(".py file not caught: %v", got)
	}
}

// --- "Recommended", not "supported" (Grier; 98f1a9a reworded three) ---

var supportedRe = regexp.MustCompile(`(?i)\b(supported|unsupported|supports)\b`)

func supportedViolations(path, src string) []string {
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, `"supported"`) || strings.Contains(line, "wording-ok:") {
			continue
		}
		if supportedRe.MatchString(line) {
			out = append(out, path+":"+strconv.Itoa(i+1)+`: say "recommended" (or "tested", "works with") instead of "supported"; Fold makes no support promise`)
		}
	}
	return out
}

func TestDocsSayRecommendedNotSupported(t *testing.T) {
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == "web/node_modules" || p == ".git" || p == "docs/plan" || p == "data") {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".md" {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, v := range supportedViolations(p, string(src)) {
			t.Error(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSupportedGuardCatches98f1a9a(t *testing.T) {
	old := "A single DigitalOcean droplet is the supported production install.\n`ubuntu-24-04-x64` remains a supported choice.\n"
	if got := supportedViolations("docs/DEPLOY-DIGITALOCEAN.md", old); len(got) != 2 {
		t.Fatalf("want 2, got %v", got)
	}
}

// --- One Node version (.nvmrc): a21cc93 and 8c676ac each hand-edited CI,
// the Dockerfile and docs, and PR #18 offered a non-LTS Node ---

var dockerNodeRe = regexp.MustCompile(`(?m)^FROM node:(\d+)`)
var docNodeRe = regexp.MustCompile(`Node (\d+)( LTS)?\b`)

func nodeVersionViolations(nvmrc string, files map[string]string) []string {
	want := strings.TrimSpace(nvmrc)
	var out []string
	for p, src := range files {
		switch {
		case p == "Dockerfile":
			m := dockerNodeRe.FindStringSubmatch(src)
			if m == nil || m[1] != want {
				out = append(out, p+": FROM node:"+want+"-alpine (the major in .nvmrc)")
			}
		case strings.HasSuffix(p, ".yml"):
			if strings.Contains(src, "node-version:") {
				out = append(out, p+": use node-version-file: .nvmrc, not node-version")
			}
		default:
			for _, m := range docNodeRe.FindAllStringSubmatch(src, -1) {
				if m[2] != "" && m[1] != want {
					out = append(out, p+": says Node "+m[1]+" LTS; .nvmrc says "+want)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestNodeVersionComesFromNvmrc(t *testing.T) {
	nvmrc, err := os.ReadFile(".nvmrc")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, p := range []string{"Dockerfile", ".github/workflows/test.yml", "README.md", "AGENTS.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	for _, v := range nodeVersionViolations(string(nvmrc), files) {
		t.Error(v)
	}
}

func TestNodeGuardCatchesA21cc93Drift(t *testing.T) {
	// 8c676ac^: CI pinned node-version "22"; a half-done bump leaves the
	// Dockerfile or docs behind.
	got := nodeVersionViolations("24\n", map[string]string{
		"Dockerfile":                 "FROM node:22-alpine AS webbuild\n",
		".github/workflows/test.yml": "          node-version: \"22\"\n",
		"README.md":                  "Recommended: Go 1.26+, Node 22 LTS, and Docker\n",
	})
	if len(got) != 3 {
		t.Fatalf("want 3, got %v", got)
	}
}

// --- Contract migration gate (PLAN 14.1, DEC-138): CI must run
// scripts/contract-gate.sh on a full checkout, before anything else can hide
// a failure ---

var contractGateKeyRe = regexp.MustCompile(`^(["']?[A-Za-z0-9_-]+["']?)[ \t]*:[ \t]*(.*)$`)

func contractGateKey(line string) (string, string, bool) {
	m := contractGateKeyRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return strings.Trim(m[1], `"'`), contractGateValue(m[2]), true
}

type contractGateStep struct {
	keys map[string]string // step-level keys (the first line's "- " removed)
	with map[string]string // keys one level under "with:"
	env  map[string]string // keys one level under "env:"
}

// contractGateValue drops a trailing " # comment" from a YAML scalar.
func contractGateValue(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// The two gate steps the test job must run, exactly as written.
const (
	contractGateRun = "bash scripts/contract-gate.sh"
	mergeRulesRun   = "bash scripts/merge-rules-check.sh"
)

// testJobSteps reads the steps of a workflow's test job with plain lines (no
// YAML dependency). It reports the shapes it cannot read, a conditional or
// failure-tolerant test job, and a multiline continuation after a gate step's
// run or after any `shell: bash`. ok is false when there is no test job to
// read at all.
func testJobSteps(workflow string) (steps []*contractGateStep, problems []string, ok bool) {
	if strings.Contains(workflow, "\r") {
		return nil, []string{"unsupported carriage return in workflow"}, false
	}
	lines := strings.Split(workflow, "\n")
	start := -1
	inJobs := false
	for i, l := range lines {
		if l == "jobs:" {
			inJobs = true
			continue
		}
		if inJobs && l != "" && l[0] != ' ' && l[0] != '#' {
			break
		}
		if inJobs && strings.TrimRight(l, " ") == "  test:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, []string{"no test job under jobs:"}, false
	}
	var block []string
	for _, l := range lines[start:] {
		trim := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if (trim != "" && !strings.HasPrefix(trim, "#") && indent == 2) || (l != "" && l[0] != ' ' && l[0] != '#') {
			break
		}
		block = append(block, l)
	}

	var cur *contractGateStep
	var nested map[string]string // the step's with: or env: map being read
	inSteps, inProtectedScalar := false, false
	for _, l := range block {
		trim := strings.TrimSpace(l)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if indent == 4 {
			inSteps = trim == "steps:"
			cur, nested, inProtectedScalar = nil, nil, false
			if key, _, ok := contractGateKey(trim); !ok {
				problems = append(problems, "unsupported test job mapping key: "+trim)
			} else if key == "if" || key == "continue-on-error" {
				problems = append(problems, "the test job must not be conditional or allowed to fail ("+key+":)")
			}
			continue
		}
		if !inSteps {
			continue
		}
		switch {
		case indent == 6 && strings.HasPrefix(trim, "- "):
			if strings.HasPrefix(trim, "-  ") || strings.HasPrefix(trim, "- \t") {
				problems = append(problems, "unsupported spacing after test step dash")
			}
			cur = &contractGateStep{keys: map[string]string{}, with: map[string]string{}, env: map[string]string{}}
			steps = append(steps, cur)
			nested, inProtectedScalar = nil, false
			trim = strings.TrimSpace(trim[2:])
			fallthrough
		case indent == 8 || indent == 6:
			if cur == nil {
				continue
			}
			nested, inProtectedScalar = nil, false
			if key, value, ok := contractGateKey(trim); ok {
				cur.keys[key] = value
				switch key {
				case "with":
					nested = cur.with
				case "env":
					nested = cur.env
				}
				inProtectedScalar = (key == "run" && (value == contractGateRun || value == mergeRulesRun)) || (key == "shell" && value == "bash")
			} else {
				problems = append(problems, "unsupported test step mapping key: "+trim)
			}
		case indent > 8 && inProtectedScalar:
			problems = append(problems, "a gate step's run or shell must not have a multiline continuation")
		case indent >= 10 && nested != nil:
			if key, value, ok := contractGateKey(trim); ok {
				nested[key] = value
			}
		}
	}
	return steps, problems, true
}

// gateStepIndex finds the first step whose run is exactly run, and says what
// would let it be skipped or pass without running: no such step, a shell
// other than plain bash, an if: or continue-on-error:, or a place before the
// checkout. label names the step in the problems.
func gateStepIndex(steps []*contractGateStep, run, label string) (int, []string) {
	for i, s := range steps {
		if s.keys["run"] != run {
			continue
		}
		var problems []string
		if s.keys["shell"] != "bash" {
			problems = append(problems, "the "+label+" step must set shell: bash")
		}
		for _, k := range []string{"if", "continue-on-error"} {
			if _, ok := s.keys[k]; ok {
				problems = append(problems, "the "+label+" step must not have "+k+":")
			}
		}
		if i == 0 {
			problems = append(problems, "the "+label+" step must come after the checkout step")
		}
		return i, problems
	}
	return -1, []string{"the test job has no step whose run is exactly `" + run + "`"}
}

// contractGateWorkflowProblems says what keeps the contract gate from
// running in the test job: the first step must be a checkout with
// fetch-depth 0, a later step must run exactly `bash scripts/contract-gate.sh`
// with shell: bash and no `if:` or `continue-on-error:`, and it must come
// before `make test`.
func contractGateWorkflowProblems(workflow string) []string {
	steps, problems, ok := testJobSteps(workflow)
	if !ok {
		return problems
	}
	if len(steps) == 0 || !strings.HasPrefix(steps[0].keys["uses"], "actions/checkout@") {
		problems = append(problems, "the first step of the test job must be actions/checkout")
	} else if steps[0].with["fetch-depth"] != "0" {
		problems = append(problems, "the checkout must set fetch-depth: 0 under with: (the gate needs history and tags)")
	}
	gate, gateProblems := gateStepIndex(steps, contractGateRun, "contract gate")
	problems = append(problems, gateProblems...)
	if gate >= 0 {
		makeTest := -1
		for i, s := range steps {
			if s.keys["run"] == "make test" {
				makeTest = i
				break
			}
		}
		if makeTest < 0 {
			problems = append(problems, "the test job has no `make test` step to order the gate before")
		} else if gate > makeTest {
			problems = append(problems, "the contract gate step must come before make test")
		}
	}
	return problems
}

// mergeRulesWorkflowProblems says what keeps the merge rules check (DEC-146)
// from running in the test job, a required check: a step after the checkout
// must run exactly `bash scripts/merge-rules-check.sh` with shell: bash, no
// `if:` or `continue-on-error:`, and GH_TOKEN set to the job's token so the
// API's anonymous rate limit cannot fail it.
func mergeRulesWorkflowProblems(workflow string) []string {
	steps, problems, ok := testJobSteps(workflow)
	if !ok {
		return problems
	}
	i, stepProblems := gateStepIndex(steps, mergeRulesRun, "merge rules check")
	problems = append(problems, stepProblems...)
	if i >= 0 && steps[i].env["GH_TOKEN"] != "${{ github.token }}" {
		problems = append(problems, "the merge rules check step must set GH_TOKEN: ${{ github.token }} under env:")
	}
	return problems
}

func TestCIRunsTheContractGate(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range contractGateWorkflowProblems(string(b)) {
		t.Error(".github/workflows/test.yml: " + p)
	}
}

func TestCIRunsTheMergeRulesCheck(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range mergeRulesWorkflowProblems(string(b)) {
		t.Error(".github/workflows/test.yml: " + p)
	}
}

const (
	contractGateCheckoutStep = "      - uses: actions/checkout@abc # v1\n        with:\n          fetch-depth: 0\n"
	contractGateStepText     = "      # The gate.\n      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash\n"
	contractGateMakeTest     = "      - uses: actions/setup-go@def\n        with:\n          go-version-file: go.mod\n      - run: make test\n"
)

func contractGateWorkflow(testSteps, webSteps string) string {
	return "name: test\non:\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    env:\n      X: y\n    steps:\n" +
		testSteps +
		"  web:\n    runs-on: ubuntu-latest\n    steps:\n" + webSteps
}

func TestContractGateWorkflowGuardCatchesMistakes(t *testing.T) {
	webCheckout := "      - uses: actions/checkout@abc\n"
	cases := []struct {
		name, workflow, want string
	}{
		{"step removed", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout), "no step whose run is exactly"},
		{"step only in the web job", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout+contractGateStepText), "no step whose run is exactly"},
		{"if false", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        if: ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"continue-on-error", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        continue-on-error: true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"quoted if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"if\": ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"spaced if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        if : ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"quoted continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        'continue-on-error': true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"spaced continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        continue-on-error : true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"quoted job if", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"if\": ${{ false }}\n", 1), "test job must not be conditional"},
		{"spaced job continue", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    continue-on-error : true\n", 1), "test job must not be conditional"},
		{"escaped step if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"i\\u0066\": ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "unsupported test step mapping key"},
		{"escaped step continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"continue-on-erro\\u0072\": true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "unsupported test step mapping key"},
		{"escaped job if", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"i\\u0066\": ${{ false }}\n", 1), "unsupported test job mapping key"},
		{"escaped job continue", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"continue-on-erro\\u0072\": true\n", 1), "unsupported test job mapping key"},
		{"fake job in workflow metadata", strings.Replace(strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout), "name: test\n", "", 1), "  test:\n", "  \"test\":\n", 1) + "name: |\n  test:\n    steps:\n" + contractGateCheckoutStep + contractGateStepText + contractGateMakeTest, "no test job under jobs:"},
		{"quoted skipped sibling job", strings.Replace(strings.Replace(contractGateWorkflow(contractGateCheckoutStep+"      - run: |\n          make test\n", webCheckout), "  test:\n", "  skip_gate:\n    if: ${{ false }}\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n  test:\n", 1), "  web:\n", "  \"gate_decoy\":\n    needs: skip_gate\n    runs-on: ubuntu-latest\n    steps:\n"+contractGateCheckoutStep+contractGateStepText+contractGateMakeTest+"  web:\n", 1), "no step whose run is exactly"},
		{"escaped if with extra dash spacing", contractGateWorkflow(contractGateCheckoutStep+"      -  run: bash scripts/contract-gate.sh\n         \"i\\u0066\": ${{ false }}\n"+contractGateMakeTest, webCheckout), "unsupported spacing after test step dash"},
		{"escaped continue with extra dash spacing", contractGateWorkflow(contractGateCheckoutStep+"      -  run: bash scripts/contract-gate.sh\n         \"continue-on-erro\\u0072\": true\n"+contractGateMakeTest, webCheckout), "unsupported spacing after test step dash"},
		{"or true", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh || true\n"+contractGateMakeTest, webCheckout), "no step whose run is exactly"},
		{"custom shell skips gate", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash -c true {0}\n"+contractGateMakeTest, webCheckout), "must set shell: bash"},
		{"no explicit shell", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must set shell: bash"},
		{"continued or true", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n          || true\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"nine-space continuation", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n         || true\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"continued custom shell", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash\n          -c true {0}\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"carriage return in comment", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh # gate\r        continue-on-error: true\n"+contractGateMakeTest, webCheckout), "unsupported carriage return"},
		{"no fetch-depth", contractGateWorkflow("      - uses: actions/checkout@abc\n"+contractGateStepText+contractGateMakeTest, webCheckout), "fetch-depth: 0"},
		{"fetch-depth 1", contractGateWorkflow("      - uses: actions/checkout@abc\n        with:\n          fetch-depth: 1\n"+contractGateStepText+contractGateMakeTest, webCheckout), "fetch-depth: 0"},
		{"gate before checkout", contractGateWorkflow(contractGateStepText+contractGateCheckoutStep+contractGateMakeTest, webCheckout), "must come after the checkout"},
		{"gate after make test", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest+contractGateStepText, webCheckout), "must come before make test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contractGateWorkflowProblems(c.workflow)
			for _, p := range got {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %q", c.want, got)
		})
	}
	t.Run("intended snippet passes", func(t *testing.T) {
		w := contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout)
		if got := contractGateWorkflowProblems(w); len(got) != 0 {
			t.Fatalf("want none, got %q", got)
		}
	})
}

// --- Merge rules check (DEC-146): CI must ask GitHub whether master still
// requires up-to-date branches, so two pull requests cannot merge the same
// migration number ---

const mergeRulesStepText = "      - name: Merge rules check\n        run: bash scripts/merge-rules-check.sh\n        shell: bash\n        env:\n          GH_TOKEN: ${{ github.token }}\n"

func TestMergeRulesWorkflowGuardCatchesMistakes(t *testing.T) {
	webCheckout := "      - uses: actions/checkout@abc\n"
	steps := func(merge string) string {
		return contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+merge+contractGateMakeTest, webCheckout)
	}
	cases := []struct {
		name, workflow, want string
	}{
		{"step removed", steps(""), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"step only in the web job", contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout+mergeRulesStepText), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"or true", steps(strings.Replace(mergeRulesStepText, "check.sh\n", "check.sh || true\n", 1)), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"if false", steps(strings.Replace(mergeRulesStepText, "        shell:", "        if: ${{ false }}\n        shell:", 1)), "merge rules check step must not have if:"},
		{"continue-on-error", steps(strings.Replace(mergeRulesStepText, "        shell:", "        continue-on-error: true\n        shell:", 1)), "merge rules check step must not have continue-on-error:"},
		{"no explicit shell", steps(strings.Replace(mergeRulesStepText, "        shell: bash\n", "", 1)), "merge rules check step must set shell: bash"},
		{"continued run", steps(strings.Replace(mergeRulesStepText, "check.sh\n", "check.sh\n          || true\n", 1)), "must not have a multiline continuation"},
		{"no token", steps(strings.Replace(mergeRulesStepText, "        env:\n          GH_TOKEN: ${{ github.token }}\n", "", 1)), "must set GH_TOKEN"},
		{"another token", steps(strings.Replace(mergeRulesStepText, "github.token", "secrets.OTHER", 1)), "must set GH_TOKEN"},
		{"before checkout", contractGateWorkflow(mergeRulesStepText+contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "merge rules check step must come after the checkout"},
		{"conditional job", strings.Replace(steps(mergeRulesStepText), "  test:\n", "  test:\n    if: ${{ false }}\n", 1), "test job must not be conditional"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeRulesWorkflowProblems(c.workflow)
			for _, p := range got {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %q", c.want, got)
		})
	}
	t.Run("intended snippet passes both guards", func(t *testing.T) {
		w := steps(mergeRulesStepText)
		if got := append(mergeRulesWorkflowProblems(w), contractGateWorkflowProblems(w)...); len(got) != 0 {
			t.Fatalf("want none, got %q", got)
		}
	})
}

// mergeRulesFixture is a GET /repos/grierj/fold/rules/branches/master answer
// in the shape GitHub returned on Nov 5, 2026, with strict set as given.
func mergeRulesFixture(strict bool) string {
	return fmt.Sprintf(`[
  {"type": "deletion", "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "non_fast_forward", "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": %t, "do_not_enforce_on_create": true,
    "required_status_checks": [{"context": "test", "integration_id": 15368}, {"context": "web", "integration_id": 15368}, {"context": "provision", "integration_id": 15368}]},
   "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "pull_request", "parameters": {"required_approving_review_count": 0, "allowed_merge_methods": ["merge", "squash", "rebase"]},
   "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073}
]`, strict)
}

// TestMergeRulesCheckScript runs scripts/merge-rules-check.sh on saved
// answers. It never calls GitHub; CI's step checks the live rules.
func TestMergeRulesCheckScript(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required for the merge rules check tests: %v", tool, err)
			}
			t.Skipf("%s is not installed; CI runs this test", tool)
		}
	}
	run := func(t *testing.T, rules string) (int, string) {
		t.Helper()
		f := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(f, []byte(rules), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "scripts/merge-rules-check.sh", "--rules-file", f)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0, string(out)
		case errors.As(err, &exit):
			return exit.ExitCode(), string(out)
		}
		t.Fatalf("running the script: %v", err)
		return 0, ""
	}
	strict := mergeRulesFixture(true)
	cases := []struct {
		name, rules string
		code        int
		want        string
	}{
		{"strict rules pass", strict, 0, "master requires pull requests, up-to-date branches"},
		{"the Nov 5 rules fail", mergeRulesFixture(false), 1, "required status checks are not strict"},
		{"no strict field", strings.Replace(strict, `"strict_required_status_checks_policy": true, `, "", 1), 1, "required status checks are not strict"},
		{"strict as a string", strings.Replace(strict, `"strict_required_status_checks_policy": true`, `"strict_required_status_checks_policy": "true"`, 1), 1, "required status checks are not strict"},
		{"no pull request rule", strings.Replace(strict, `"type": "pull_request"`, `"type": "creation"`, 1), 1, "no pull_request rule"},
		{"no non-fast-forward rule", strings.Replace(strict, `"type": "non_fast_forward"`, `"type": "update"`, 1), 1, "no non_fast_forward rule"},
		{"a required check dropped", strings.Replace(strict, `{"context": "web", "integration_id": 15368}, `, "", 1), 1, "required status check web is not required"},
		{"no rules at all", "[]", 1, "required status checks are not strict"},
		{"an error object", `{"message": "Not Found", "status": "404"}`, 1, "not a list of rules"},
		{"not JSON", "<html>rate limited</html>", 1, "not JSON"},
		{"an empty answer", "", 1, "not JSON"},
		{"a blank answer", " \n", 1, "not JSON"},
		// GitHub combines the rules of every active ruleset: strict in one
		// rule makes every PR up to date before the checks of any rule count.
		{"strict in a second ruleset", strings.Replace(mergeRulesFixture(false), "\n]", `,
  {"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": true, "required_status_checks": []}, "ruleset_id": 2}
]`, 1), 0, "up-to-date branches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := run(t, c.rules)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, output %q; want exit %d and output containing %q", code, out, c.code, c.want)
			}
		})
	}
	t.Run("bad arguments", func(t *testing.T) {
		out, err := exec.Command("bash", "scripts/merge-rules-check.sh", "--rules", "x").CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "usage:") {
			t.Fatalf("got %v %q, want exit 2 with usage", err, out)
		}
	})
}

// contractGateEnv is a git environment that reads nothing from the machine:
// empty global and system config, a throwaway HOME, no inherited GIT_*
// variables (a hook may set GIT_DIR), a fixed identity and a ceiling so a
// directory outside any repository is not found inside one above it.
func contractGateEnv(t *testing.T, ceiling string) []string {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+t.TempDir(),
		"GIT_CONFIG_GLOBAL="+empty,
		"GIT_CONFIG_SYSTEM="+empty,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Gate Test", "GIT_AUTHOR_EMAIL=gate@example.invalid",
		"GIT_COMMITTER_NAME=Gate Test", "GIT_COMMITTER_EMAIL=gate@example.invalid",
		"GIT_CEILING_DIRECTORIES="+ceiling,
	)
}

type contractGateRepo struct {
	t      *testing.T
	dir    string
	env    []string
	script string
}

// contractGateFixtureFiles selects the real expand history, even when the
// source tree already contains later contract migrations.
func contractGateFixtureFiles(source string) []string {
	return []string{filepath.Join(source, "00001_baseline.sql"), filepath.Join(source, "00002_suppression_address_hash.sql")}
}

func TestContractGateFixtureIgnoresLaterContracts(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"00001_baseline.sql", "00002_suppression_address_hash.sql", "00003_real_contract.sql"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := contractGateFixtureFiles(source)
	if len(files) != 2 || filepath.Base(files[0]) != "00001_baseline.sql" || filepath.Base(files[1]) != "00002_suppression_address_hash.sql" {
		t.Fatalf("unexpected expand-era fixture files: %v", files)
	}
}

func newContractGateRepo(t *testing.T) *contractGateRepo {
	t.Helper()
	script, err := filepath.Abs("scripts/contract-gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &contractGateRepo{t: t, dir: dir, env: contractGateEnv(t, filepath.Dir(dir)), script: script}
	r.git(r.dir, "-c", "init.defaultBranch=master", "init", "-q")
	return r
}

func (r *contractGateRepo) git(dir string, args ...string) string {
	r.t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *contractGateRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *contractGateRepo) commit(msg string) {
	r.t.Helper()
	r.git(r.dir, "add", "-A")
	r.git(r.dir, "commit", "-q", "-m", msg)
}

// gate runs the script in dir and returns its exit code and combined output.
func (r *contractGateRepo) gate(dir string) (int, string) {
	r.t.Helper()
	cmd := exec.Command("bash", r.script)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		r.t.Fatalf("run contract-gate.sh: %v", err)
		return -1, ""
	}
}

func (r *contractGateRepo) expect(dir string, wantCode int, wantOut ...string) string {
	r.t.Helper()
	code, out := r.gate(dir)
	if code != wantCode {
		r.t.Fatalf("exit %d, want %d\n%s", code, wantCode, out)
	}
	for _, w := range wantOut {
		if !strings.Contains(out, w) {
			r.t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
	return out
}

const (
	contractGateBaseSQL   = "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	contractGateExpandSQL = "-- +goose Up\n-- Expand step.\nSELECT 2;\n-- +goose Down\nSELECT 2;\n"
)

func contractGateContractSQL(header string) string {
	return "-- +goose Up\n" + header + "\nSELECT 3;\n-- +goose Down\nSELECT 3;\n"
}

const contractGateHeader = "-- contracts: 00002_expand.sql"

// contractGateTagged is the passing shape: commit 1 holds the base and the
// expand and carries v0.0.1; commit 2 adds the contract.
func contractGateTagged(t *testing.T, header string) *contractGateRepo {
	r := newContractGateRepo(t)
	r.write("migrations/00001_base.sql", contractGateBaseSQL)
	r.write("migrations/00002_expand.sql", contractGateExpandSQL)
	r.commit("expand")
	r.git(r.dir, "tag", "v0.0.1")
	r.write("migrations/00003_contract.sql", contractGateContractSQL(header))
	r.commit("contract")
	return r
}

func TestContractGateScript(t *testing.T) {
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required for the contract gate tests: %v", tool, err)
		}
	}
	const noContracts = "contract-gate: no contract migrations"
	const noTag = "contract-gate: 00003_contract.sql: no earlier v* tag contains 00002_expand.sql without the contract"

	t.Run("no migrations directory passes", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("README.md", "x\n")
		r.commit("one")
		r.expect(r.dir, 0, noContracts)
	})
	t.Run("a file with no header is ignored", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/notes.sql", "-- contracts: not a migration name\n")
		r.commit("one")
		r.expect(r.dir, 0, noContracts)
	})
	t.Run("no tag fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("all at once")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag that is not v* fails", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.git(r.dir, "tag", "-d", "v0.0.1")
		r.git(r.dir, "tag", "release-1", "HEAD~1")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag without the expand fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.commit("base")
		r.git(r.dir, "tag", "v0.0.1")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.commit("expand")
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("contract")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag holding the expand and the contract fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("all")
		r.git(r.dir, "tag", "v0.0.1")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag on a branch not merged into HEAD fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.commit("base")
		r.git(r.dir, "checkout", "-q", "-b", "side")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.commit("expand on the side")
		r.git(r.dir, "tag", "v0.0.1")
		r.git(r.dir, "checkout", "-q", "master")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("expand and contract")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("an earlier tag with only the expand passes", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		out := r.expect(r.dir, 0,
			"contract-gate: 00003_contract.sql contracts 00002_expand.sql (shipped in v0.0.1)",
			"publication evidence for this tag must be recorded in the PR")
		if strings.Contains(out, noContracts) {
			t.Fatalf("a contract was found, so the no-contract line must not print:\n%s", out)
		}
	})
	t.Run("an annotated tag counts", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.git(r.dir, "tag", "-d", "v0.0.1")
		r.git(r.dir, "tag", "-a", "-m", "release", "v0.1.0", "HEAD~1")
		r.expect(r.dir, 0, "(shipped in v0.1.0)")
	})
	t.Run("an expand edited after its tag fails", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL+"-- edited\n")
		r.commit("edit the expand")
		r.expect(r.dir, 1, "contract-gate: 00002_expand.sql changed after v0.0.1")
	})
	t.Run("a header naming a missing expand fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00002_missing.sql")
		r.expect(r.dir, 1, "00002_missing.sql does not exist")
	})
	t.Run("a header naming itself fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00003_contract.sql")
		r.expect(r.dir, 1, "names itself")
	})
	t.Run("a header naming a higher number fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00004_later.sql")
		r.write("migrations/00004_later.sql", contractGateExpandSQL)
		r.commit("later")
		r.expect(r.dir, 1, "expand 00004_later.sql must have a lower number")
	})
	malformed := map[string]string{
		"missing space":   "--contracts: 00002_expand.sql",
		"capitalized":     "-- Contracts: 00002_expand.sql",
		"no space after":  "-- contracts:00002_expand.sql",
		"not a file name": "--contracts: x.sql",
		"trailing space":  "-- contracts: 00002_expand.sql ",
		"two lines":       contractGateHeader + "\n" + contractGateHeader,
		"two different":   contractGateHeader + "\n-- contracts: 00001_base.sql",
	}
	for name, header := range malformed {
		t.Run("malformed: "+name, func(t *testing.T) {
			r := contractGateTagged(t, header)
			r.expect(r.dir, 1, "contract-gate: 00003_contract.sql: malformed contracts line")
		})
	}
	t.Run("a shallow clone fails even with no contract file", func(t *testing.T) {
		src := newContractGateRepo(t)
		src.write("migrations/00001_base.sql", contractGateBaseSQL)
		src.commit("one")
		src.write("migrations/00002_expand.sql", contractGateExpandSQL)
		src.commit("two")
		dst := filepath.Join(t.TempDir(), "clone")
		src.git(src.dir, "clone", "-q", "--depth", "1", "file://"+src.dir, dst)
		if got := src.git(dst, "rev-parse", "--is-shallow-repository"); got != "true" {
			t.Fatalf("fixture is not shallow: %q", got)
		}
		src.expect(dst, 1, "contract-gate: shallow checkout; fetch full history and tags")
	})
	t.Run("a subdirectory behaves like the root", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		want := r.expect(r.dir, 0, "(shipped in v0.0.1)")
		if got := r.expect(filepath.Join(r.dir, "migrations"), 0); got != want {
			t.Fatalf("subdirectory output differs:\n%s\nvs\n%s", got, want)
		}
		bad := contractGateTagged(t, "--contracts: x.sql")
		bad.expect(filepath.Join(bad.dir, "migrations"), 1, "malformed contracts line")
	})
	t.Run("outside a repository exits 1", func(t *testing.T) {
		r := newContractGateRepo(t)
		plain := filepath.Join(t.TempDir(), "plain")
		if err := os.Mkdir(plain, 0o755); err != nil {
			t.Fatal(err)
		}
		r.env = contractGateEnv(t, filepath.Dir(plain))
		r.expect(plain, 1, "contract-gate: not a git repository")
	})
	t.Run("the real migrations pass with a tag and fail without one", func(t *testing.T) {
		// Copy just the two real files this history needs. Other migrations,
		// including future contracts, cannot be placed in its expand-era tag.
		files := contractGateFixtureFiles("migrations")
		r := newContractGateRepo(t)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			r.write(f, string(b))
		}
		r.commit("migrations as they are")
		r.write("migrations/00003_x.sql", contractGateContractSQL("-- contracts: 00002_suppression_address_hash.sql"))
		r.commit("a contract for the suppression expand")
		r.expect(r.dir, 1, "contract-gate: 00003_x.sql: no earlier v* tag contains 00002_suppression_address_hash.sql without the contract")
		r.git(r.dir, "tag", "v0.0.1", "HEAD~1")
		r.expect(r.dir, 0, "contract-gate: 00003_x.sql contracts 00002_suppression_address_hash.sql (shipped in v0.0.1)")
	})
}

// --- UI ratchets (ENFORCEMENT.md B2-B6) ---
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
		fix:  "use <Button variant=…>, <Chip selected=…> or <Field label=…><Input/></Field> from components/ui (ENFORCEMENT.md A)",
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
			return strings.HasSuffix(path, "lib/when.ts") || strings.HasSuffix(path, "lib/wall.ts")
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

// --- One list filter for event visibility ---
//
// Series lists go through eventCandidates; date lists load dateCandidates then
// apply visibleSet.canSeeDate. Candidate parents never authorize a series.
// Both paths skip missing groups; policy loops elsewhere duplicate the filter.

func TestOneEventVisibilityFilter(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "eventCandidates" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch l := n.(type) {
				case *ast.RangeStmt:
					body = l.Body
				case *ast.ForStmt:
					body = l.Body
				default:
					return true
				}
				ast.Inspect(body, func(m ast.Node) bool {
					if call, ok := m.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CanSeeEvent" {
							t.Errorf("%s: %s filters a list with policy.CanSeeEvent; use eventCandidates for series, or dateCandidates and canSeeDate for dates", fset.Position(call.Pos()), fn.Name.Name)
						}
					}
					return true
				})
				return false
			})
		}
	}
}

// #95: CI actions are pinned to full commit SHAs (a moved tag can't run new
// code), and the workflow token is read-only.
func TestWorkflowActionsPinnedAndReadOnly(t *testing.T) {
	files, err := filepath.Glob(".github/workflows/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("workflows: %v %v", files, err)
	}
	pinned := regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*[^@\s]+@[0-9a-f]{40}(?:\s+#\s*\S+)?\s*$`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^permissions:\s*\n\s+contents:\s*read\s*$`).Match(src) {
			t.Errorf("%s: no top-level `permissions: contents: read`", f)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "uses:") && !strings.Contains(line, "uses: ./") && !pinned.MatchString(line) {
				t.Errorf("%s:%d: action not pinned to a full SHA: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Registration subjects must pass through the mixed holder scope; membership
// joins remain account-only.
func TestRegistrationQueriesUseSubjectScope(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), `listedUsers("registrations")`) {
			t.Errorf("%s uses the account-only registration join", file)
		}
	}
}

// R6 (M7.5): notify.Send writes a durable delivery intent in the caller's
// transaction and the notify.deliver job it queues sends the email and the
// push. No caller delivers a notice itself after commit.
var legacyNoticeDeliveryNames = map[string]bool{
	"deliverNotificationEmails": true,
	"deliverNotificationEmail":  true,
	"recordDelivery":            true,
	"queuePushDeliveries":       true,
}

// mailerSendFiles may deliver application mail, each with its reason. The
// synchronous account exceptions below are methods, never whole-file grants.
var mailerSendFiles = map[string]string{
	"notify_deliver.go":      "the notify.deliver job sends notice mail",
	"mail_jobs.go":           "direct account mail is sent by durable jobs after preparation commits",
	"messages_send.go":       "announcement mail goes through its own send path",
	"api_message_preview.go": "the composer's test send is synchronous by design (brief section 4)",
}

var synchronousMailMethods = map[string]struct{ method, reason string }{
	"api_profile.go": {"sendEmailChangeNotice", "the old address and cancellation capability cannot be rebuilt after verification"},
	"api_guest.go":   {"sendGuestConfirmationEmail", "the browser receives the same manage capability; delayed rotation would invalidate it"},
}

func mailSendAllowed(file string, parents []ast.Node) bool {
	if _, ok := mailerSendFiles[file]; ok {
		return true
	}
	exception, ok := synchronousMailMethods[file]
	if !ok || !immediateMailCall(parents) {
		return false
	}
	for i := len(parents) - 1; i >= 0; i-- {
		if fn, ok := parents[i].(*ast.FuncDecl); ok {
			if fn.Name.Name != exception.method || fn.Recv == nil || len(fn.Recv.List) != 1 {
				return false
			}
			recv := fn.Recv.List[0].Type
			if ptr, ok := recv.(*ast.StarExpr); ok {
				recv = ptr.X
			}
			name, ok := recv.(*ast.Ident)
			return ok && name.Name == "App"
		}
	}
	return false
}

func immediateMailCall(parents []ast.Node) bool {
	// A synchronous exception authorizes an immediate call, not a method value
	// that could escape to another function or be invoked with go/defer later.
	i := len(parents) - 2
	for i >= 0 {
		if _, ok := parents[i].(*ast.ParenExpr); !ok {
			break
		}
		i--
	}
	if i < 0 {
		return false
	}
	call, ok := parents[i].(*ast.CallExpr)
	if !ok || call.Fun != parents[i+1] {
		return false
	}
	for i := len(parents) - 1; i >= 0; i-- {
		switch parents[i].(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.FuncLit:
			// Also keep exception-method calls out of transaction closures.
			return false
		}
	}
	return true
}

type mailSource uint8

const (
	mailSenderSource mailSource = 1 << iota
	mailUnwrapSource
	mailFactorySource
)

// mailSources follows parsed bindings, mail sender types, constructors and
// result positions, and package variables across files in the same directory.
// Keep sources once seen so a later assignment cannot hide a possible send.
// Field and method names are conservative matches, not resolved Go types. This
// is not interprocedural analysis: arbitrary helper bodies, type aliases and
// mail hidden behind untyped interfaces or containers still require review.
func mailSources(trees ...*ast.File) func(ast.Expr) mailSource {
	mailNames := map[*ast.Ident]bool{}
	dotNames := map[*ast.Ident]bool{}
	globals := map[string]*ast.Ident{}
	fields := map[string]mailSource{}
	results := map[string][]mailSource{}
	isSenderType := func(name string) bool {
		switch name {
		case "Sender", "Adapter", "SMTPSender", "Postmark", "DevOutbox", "Suppressing", "NoSender":
			return true
		}
		return false
	}
	packageSource := func(name string) mailSource {
		if name == "Unwrap" {
			return mailUnwrapSource
		}
		if strings.HasPrefix(name, "New") || name == "WithSuppressions" {
			return mailFactorySource
		}
		return 0
	}
	for _, tree := range trees {
		imports := map[string]bool{}
		for _, imp := range tree.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err == nil && path == "fold/internal/mail" {
				name := "mail"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = true
			}
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Obj == nil {
				mailNames[id] = imports[id.Name]
				dotNames[id] = imports["."]
			}
			return true
		})
		for _, decl := range tree.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.VAR {
				for _, spec := range d.Specs {
					for _, id := range spec.(*ast.ValueSpec).Names {
						globals[id.Name] = id
					}
				}
			}
		}
	}
	typedSource := func(expr ast.Expr) mailSource {
		for {
			switch x := expr.(type) {
			case *ast.StarExpr:
				expr = x.X
			case *ast.ParenExpr:
				expr = x.X
			case *ast.Ident:
				if dotNames[x] && isSenderType(x.Name) {
					return mailSenderSource
				}
				return 0
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && mailNames[pkg] && isSenderType(x.Sel.Name) {
					return mailSenderSource
				}
				return 0
			default:
				return 0
			}
		}
	}
	//lint:ignore SA1019 Parse-only guard uses parser-local bindings, not type identity; unresolved flows are documented above and in AGENTS.md.
	bindings := map[*ast.Object]mailSource{}
	for _, tree := range trees {
		ast.Inspect(tree, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.Field:
				for _, id := range x.Names {
					if id.Obj != nil {
						bindings[id.Obj] |= typedSource(x.Type)
					}
				}
			case *ast.StructType:
				for _, field := range x.Fields.List {
					for _, id := range field.Names {
						fields[id.Name] |= typedSource(field.Type)
					}
				}
			case *ast.ValueSpec:
				for _, id := range x.Names {
					if id.Obj != nil {
						bindings[id.Obj] |= typedSource(x.Type)
					}
				}
			case *ast.FuncDecl:
				if x.Type.Results != nil {
					var signature []mailSource
					for _, field := range x.Type.Results.List {
						for range max(1, len(field.Names)) {
							signature = append(signature, typedSource(field.Type))
						}
					}
					old := results[x.Name.Name]
					for len(old) < len(signature) {
						old = append(old, 0)
					}
					for i, value := range signature {
						old[i] |= value
					}
					results[x.Name.Name] = old
				}
			}
			return true
		})
	}
	var source func(ast.Expr) mailSource
	callResults := func(expr ast.Expr) []mailSource {
		for {
			if paren, ok := expr.(*ast.ParenExpr); ok {
				expr = paren.X
			} else {
				break
			}
		}
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return nil
		}
		if source(call.Fun)&(mailUnwrapSource|mailFactorySource) != 0 {
			return []mailSource{mailSenderSource}
		}
		fn := call.Fun
		for {
			if paren, ok := fn.(*ast.ParenExpr); ok {
				fn = paren.X
			} else {
				break
			}
		}
		switch fn := fn.(type) {
		case *ast.Ident:
			if fn.Obj != nil {
				if _, ok := fn.Obj.Decl.(*ast.FuncDecl); !ok {
					return nil
				}
			}
			return results[fn.Name]
		case *ast.SelectorExpr:
			return results[fn.Sel.Name]
		}
		return nil
	}
	source = func(expr ast.Expr) mailSource {
		switch x := expr.(type) {
		case *ast.Ident:
			if x.Obj != nil {
				return bindings[x.Obj]
			}
			if id := globals[x.Name]; id != nil && id.Obj != nil {
				return bindings[id.Obj]
			}
			if dotNames[x] {
				return packageSource(x.Name)
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "Mailer" {
				return mailSenderSource
			}
			if pkg, ok := x.X.(*ast.Ident); ok && mailNames[pkg] {
				return packageSource(x.Sel.Name)
			}
			return fields[x.Sel.Name]
		case *ast.CallExpr:
			if values := callResults(x); len(values) > 0 {
				return values[0]
			}
		case *ast.CompositeLit:
			return typedSource(x.Type)
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				return source(x.X)
			}
		case *ast.TypeAssertExpr:
			return source(x.X) | typedSource(x.Type)
		case *ast.ParenExpr:
			return source(x.X)
		}
		return 0
	}
	for changed := true; changed; {
		changed = false
		bind := func(lhs ast.Expr, value mailSource) {
			switch x := lhs.(type) {
			case *ast.Ident:
				id := x
				if id.Obj == nil {
					id = globals[x.Name]
				}
				if id != nil && id.Obj != nil {
					old := bindings[id.Obj]
					bindings[id.Obj] |= value
					changed = changed || old != bindings[id.Obj]
				}
			case *ast.SelectorExpr:
				old := fields[x.Sel.Name]
				fields[x.Sel.Name] |= value
				changed = changed || old != fields[x.Sel.Name]
			}
		}
		bindValues := func(lhs, rhs []ast.Expr) {
			if len(rhs) == 1 && len(lhs) > 1 {
				if values := callResults(rhs[0]); len(values) > 0 {
					for i, value := range values {
						if i < len(lhs) {
							bind(lhs[i], value)
						}
					}
					return
				}
			}
			for i, value := range rhs {
				if i < len(lhs) {
					bind(lhs[i], source(value))
				}
			}
		}
		for _, tree := range trees {
			ast.Inspect(tree, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.AssignStmt:
					bindValues(x.Lhs, x.Rhs)
				case *ast.ValueSpec:
					var lhs []ast.Expr
					for _, id := range x.Names {
						lhs = append(lhs, id)
					}
					bindValues(lhs, x.Values)
				}
				return true
			})
		}
	}
	return source
}

// Legacy delivery and runInBackground identifiers fail in all production
// packages. Only internal/mail owns sender implementations. Parsing ignores
// comments and quoted examples.
func noticeDeliveryViolations(file, src string, checkMailer bool) []string {
	positions := token.NewFileSet()
	tree, err := parser.ParseFile(positions, file, src, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse production source: %v", file, err)}
	}
	source := mailSources(tree)
	return parsedMailViolations(file, positions, tree, source, checkMailer)
}

func parsedMailViolations(file string, positions *token.FileSet, tree *ast.File, source func(ast.Expr) mailSource, checkMailer bool) []string {
	var bad []string
	var parents []ast.Node
	ast.Inspect(tree, func(node ast.Node) bool {
		if node == nil {
			parents = parents[:len(parents)-1]
			return true
		}
		parents = append(parents, node)
		at := positions.Position(node.Pos()).String()
		if id, ok := node.(*ast.Ident); ok {
			if legacyNoticeDeliveryNames[id.Name] {
				bad = append(bad, at+" (write the notice with notify.Send; delivery is automatic (R6))")
			}
			if id.Name == "runInBackground" {
				bad = append(bad, at+" (queue durable work with jobs.Enqueue; untracked background mail is forbidden)")
			}
		}
		if sel, ok := node.(*ast.SelectorExpr); ok && checkMailer {
			// A named exception must stay synchronous at its call site too,
			// even when the caller is otherwise allowed to deliver mail.
			for _, exception := range synchronousMailMethods {
				if sel.Sel.Name == exception.method && !immediateMailCall(parents) {
					bad = append(bad, at+" (call synchronous mail methods immediately after commit; never use go, defer, closures or escaping method values)")
				}
			}
			if !mailSendAllowed(file, parents) && (sel.Sel.Name == "SendEmail" || (sel.Sel.Name == "Send" && source(sel.X)&mailSenderSource != 0)) {
				bad = append(bad, at+" (mail a notice with notify.Send or enqueue an ID-based direct-mail job; synchronous sends need a named exception)")
			}
		}
		return true
	})
	return bad
}

// Scan every production Go directory, regardless of nesting. Test fixtures,
// frontend dependencies, vendored code and repository metadata are not shipped
// Go application source. Keep this file discovery shared by every guard that
// reads production source (the mail guards and the suppression SQL guard).
func walkProductionGo(root string, visit func(path string, src []byte) error) error {
	return fs.WalkDir(os.DirFS(root), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." && (strings.HasPrefix(entry.Name(), ".") || path == "web" || entry.Name() == "vendor" || entry.Name() == "testdata" || entry.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		return visit(path, src)
	})
}

func productionMailViolations(root string, checkMailer bool) ([]string, error) {
	positions := token.NewFileSet()
	packages := map[string][]*ast.File{}
	err := walkProductionGo(root, func(path string, src []byte) error {
		tree, err := parser.ParseFile(positions, path, src, 0)
		if err != nil {
			return err
		}
		key := filepath.Dir(path) + ":" + tree.Name.Name
		packages[key] = append(packages[key], tree)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, trees := range packages {
		source := mailSources(trees...)
		for _, tree := range trees {
			file := positions.Position(tree.Pos()).Filename
			mailer := checkMailer && filepath.Dir(file) != "internal/mail"
			bad = append(bad, parsedMailViolations(file, positions, tree, source, mailer)...)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

func TestNoticesAreDeliveredBySendNotByCallers(t *testing.T) {
	bad, err := productionMailViolations(".", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range bad {
		t.Error(v)
	}
}

func TestMailGoesThroughJobs(t *testing.T) {
	bad, err := productionMailViolations(".", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range bad {
		t.Error(v)
	}
	for f := range mailerSendFiles {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("mailerSendFiles names %s, which does not exist: %v", f, err)
		}
	}
}

// These are complete Go source files in real package directories, not fragments
// tested with a manually selected checkMailer flag. Exercise discovery and the
// same-package resolver together, including the sender-implementation exclusion.
func TestMailGuardScansProductionPackages(t *testing.T) {
	for _, checkMailer := range []bool{false, true} {
		bad, err := productionMailViolations("testdata/mailguard", checkMailer)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"migrations/migration.go"}
		if checkMailer {
			want = append(want, "models/model.go", "storage/notify_deliver.go", "migrations/migration.go", "internal/deep/package/deliver.go", "internal/deep/package/deliver.go")
		}
		var got []string
		for _, v := range bad {
			file, _, _ := strings.Cut(v, ":")
			got = append(got, file)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("checkMailer=%v: got %v, want %v; violations: %v", checkMailer, got, want, bad)
		}
	}
}

func TestMailGuardSkipsFixturesButNotProductionPackages(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"models/model.go", "storage/store.go", "migrations/migration.go", "internal/one/two/three/worker.go", "internal/mail/legacy.go", "internal/web/worker.go",
		"example_test.go", "testdata/example.go", "web/node_modules/example/example.go", "vendor/example/example.go", ".git/example.go",
	} {
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("package example\nfunc runInBackground() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := productionMailViolations(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 6 {
		t.Fatalf("all six production directories must reject the identifier, including internal/mail and internal/web: %v", bad)
	}
}

// The scan catches a handler that delivers a notice after commit, and a
// direct mailer call from a file with no reason to hold one.
func TestNoticeGuardCatchesPostCommitDelivery(t *testing.T) {
	for name := range legacyNoticeDeliveryNames {
		post := "package main\nfunc example() { ids, err := notifyThing(tx); a." + name + "(ids) }\n"
		if bad := noticeDeliveryViolations("api_example.go", post, false); len(bad) != 1 || !strings.Contains(bad[0], "delivery is automatic") {
			t.Fatalf("guard missed legacy %s: %v", name, bad)
		}
	}
	mailer := "package main\nfunc example() { err := a.Mailer.Send(ctx, msg) }\n"
	if len(noticeDeliveryViolations("api_example.go", mailer, true)) != 1 {
		t.Fatal("guard missed a mailer call from a file outside the allow-list")
	}
	if len(noticeDeliveryViolations("notify_deliver.go", mailer, true)) != 0 {
		t.Fatal("guard flagged an allow-listed file")
	}
}

func TestMailGuardCatchesBypasses(t *testing.T) {
	for _, tc := range []struct {
		name, imports, body string
	}{
		{"direct", "", "a.Mailer.Send(ctx, msg)"},
		{"multiline direct", "", "a.\nMailer.\nSend (ctx, msg)"},
		{"assigned mailer", "", "sender := a.Mailer; sender.Send(ctx, msg)"},
		{"tracked", "", "sender.SendEmail(ctx, msg)"},
		{"direct unwrap", `import "fold/internal/mail"`, "mail.Unwrap(a.Mailer).Send(ctx, msg)"},
		{"aliased import", `import delivery "fold/internal/mail"`, "delivery.Unwrap(a.Mailer).Send(ctx, msg)"},
		{"assigned unwrap", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"aliased assigned unwrap", `import delivery "fold/internal/mail"`, "sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"sender alias", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); alias := sender; alias.Send(ctx, msg)"},
		{"var sender", `import "fold/internal/mail"`, "var sender = mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"reassigned sender", `import "fold/internal/mail"`, "var sender Sender; sender = mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"asserted sender", `import "fold/internal/mail"`, "sender, ok := mail.Unwrap(a.Mailer).(*mail.DevOutbox); sender.Send(ctx, msg)"},
		{"parenthesized", `import "fold/internal/mail"`, "sender := (mail.Unwrap(a.Mailer)); (sender).Send(ctx, msg)"},
		{"assigned unwrap function", `import delivery "fold/internal/mail"`, "unwrap := delivery.Unwrap; sender := unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"dot import", `import . "fold/internal/mail"`, "sender := Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"method value", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); send := sender.Send; send(ctx, msg)"},
		{"background call", "", "a.runInBackground(func() {})"},
		{"background alias", "", "background := a.runInBackground; background(f)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\n" + tc.imports + "\nfunc (a *App) example() { " + tc.body + " }\n"
			bad := noticeDeliveryViolations("api_example.go", src, true)
			if len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
				t.Fatalf("guard must reject the send or background identifier: %v", bad)
			}
		})
	}
	background := "package main\nfunc (a *App) runInBackground(f func()) { go f() }\n"
	if bad := noticeDeliveryViolations("internal/example/example.go", background, false); len(bad) != 1 || !strings.Contains(bad[0], "untracked background mail") {
		t.Fatalf("guard must reject reintroducing the helper in any production package: %v", bad)
	}
}

func TestMailGuardFollowsConcreteSendersAndConstructors(t *testing.T) {
	for _, senderType := range []string{"Sender", "Adapter", "SMTPSender", "Postmark", "DevOutbox", "Suppressing", "NoSender"} {
		for _, ptr := range []string{"", "*"} {
			for _, declaration := range []string{
				"func example(sender " + ptr + "delivery." + senderType + ") { sender.Send(ctx, msg) }",
				"func example() { var sender " + ptr + "delivery." + senderType + "; sender.Send(ctx, msg) }",
				"type holder struct { sender " + ptr + "delivery." + senderType + " }; func example(h holder) { h.sender.Send(ctx, msg) }",
			} {
				src := "package main\nimport delivery \"fold/internal/mail\"\n" + declaration
				if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
					t.Fatalf("guard missed typed sender in %s: %v", declaration, bad)
				}
			}
		}
	}
	for _, body := range []string{
		"s, _ := delivery.NewSMTP(url, from); s.Send(ctx, msg)",
		"s, err := delivery.NewPostmark(token, from, transactional, broadcast); s.Send(ctx, msg)",
		"delivery.NewDevOutbox(db, from).Send(ctx, msg)",
		"delivery.WithSuppressions(db, inner).Send(ctx, msg)",
		"s := delivery.WithSuppressions(db, inner); s.Send(ctx, msg)",
		"delivery.NoSender{}.Send(ctx, msg)",
		"(&delivery.Postmark{}).Send(ctx, msg)",
		"s := &delivery.SMTPSender{}; s.Send(ctx, msg)",
		"s := delivery.DevOutbox{}; s.Send(ctx, msg)",
		"s := delivery.Suppressing{}; send := s.Send; consume(send)",
		"build := delivery.NewSMTP; s, err := build(url, from); s.Send(ctx, msg)",
		"wrap := delivery.WithSuppressions; s := wrap(db, inner); s.Send(ctx, msg)",
		"var s = delivery.NewDevOutbox(db, from); s.Send(ctx, msg)",
		"var s, err = delivery.NewSMTP(url, from); s.Send(ctx, msg)",
		"s := unknown.(*delivery.Postmark); s.Send(ctx, msg)",
	} {
		src := "package main\nimport delivery \"fold/internal/mail\"\nfunc example() { " + body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
			t.Fatalf("guard missed concrete sender or constructor in %s: %v", body, bad)
		}
	}
	for _, body := range []string{
		"var s *Postmark; s.Send(ctx, msg)",
		"var s Adapter; s.Send(ctx, msg)",
		"NoSender{}.Send(ctx, msg)",
		"s, _ := NewSMTP(url, from); s.Send(ctx, msg)",
		"WithSuppressions(db, inner).Send(ctx, msg)",
	} {
		src := "package main\nimport . \"fold/internal/mail\"\nfunc example() { " + body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
			t.Fatalf("guard missed dot-imported sender in %s: %v", body, bad)
		}
	}
}

func TestMailGuardFollowsMultipleResultPositions(t *testing.T) {
	for _, tc := range []struct{ declaration, body string }{
		{"func mailer() (delivery.Sender, error) { return nil, nil }", "s, err := mailer(); s.Send(ctx, msg)"},
		{"func (a *App) mailer() (delivery.Sender, error) { return nil, nil }", "s, err := a.mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, *delivery.Postmark) { return nil, nil }", "err, s := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (int, delivery.Adapter, error) { return 0, nil, nil }", "_, s, err := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (first, second delivery.Sender, err error) { return }", "_, s, err := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, delivery.Sender) { return nil, nil }", "var err, s = mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, delivery.Sender) { return nil, nil }", "var s any; var err error; err, s = mailer(); s.Send(ctx, msg)"},
		{"func mailer() (delivery.Sender, error) { return nil, nil }", "var s, err = (mailer)(); s.Send(ctx, msg)"},
	} {
		src := "package main\nimport delivery \"fold/internal/mail\"\n" + tc.declaration + "\nfunc example() { " + tc.body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
			t.Fatalf("guard missed sender result in %s with %s: %v", tc.declaration, tc.body, bad)
		}
	}
	// A multi-result helper's error and other positions are not mail senders.
	src := `package main
import "fold/internal/mail"
func mailer() (other, mail.Sender, error) { return other{}, nil, nil }
func example() {
	x, _, err := mailer()
	x.Send(ctx, msg)
	err.Send(ctx, msg)
	var y, _, e = mailer()
	y.Send(ctx, msg)
	e.Send(ctx, msg)
}
`
	if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 0 {
		t.Fatalf("guard treated a non-sender result as mail: %v", bad)
	}
}

func TestMailGuardSynchronousMethodsStayImmediate(t *testing.T) {
	for file, exception := range synchronousMailMethods {
		call := "a." + exception.method + "(args)"
		for _, body := range []string{
			"go " + call,
			"defer " + call,
			"go func() { " + call + " }()",
			"func() { " + call + " }()",
			"db.Transaction(func(tx *gorm.DB) error { return " + call + " })",
			"send := a." + exception.method + "; go send(args)",
			"consume(a." + exception.method + ")",
		} {
			src := "package main\nfunc (a *App) handler() { " + body + " }\n"
			// Whole-file delivery allowances must not permit these methods to
			// escape either. Their provider call needs a waiting HTTP handler.
			for _, callerFile := range []string{file, "api_example.go", "mail_jobs.go", "notify_deliver.go"} {
				if bad := noticeDeliveryViolations(callerFile, src, true); len(bad) != 1 || !strings.Contains(bad[0], "call synchronous mail methods immediately after commit") {
					t.Fatalf("%s allowed %s: %v", callerFile, body, bad)
				}
			}
		}
		for _, body := range []string{
			call,
			"if err := " + call + "; err != nil { log(err) }",
			"(a." + exception.method + ")(args)",
			"err := db.Transaction(func(tx *gorm.DB) error { return save(tx) }); if err != nil { return }; " + call,
		} {
			src := "package main\nfunc (a *App) handler() { " + body + " }\n"
			if bad := noticeDeliveryViolations(file, src, true); len(bad) != 0 {
				t.Fatalf("%s rejected immediate post-commit call %s: %v", file, body, bad)
			}
		}
	}
}

func TestMailGuardScopesSynchronousExceptions(t *testing.T) {
	for file, exception := range synchronousMailMethods {
		t.Run(file, func(t *testing.T) {
			if exception.reason == "" {
				t.Fatal("synchronous exceptions must name their reason")
			}
			for _, send := range []string{
				"a.Mailer.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg)",
				"sender.SendEmail(ctx, msg)",
			} {
				for _, fn := range []struct {
					decl string
					want int
				}{
					{"func (a *App) " + exception.method + "()", 0},
					{"func (app *App) " + exception.method + "()", 0},
					{"func (a *App) unrelated()", 1},
					{"func " + exception.method + "()", 1},
					{"func (a *Other) " + exception.method + "()", 1},
				} {
					src := "package main\nimport delivery \"fold/internal/mail\"\n" + fn.decl + " { " + send + " }\n"
					if bad := noticeDeliveryViolations(file, src, true); len(bad) != fn.want {
						t.Fatalf("%s with %s: %v, want %d violations", fn.decl, send, bad, fn.want)
					}
					if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 {
						t.Fatalf("method name granted permission in another file: %v", bad)
					}
				}
			}
			for _, body := range []string{
				"go func() { a.Mailer.Send(ctx, msg) }()",
				"go a.Mailer.Send(ctx, msg)",
				"defer a.Mailer.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); go sender.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); defer sender.Send(ctx, msg)",
				"send := a.Mailer.Send; go send(ctx, msg)",
				"send := a.Mailer.Send; defer send(ctx, msg)",
				"consume(a.Mailer.Send)",
			} {
				src := "package main\nimport delivery \"fold/internal/mail\"\nfunc (a *App) " + exception.method + "() { " + body + " }\n"
				if bad := noticeDeliveryViolations(file, src, true); len(bad) != 1 || strings.Contains(bad[0], "parse production source") {
					t.Fatalf("guard granted the synchronous exception to %s: %v", body, bad)
				}
			}
		})
	}
}

func TestMailGuardAllowsDeliveryAndReadOnlyOutbox(t *testing.T) {
	send := "package main\nimport delivery \"fold/internal/mail\"\nfunc example() { a.Mailer.Send(ctx, msg); sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg); sender.SendEmail(ctx, msg) }\n"
	for file, reason := range mailerSendFiles {
		if reason == "" {
			t.Fatalf("%s needs its delivery reason", file)
		}
		if bad := noticeDeliveryViolations(file, send, true); len(bad) != 0 {
			t.Errorf("%s delivery rejected: %v", file, bad)
		}
	}
	read := `package main
import delivery "fold/internal/mail"
func (a *App) devOutbox() {
	outbox, ok := delivery.Unwrap(a.Mailer).(*delivery.DevOutbox)
	outbox.List(100)
	outbox.HTMLOf(1)
}
`
	if bad := noticeDeliveryViolations("api_dev.go", read, true); len(bad) != 0 {
		t.Fatalf("read-only unwrap rejected: %v", bad)
	}
	readAndSend := strings.Replace(read, "outbox.List(100)", "outbox.List(100); outbox.Send(ctx, msg)", 1)
	if bad := noticeDeliveryViolations("api_dev.go", readAndSend, true); len(bad) != 1 {
		t.Fatalf("dev outbox was granted permission to send: %v", bad)
	}
	// Comments, strings, unrelated sends and shadowed bindings are not mail.
	other := `package main
import "fold/internal/mail"
func example() {
	// a.Mailer.Send(ctx, msg); runInBackground
	text := "deliverNotificationEmails and sender.SendEmail(ctx, msg)"
	sender := mail.Unwrap(a.Mailer)
	{ sender := other; sender.Send(ctx, msg) }
	{ mail := other; sender := mail.Unwrap(other); sender.Send(ctx, msg) }
}
`
	if bad := noticeDeliveryViolations("api_example.go", other, true); len(bad) != 0 {
		t.Fatalf("guard confused examples or unrelated bindings with mail: %v", bad)
	}
	// Provider implementations still own their Send/SendEmail methods.
	if bad := noticeDeliveryViolations("internal/mail/sender.go", send, false); len(bad) != 0 {
		t.Fatalf("guard rejected sender implementation calls: %v", bad)
	}
}

// R4-S4: suppression SQL lives in four places. The suppressions table holds
// an address (plaintext until R4-SC) and its hash, and a reader must look at
// the hash first and the address only on a NULL-hash row. A second reader that
// compares `address = ...` or `lower(btrim(...))` again would miss hashed rows
// the day R4-SC drops the plaintext column. So production source may name the
// table, or models.Suppression, only inside these helpers, apart from literal
// erasure inventory metadata; everything else goes through mail.OpenSuppressionReasons.
//
// Parse-only, the same stance as the mail guard. Known limits: table names
// built from non-literal pieces, reflection, SQL sent around GORM, dot-imports
// of fold/models and cross-package helpers are not seen. The unwanted shapes
// the guard does see are covered by TestSuppressionSQLGuardCatchesBypasses.
var suppressionSQLAllowed = map[string][]string{
	"internal/mail/suppress.go": {"OpenSuppressionReasons", "openSuppressionsSQL"},
	"api_admin_suppressions.go": {"openSuppressions", "apiAdminClearSuppression"},
	"api_email_webhooks.go":     {"applyEmailEvent"},
	"models/models.go":          {"Suppression", "All"},
}

var suppressionTableRe = regexp.MustCompile(`\b(from|join|update|into|table)\s+(public\.)?suppressions\b`)

// erasureInventoryTableLiterals identifies only the Table fields of the root
// inventory's data-only initializer. Never exempt a whole file or declaration:
// SQL in another field, an executable initializer or a sibling declaration
// must still pass through the SQL guards. Inventory coverage stays unchanged.
func erasureInventoryTableLiterals(path string, tree *ast.File) map[*ast.BasicLit]bool {
	tables := map[*ast.BasicLit]bool{}
	if path != "erasure_inventory.go" {
		return tables
	}
	stringLiteral := func(expr ast.Expr) *ast.BasicLit {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return nil
		}
		return lit
	}
	for _, decl := range tree.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "erasureColumns" || len(value.Values) != 1 {
				continue
			}
			list, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			array, ok := list.Type.(*ast.ArrayType)
			if !ok || array.Len != nil {
				continue
			}
			elem, ok := array.Elt.(*ast.Ident)
			if !ok || elem.Name != "erasureColumn" {
				continue
			}
			for _, expr := range list.Elts {
				row, ok := expr.(*ast.CompositeLit)
				if !ok || row.Type != nil || len(row.Elts) != 5 {
					continue
				}
				plain := true
				for _, field := range row.Elts[:4] {
					plain = plain && stringLiteral(field) != nil
				}
				rules, ok := row.Elts[4].(*ast.CompositeLit)
				if !plain || !ok {
					continue
				}
				ruleType, ok := rules.Type.(*ast.ArrayType)
				if !ok || ruleType.Len != nil {
					continue
				}
				ruleElem, ok := ruleType.Elt.(*ast.Ident)
				if !ok || ruleElem.Name != "string" {
					continue
				}
				for _, rule := range rules.Elts {
					plain = plain && stringLiteral(rule) != nil
				}
				if plain {
					table := stringLiteral(row.Elts[0])
					name, err := strconv.Unquote(table.Value)
					if err == nil && (name == "login_throttles" || name == "suppressions" || name == "email_address_optouts") {
						tables[table] = true
					}
				}
			}
		}
	}
	return tables
}

func TestSQLGuardsScopeErasureInventoryMetadata(t *testing.T) {
	for _, guard := range []struct {
		table, model string
		check        func(map[string]string) []string
	}{
		{"login_throttles", "LoginThrottle", func(sources map[string]string) []string {
			return throttleSQLViolations(sources, nil)
		}},
		{"suppressions", "Suppression", func(sources map[string]string) []string {
			return suppressionSQLViolations(sources, nil)
		}},
		{"email_address_optouts", "EmailAddressOptOut", emailChoiceViolations},
	} {
		t.Run(guard.table, func(t *testing.T) {
			row := fmt.Sprintf(`{%q, "id", "T01", "RET", []string{"T01"}}`, guard.table)
			declaration := "var erasureColumns = []erasureColumn{" + row + "}\n"
			sql := "SELECT * FROM " + guard.table
			for _, tc := range []struct {
				name, path, source string
				wantViolation      bool
			}{
				{"literal metadata", "erasure_inventory.go", declaration, false},
				{"same basename in another directory", "internal/erasure_inventory.go", declaration, true},
				{"another file", "other.go", declaration, true},
				{"another variable", "erasure_inventory.go", strings.Replace(declaration, "erasureColumns", "other", 1), true},
				{"another row type", "erasure_inventory.go", strings.Replace(declaration, "[]erasureColumn", "[]other", 1), true},
				{"SQL in table field", "erasure_inventory.go", strings.Replace(declaration, strconv.Quote(guard.table), strconv.Quote(sql), 1), true},
				{"SQL in column field", "erasure_inventory.go", strings.Replace(declaration, `"id"`, strconv.Quote(sql), 1), true},
				{"SQL in rules", "erasure_inventory.go", strings.Replace(declaration, `[]string{"T01"}`, "[]string{"+strconv.Quote(sql)+"}", 1), true},
				{"Table call beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("func other() { db.Table(%q) }", guard.table), true},
				{"SQL constant beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("const query = %q", sql), true},
				{"TableName beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("func (other) TableName() string { return %q }", guard.table), true},
				{"executable initializer", "erasure_inventory.go", "var erasureColumns = func() []erasureColumn { db.Exec(" + strconv.Quote(sql) + "); return []erasureColumn{" + row + "} }()", true},
				{"executable row field", "erasure_inventory.go", strings.Replace(declaration, `"id"`, "func() string { db.Exec("+strconv.Quote(sql)+"); return \"id\" }()", 1), true},
				{"computed table", "erasure_inventory.go", strings.Replace(declaration, strconv.Quote(guard.table), strconv.Quote(guard.table)+` + ""`, 1), true},
				{"model beside metadata", "erasure_inventory.go", declaration + "func other() { _ = &models." + guard.model + "{} }", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					src := "package main\nimport \"fold/models\"\n" + tc.source
					bad := guard.check(map[string]string{tc.path: src})
					if strings.Contains(strings.Join(bad, "\n"), "parse:") {
						t.Fatalf("invalid guard fixture: %v", bad)
					}
					if (len(bad) != 0) != tc.wantViolation {
						t.Fatalf("want violation %t, got %v", tc.wantViolation, bad)
					}
				})
			}
		})
	}
}

// suppressionSQLViolations checks parsed sources keyed by slash path from the
// repository root. A flag outside an allowed top-level function, constant or
// type is a violation; so is an allowed helper (outside models/) that never
// mentions the hash, a flagged literal in an allowed helper that normalizes
// in SQL, and an allowance that no longer matches any source.
func suppressionSQLViolations(sources map[string]string, allowed map[string][]string) []string {
	const hint = " (read open suppressions with mail.OpenSuppressionReasons; hash first, then the address of a NULL-hash row)"
	positions := token.NewFileSet()
	var bad []string
	seen := map[string]bool{}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := sources[path]
		tree, err := parser.ParseFile(positions, path, src, 0)
		if err != nil {
			bad = append(bad, path+": cannot parse: "+err.Error())
			continue
		}
		inventoryTables := erasureInventoryTableLiterals(path, tree)
		inModels := strings.HasPrefix(path, "models/")
		modelNames := map[string]bool{}
		for _, imp := range tree.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != "fold/models" {
				continue
			}
			name := "models"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			modelNames[name] = true
		}
		inspect := func(owner string, node ast.Node) {
			isAllowed := false
			for _, name := range allowed[path] {
				if name == owner {
					isAllowed = true
					seen[path+" "+name] = true
				}
			}
			var flagged []string
			ast.Inspect(node, func(n ast.Node) bool {
				at := func() string { return fmt.Sprintf("%s:%d", path, positions.Position(n.Pos()).Line) }
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind != token.STRING || inventoryTables[n] {
						return true
					}
					text, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					text = strings.ToLower(strings.Join(strings.Fields(text), " "))
					if suppressionTableRe.MatchString(text) || text == "suppressions" || text == "public.suppressions" {
						flagged = append(flagged, at()+": SQL on the suppressions table in "+owner)
						if isAllowed && (strings.Contains(text, "lower(") || strings.Contains(text, "btrim(")) {
							bad = append(bad, at()+": "+owner+" normalizes an address in SQL (lower/btrim); normalize in Go with mail.NormalizeAddress"+hint)
						}
					}
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && modelNames[x.Name] && n.Sel.Name == "Suppression" {
						flagged = append(flagged, at()+": models.Suppression used in "+owner)
					}
				case *ast.TypeSpec:
					if n.Name.Name == "Suppression" && !inModels {
						flagged = append(flagged, at()+": a type named Suppression in "+owner)
					}
				}
				return true
			})
			if !isAllowed {
				for _, f := range flagged {
					bad = append(bad, f+hint)
				}
				return
			}
			if !inModels {
				text := src[positions.Position(node.Pos()).Offset:positions.Position(node.End()).Offset]
				if !strings.Contains(text, "address_hash") && !strings.Contains(text, "AddressHash") {
					bad = append(bad, fmt.Sprintf("%s:%d: %s is allowed to touch suppressions but never mentions address_hash or AddressHash%s", path, positions.Position(node.Pos()).Line, owner, hint))
				}
			}
		}
		for _, decl := range tree.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				inspect(d.Name.Name, d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						owner := ""
						if len(s.Names) > 0 {
							owner = s.Names[0].Name
						}
						inspect(owner, s)
					case *ast.TypeSpec:
						inspect(s.Name.Name, s)
					}
				}
			}
		}
	}
	for path, names := range allowed {
		for _, name := range names {
			if !seen[path+" "+name] {
				bad = append(bad, fmt.Sprintf("%s: suppressionSQLAllowed names %s, which no longer exists there; update the list and keep the SQL in helpers that use the hash", path, name))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestSuppressionSQLStaysInItsHelpers(t *testing.T) {
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error {
		sources[path] = string(src)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, v := range suppressionSQLViolations(sources, suppressionSQLAllowed) {
		t.Error(v)
	}
}

// The clean tree holds every allowed helper once, using the hash, and nothing
// else touching the table. Each bypass below changes it in one way.
func cleanSuppressionSources() map[string]string {
	return map[string]string{
		"internal/mail/suppress.go": `package mail
const openSuppressionsSQL = "SELECT address, address_hash, reason FROM suppressions WHERE address_hash IN ?"
func OpenSuppressionReasons() { _ = StoredAddressHash }
`,
		"api_admin_suppressions.go": `package main
import "fold/models"
func openSuppressions() { var rows []models.Suppression; _ = rows[0].AddressHash }
func (a *App) apiAdminClearSuppression() { q := "UPDATE suppressions SET cleared_at = ? WHERE address_hash = ?"; _ = q }
`,
		"api_email_webhooks.go": `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (channel, address, address_hash) VALUES (?, ?, ?)"; _ = q }
`,
		"models/models.go": `package models
type Suppression struct{ AddressHash *string }
func All() []any { return []any{&Suppression{}} }
`,
		"api_other.go": `package main
func other() {}
`,
	}
}

func TestSuppressionSQLGuardCatchesBypasses(t *testing.T) {
	if bad := suppressionSQLViolations(cleanSuppressionSources(), suppressionSQLAllowed); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	for name, tc := range map[string]struct {
		file, source string // replaces or adds this file
		want         string // a substring of the violation
	}{
		"a second subquery that normalizes in SQL": {"api_other.go", `package main
func other() { q := "SELECT 1 WHERE EXISTS (SELECT 1 FROM suppressions s WHERE s.address = lower(btrim(users.email)))"; _ = q }
`, "api_other.go:2"},
		"a Table call": {"api_other.go", `package main
func other() { db.Table("suppressions").Find(&x) }
`, "api_other.go:2"},
		"concatenated SQL": {"api_other.go", `package main
func other() { q := "SELECT reason FROM " + "suppressions"; _ = q }
`, "api_other.go:2"},
		"a model with a TableName": {"api_other.go", `package main
type thing struct{}
func (thing) TableName() string { return "suppressions" }
`, "api_other.go:3"},
		"a local Suppression type": {"api_other.go", `package main
type Suppression struct{}
`, "api_other.go:2"},
		"models.Suppression elsewhere": {"api_other.go", `package main
import "fold/models"
func other() { var rows []models.Suppression; _ = rows }
`, "api_other.go:3"},
		"models.Suppression through an import alias": {"api_other.go", `package main
import m "fold/models"
func other() { _ = &m.Suppression{} }
`, "api_other.go:3"},
		"a second reader in an allowed file": {"api_admin_suppressions.go", `package main
import "fold/models"
func openSuppressions() { var rows []models.Suppression; _ = rows[0].AddressHash }
func (a *App) apiAdminClearSuppression() { q := "UPDATE suppressions SET cleared_at = ? WHERE address_hash = ?"; _ = q }
func anotherReader() { q := "SELECT reason FROM suppressions WHERE address_hash = ?"; _ = q }
`, "api_admin_suppressions.go:5"},
		"an allowed function that normalizes in SQL": {"api_email_webhooks.go", `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (address, address_hash) SELECT lower(btrim(email)), ?"; _ = q }
`, "normalizes an address in SQL"},
		"an allowed function that never uses the hash": {"api_email_webhooks.go", `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (channel, address) VALUES (?, ?)"; _ = q }
`, "never mentions address_hash"},
		"an allowed helper that was renamed": {"api_email_webhooks.go", `package main
func applyEvent() {}
`, "names applyEmailEvent, which no longer exists"},
	} {
		sources := cleanSuppressionSources()
		sources[tc.file] = tc.source
		bad := suppressionSQLViolations(sources, suppressionSQLAllowed)
		found := false
		for _, v := range bad {
			if strings.Contains(v, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want a violation containing %q, got %v", name, tc.want, bad)
		}
	}
	// A violation names the helper to use.
	sources := cleanSuppressionSources()
	sources["api_other.go"] = "package main\nfunc other() { db.Table(\"suppressions\") }\n"
	if bad := suppressionSQLViolations(sources, suppressionSQLAllowed); len(bad) != 1 || !strings.Contains(bad[0], "mail.OpenSuppressionReasons") {
		t.Errorf("violation = %v, want one that names mail.OpenSuppressionReasons", bad)
	}
}

// ---------------------------------------------------------------------------
// R4-T07A: contact-request text changes stay in their helpers.
//
// Rules R-1 to R-7 (design.T07-A.guards) and the scoped reading of hard
// constraint H8 (RC-1 (b), (c), (d), (e)). Like the mail and suppression
// guards it parses production Go and reads syntax only.
// ---------------------------------------------------------------------------

// ctgLimits is the guard's stated reach. It is printed whenever the guard
// fails, and the self-test checks that it still names the SQL it cannot see.
const ctgLimits = "This guard is parse-only, like the mail and suppression guards. It is not a type checker or a call graph. " +
	"It binds names to models.ContactRequest only from declarations, parameters, results, composite literals, new, make, append, " +
	"type assertions, range values and := or = assignments, by the parser's lexical resolution within one file (a declaration that " +
	"shadows a name is its own binding, a closure sees the variable it captures, and a name declared in another file is not bound), " +
	"and it reads receiver chains syntactically plus a local variable of the same top-level declaration that is assigned exactly " +
	"once (counting assignments from closures, op-assignments and &v) from a chain. It reads constant SQL by quote: a semicolon in " +
	"a string, E-string, dollar quote or double-quoted identifier does not end a statement, and a comment or an unclosed quote makes " +
	"the rest of the literal one statement. Same-file string constants resolve by the same lexical rule as variables (a constant that " +
	"a closure or block redeclares is its own; a map-literal key constant must be declared before its use). The callback's *gorm.DB " +
	"argument to eraseContactRequestTextIn must resolve to the callback's own parameter. It does not see SQL built from non-literal pieces (computed-table-name SQL such as " +
	"internal/testdb's 'DELETE FROM ' + t is not seen, nor is fmt.Sprintf with non-constant arguments), constants from other files " +
	"or packages, table or column names held in variables, reflection, generics, type aliases, embedded structs, ContactRequest " +
	"values or *gorm.DB chains that arrive from another function's result, a parameter, a field or an interface, reassigned chain " +
	"variables, gorm.Expr or clause values built at run time, GORM scopes or callbacks that add clauses, Create with a map on a " +
	"contact_requests model, the order of assignments inside apiCreateAddRequest, a write through a pointer to a field (&r.Reason), " +
	"or SQL sent around GORM (database/sql or pgx directly; Fold sends none). Within those limits it rejects every form named by " +
	"R-1 to R-7 and RC-1 (b) to (e); beyond them only the database triggers and review apply."

var (
	ctgProtectedWord  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(?:reason|add_childcare_ages_note|addchildcareagesnote)(?:[^a-z0-9_]|$)`)
	ctgTableName      = regexp.MustCompile(`^(?:public\.)?contact_requests(?: (?:as )?[a-z_][a-z0-9_]*)?$`)
	ctgUpdateSQL      = regexp.MustCompile(`\bupdate (?:only )?(?:public\.)?contact_requests\b`)
	ctgInsertSQL      = regexp.MustCompile(`\binsert into (?:public\.)?contact_requests\b`)
	ctgMergeSQL       = regexp.MustCompile(`\bmerge into (?:only )?(?:public\.)?contact_requests\b`)
	ctgDeleteSQL      = regexp.MustCompile(`\bdelete from (?:only )?(?:public\.)?contact_requests\b`)
	ctgTruncateSQL    = regexp.MustCompile(`\btruncate\b[^;]*\bcontact_requests\b`)
	ctgSetKeyword     = regexp.MustCompile(`\bset\b`)
	ctgDoUpdateSet    = regexp.MustCompile(`\bdo update set\b`)
	ctgUpdateSet      = regexp.MustCompile(`\bupdate set\b`)
	ctgSetListEnd     = regexp.MustCompile(`\b(?:where|from|returning)\b`)
	ctgDollarTag      = regexp.MustCompile(`^\$(?:[a-z_][a-z0-9_]*)?\$`)
	ctgTriggerBypass  = regexp.MustCompile(`\b(?:disable|enable replica|enable always) trigger\b`)
	ctgDropTrigger    = regexp.MustCompile(`\bdrop trigger\b`)
	ctgCreateFunction = regexp.MustCompile(`\bcreate (?:or replace )?function (?:public\.)?(?:add_request_identity|add_request_completed)\b`)
)

// ctgTerminals are the GORM calls that write or delete rows.
var ctgTerminals = map[string]bool{
	"Create": true, "CreateInBatches": true, "Save": true, "FirstOrCreate": true, "FirstOrInit": true,
	"Update": true, "UpdateColumn": true, "Updates": true, "UpdateColumns": true, "Delete": true,
}

const ctgResetLiteral = "SET LOCAL session_replication_role = replica;"

// ctgElem is one call in a GORM chain: its method name and arguments.
type ctgElem struct {
	name string
	args []ast.Expr
}

type ctgFile struct {
	path     string
	fset     *token.FileSet
	tree     *ast.File
	models   map[string]bool       // import names of fold/models
	dot      bool                  // fold/models is dot-imported
	inModels bool                  // the file is in package models
	consts   map[*ctgObject]string // same-file string constants by declaration, so a shadowing const is its own
	folded   map[*ast.BinaryExpr]string
	skip     map[*ast.BasicLit]bool
	objs     map[*ctgObject]bool     // declarations the parser tied to ContactRequest values
	single   map[*ctgObject]ast.Expr // locals of the walked declaration assigned exactly once
}

type ctgRun struct {
	bad         []string
	eraseCalls  int
	eraseSites  map[string]int
	agesLiteral bool
	// reasonLiteral, reasonCalls and reasonSites are the same for the reason
	// helper, eraseContactRequestReasonsIn (T07B-2b).
	reasonLiteral bool
	reasonCalls   int
	reasonSites   map[string]int
	resetLiteral  bool
}

// ctgObject is the parser's own resolution of a name to its declaration. It
// is how the guard tells a captured variable from one that shadows it. The
// parser resolves within one file only; a name declared in another file has no
// object and is never bound.
//
//lint:ignore SA1019 Parse-only guard uses parser-local bindings, not type identity; unresolved flows are documented in ctgLimits and AGENTS.md.
type ctgObject = ast.Object

// ctgObj returns the object an identifier declares or refers to, or nil.
func ctgObj(id *ast.Ident) *ctgObject {
	if id == nil {
		return nil
	}
	return id.Obj
}

// ctgDeclaredBy reports whether id is the declaring name of decl itself, as
// opposed to a later assignment to a variable declared elsewhere.
func ctgDeclaredBy(id *ast.Ident, decl ast.Node) bool {
	return id.Obj != nil && id.Obj.Decl == decl
}

func ctgUnparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func ctgNormalize(s string) string {
	return strings.Join(strings.Fields(ctgUnquoteIdents(strings.ToLower(s))), " ")
}

// ctgEscapeString reports whether the single quote at s[i] opens an E-string,
// where a backslash escapes the next byte. s is lower case.
func ctgEscapeString(s string, i int) bool {
	return i > 0 && s[i-1] == 'e' && (i == 1 || strings.IndexByte("abcdefghijklmnopqrstuvwxyz0123456789_", s[i-2]) < 0)
}

// ctgUnquoteIdents drops the double quotes of SQL identifiers so a quoted
// table or column name matches like a bare one. It reads quote context first,
// as ctgStatementEnd does: quoted strings and comments retain their original
// bytes, and a semicolon inside a
// double-quoted identifier (an alias such as "r;kept") becomes an underscore so
// it cannot end the statement later. A double quote that never closes makes the
// rest of s one identifier, so the guard reads more, never less.
func ctgUnquoteIdents(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						i++
						continue
					}
					break
				}
				if s[i] == ';' {
					b.WriteByte('_')
				} else {
					b.WriteByte(s[i])
				}
			}
		case c == '-' && strings.HasPrefix(s[i:], "--"), c == '/' && strings.HasPrefix(s[i:], "/*"):
			b.WriteString(s[i:])
			return b.String()
		case c == '\'':
			end := ctgQuoteEnd(s, i, ctgEscapeString(s, i))
			if end < 0 {
				end = len(s) - 1
			}
			b.WriteString(s[i : end+1])
			i = end
		case c == '$':
			tag := ctgDollarTag.FindString(s[i:])
			if tag == "" {
				b.WriteByte(c)
				continue
			}
			end := len(s) - 1
			if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				end = i + len(tag) + j + len(tag) - 1
			}
			b.WriteString(s[i : end+1])
			i = end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ctgProtectedName reports whether a column or Go field name (optionally
// quoted or qualified) is reason or add_childcare_ages_note. EndReason and
// end_reason are other names and never match.
func ctgProtectedName(s string) bool {
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\"", ""), "`", "")))
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s == "reason" || s == "add_childcare_ages_note" || s == "addchildcareagesnote"
}

func ctgUnwrap(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return e
			}
			e = x.X
		default:
			return e
		}
	}
}

func ctgTypeName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// ctgEnclosing returns the top-level function around the innermost parent and
// whether a func literal sits between them.
func ctgEnclosing(parents []ast.Node) (*ast.FuncDecl, bool) {
	inLit := false
	for i := len(parents) - 1; i >= 0; i-- {
		switch n := parents[i].(type) {
		case *ast.FuncLit:
			inLit = true
		case *ast.FuncDecl:
			return n, inLit
		}
	}
	return nil, inLit
}

// ctgCreationSite reports whether decl is one of the two functions in
// api_contact.go that build a new request. Reason may be set only in the
// composite literal there; the ages note only in apiCreateAddRequest.
func ctgCreationSite(path string, decl *ast.FuncDecl, field string, literal bool) bool {
	if path != "api_contact.go" || decl == nil {
		return false
	}
	switch field {
	case "Reason":
		return literal && (decl.Name.Name == "apiCreateContactRequest" || decl.Name.Name == "apiCreateAddRequest")
	case "AddChildcareAgesNote":
		return decl.Name.Name == "apiCreateAddRequest"
	}
	return false
}

func newCtgFile(path string, fset *token.FileSet, tree *ast.File) *ctgFile {
	f := &ctgFile{
		path: path, fset: fset, tree: tree,
		models:   map[string]bool{},
		inModels: strings.HasPrefix(path, "models/"),
		objs:     map[*ctgObject]bool{},
		single:   map[*ctgObject]ast.Expr{},
		consts:   map[*ctgObject]string{},
	}
	for _, imp := range tree.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != "fold/models" {
			continue
		}
		name := "models"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch name {
		case ".":
			f.dot = true
		case "_":
		default:
			f.models[name] = true
		}
	}
	for pass := 0; pass < 3; pass++ {
		f.collectConsts()
	}
	f.foldLiterals()
	f.bind(tree)
	return f
}

// fold evaluates string literals, same-file string constants and their
// concatenation.
func (f *ctgFile) fold(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return f.fold(x.X)
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.Ident:
		obj := ctgObj(x)
		if obj == nil {
			return "", false
		}
		s, ok := f.consts[obj]
		return s, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok := f.fold(x.X)
		if !ok {
			return "", false
		}
		r, ok := f.fold(x.Y)
		if !ok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

func (f *ctgFile) collectConsts() {
	ast.Inspect(f.tree, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				if obj := ctgObj(name); obj == nil {
					continue
				} else if s, ok := f.fold(vs.Values[i]); ok {
					f.consts[obj] = s
				}
			}
		}
		return true
	})
}

// foldLiterals finds the maximal constant concatenations. Each is scanned as
// one string; the literals inside it are not scanned again on their own.
func (f *ctgFile) foldLiterals() {
	f.folded = map[*ast.BinaryExpr]string{}
	f.skip = map[*ast.BasicLit]bool{}
	ast.Inspect(f.tree, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.ADD {
			return true
		}
		s, ok := f.fold(be)
		if !ok {
			return true
		}
		f.folded[be] = s
		ast.Inspect(be, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok {
				f.skip[lit] = true
			}
			return true
		})
		return false
	})
}

// isCRName: the type expression names models.ContactRequest (through an
// import alias, a dot import, or bare inside package models).
func (f *ctgFile) isCRName(t ast.Expr) bool {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && f.models[id.Name] && x.Sel.Name == "ContactRequest"
	case *ast.Ident:
		return x.Name == "ContactRequest" && (f.dot || f.inModels)
	}
	return false
}

// isCRType also accepts pointers, slices, arrays, maps (by value type) and
// variadic parameters of ContactRequest.
func (f *ctgFile) isCRType(t ast.Expr) bool {
	switch x := t.(type) {
	case *ast.ParenExpr:
		return f.isCRType(x.X)
	case *ast.StarExpr:
		return f.isCRType(x.X)
	case *ast.ArrayType:
		return f.isCRType(x.Elt)
	case *ast.MapType:
		return f.isCRType(x.Value)
	case *ast.Ellipsis:
		return f.isCRType(x.Elt)
	}
	return f.isCRName(t)
}

// bound reports whether the parser can bind the expression to a
// ContactRequest value in this declaration.
func (f *ctgFile) bound(e ast.Expr) bool {
	for steps := 0; steps < 32; steps++ {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return false
			}
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.Ident:
			obj := ctgObj(x)
			return obj != nil && f.objs[obj]
		case *ast.CompositeLit:
			return x.Type != nil && f.isCRType(x.Type)
		case *ast.TypeAssertExpr:
			return x.Type != nil && f.isCRType(x.Type)
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			if !ok || len(x.Args) == 0 {
				return false
			}
			switch id.Name {
			case "new", "make":
				return f.isCRType(x.Args[0])
			case "append":
				e = x.Args[0]
			default:
				return false
			}
		default:
			return false
		}
	}
	return false
}

// crLiteral: a ContactRequest composite literal, including a slice of them.
func (f *ctgFile) crLiteral(e ast.Expr) bool {
	lit, ok := ctgUnwrap(e).(*ast.CompositeLit)
	return ok && lit.Type != nil && f.isCRType(lit.Type)
}

// crValueLiteral: a single ContactRequest literal, by value or pointer.
func (f *ctgFile) crValueLiteral(e ast.Expr) bool {
	lit, ok := ctgUnwrap(e).(*ast.CompositeLit)
	return ok && lit.Type != nil && f.isCRName(lit.Type)
}

// literalsOf returns the ContactRequest literals a composite literal builds:
// itself, or the elided elements of a slice, array or map of them.
func (f *ctgFile) literalsOf(lit *ast.CompositeLit) []*ast.CompositeLit {
	if lit.Type == nil {
		return nil
	}
	return f.literalsFor(lit, lit.Type)
}

func (f *ctgFile) literalsFor(lit *ast.CompositeLit, t ast.Expr) []*ast.CompositeLit {
	for {
		if p, ok := t.(*ast.ParenExpr); ok {
			t = p.X
			continue
		}
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
			continue
		}
		break
	}
	if f.isCRName(t) {
		return []*ast.CompositeLit{lit}
	}
	var elem ast.Expr
	switch x := t.(type) {
	case *ast.ArrayType:
		elem = x.Elt
	case *ast.MapType:
		elem = x.Value
	default:
		return nil
	}
	if !f.isCRType(elem) {
		return nil
	}
	var out []*ast.CompositeLit
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			el = kv.Value
		}
		inner, ok := ctgUnwrap(el).(*ast.CompositeLit)
		if ok && inner.Type == nil {
			out = append(out, f.literalsFor(inner, elem)...)
		}
	}
	return out
}

// bind collects the declarations the parser can tie to ContactRequest values
// in root, to a fixed point so that q := &rs[0] and for _, x := range rs work.
// A declaration is bound, not its spelling: an inner parameter or variable
// that shadows a request's name stays unbound unless it is a request itself.
func (f *ctgFile) bind(root ast.Node) {
	for pass := 0; pass < 4; pass++ {
		changed := false
		set := func(id *ast.Ident) {
			if obj := ctgObj(id); obj != nil && id.Name != "_" && !f.objs[obj] {
				f.objs[obj] = true
				changed = true
			}
		}
		fields := func(list *ast.FieldList) {
			if list == nil {
				return
			}
			for _, fld := range list.List {
				if f.isCRType(fld.Type) {
					for _, id := range fld.Names {
						set(id)
					}
				}
			}
		}
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				fields(x.Recv)
			case *ast.FuncType:
				fields(x.Params)
				fields(x.Results)
			case *ast.ValueSpec:
				if x.Type != nil {
					if f.isCRType(x.Type) {
						for _, id := range x.Names {
							set(id)
						}
					}
				} else if len(x.Values) == len(x.Names) {
					for i, id := range x.Names {
						if f.bound(x.Values[i]) {
							set(id)
						}
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) == len(x.Rhs) {
					for i, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && f.bound(x.Rhs[i]) {
							set(id)
						}
					}
				}
			case *ast.RangeStmt:
				if id, ok := x.Value.(*ast.Ident); ok && f.bound(x.X) {
					set(id)
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
}

// singleAssigned records the locals of decl that are declared in it and
// assigned exactly once, so q := tx.Model(&r).Where(...) followed by
// q.Update(...) is one chain. Variables are the parser's objects: a closure
// that captures q is the same variable and its assignments count against it,
// while a q declared inside a closure is another variable. Parameters, range
// variables, op-assignments and &q count twice, which takes them out.
func (f *ctgFile) singleAssigned(decl ast.Node) {
	count := map[*ctgObject]int{}
	value := map[*ctgObject]ast.Expr{}
	declared := map[*ctgObject]bool{}
	heavy := func(e ast.Expr) {
		if obj := ctgObj(ctgIdent(ctgUnparen(e))); obj != nil {
			count[obj] += 2
		}
	}
	ast.Inspect(decl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id := ctgIdent(ctgUnparen(lhs))
				obj := ctgObj(id)
				if obj == nil {
					continue
				}
				count[obj]++
				if x.Tok != token.ASSIGN && x.Tok != token.DEFINE {
					count[obj] += 2
				}
				if ctgDeclaredBy(id, x) {
					declared[obj] = true
				}
				if len(x.Lhs) == len(x.Rhs) {
					value[obj] = x.Rhs[i]
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				obj := ctgObj(id)
				if obj == nil {
					continue
				}
				declared[obj] = true
				if len(x.Values) > 0 {
					count[obj]++
					if len(x.Names) == len(x.Values) {
						value[obj] = x.Values[i]
					}
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if e != nil {
					heavy(e)
				}
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				heavy(x.X)
			}
		case *ast.FuncType:
			for _, list := range []*ast.FieldList{x.Params, x.Results} {
				if list == nil {
					continue
				}
				for _, fld := range list.List {
					for _, id := range fld.Names {
						if obj := ctgObj(id); obj != nil {
							count[obj] += 2
						}
					}
				}
			}
		}
		return true
	})
	f.single = map[*ctgObject]ast.Expr{}
	for obj, c := range count {
		if v := value[obj]; c == 1 && v != nil && declared[obj] {
			f.single[obj] = v
		}
	}
}

// ctgIdent returns e as an identifier, or nil.
func ctgIdent(e ast.Expr) *ast.Ident {
	id, _ := e.(*ast.Ident)
	return id
}

// chain reads the receiver chain that ends in call, terminal first.
func (f *ctgFile) chain(call *ast.CallExpr) []ctgElem {
	var elems []ctgElem
	seen := map[*ctgObject]bool{}
	var cur ast.Expr = call
	for step := 0; step < 64; step++ {
		switch x := cur.(type) {
		case *ast.ParenExpr:
			cur = x.X
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return elems
			}
			elems = append(elems, ctgElem{sel.Sel.Name, x.Args})
			cur = sel.X
		case *ast.Ident:
			obj := ctgObj(x)
			next, ok := f.single[obj]
			if !ok || seen[obj] {
				return elems
			}
			seen[obj] = true
			cur = next
		default:
			return elems
		}
	}
	return elems
}

// litKeysProblem reads the keys of a map or struct literal. It returns "" when
// every key is readable and none names a protected column.
func (f *ctgFile) litKeysProblem(lit *ast.CompositeLit) string {
	_, isMap := lit.Type.(*ast.MapType)
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return "an element without a key, which the guard cannot read"
		}
		var name string
		switch k := kv.Key.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(k.Value)
			if k.Kind != token.STRING || err != nil {
				return "a key the guard cannot read"
			}
			name = s
		case *ast.Ident:
			if !isMap {
				name = k.Name
			} else if s, ok := f.consts[ctgObj(k)]; ok {
				name = s
			} else {
				return "a map key held in a variable, which the guard cannot read"
			}
		default:
			return "a key the guard cannot read"
		}
		if ctgProtectedName(name) {
			return "a key naming reason or add_childcare_ages_note"
		}
	}
	return ""
}

// updatesProblem: the argument of Updates or UpdateColumns must be a literal
// whose keys the guard can read and none of which is a protected column.
func (f *ctgFile) updatesProblem(arg ast.Expr) string {
	lit, ok := ctgUnwrap(arg).(*ast.CompositeLit)
	if !ok {
		return "an Updates argument that is not a literal, so the guard cannot show it leaves reason and the ages note out"
	}
	return f.litKeysProblem(lit)
}

// conditionProblem reads a Where, Assign, Attrs or inline FirstOrCreate
// condition.
func (f *ctgFile) conditionProblem(arg ast.Expr) string {
	e := ctgUnwrap(arg)
	if s, ok := f.fold(e); ok {
		if ctgProtectedWord.MatchString(s) {
			return "a condition naming reason or add_childcare_ages_note"
		}
		return ""
	}
	if lit, ok := e.(*ast.CompositeLit); ok {
		return f.litKeysProblem(lit)
	}
	return "a condition that is not a literal the guard can read"
}

func (f *ctgFile) selectProblem(args []ast.Expr) string {
	const bad = "Select names reason, add_childcare_ages_note or *, or cannot be read, before a write"
	for _, a := range args {
		e := ctgUnwrap(a)
		if s, ok := f.fold(e); ok {
			if strings.Contains(s, "*") || ctgProtectedWord.MatchString(s) {
				return bad
			}
			continue
		}
		lit, ok := e.(*ast.CompositeLit)
		if !ok {
			return bad
		}
		for _, el := range lit.Elts {
			s, ok := f.fold(el)
			if !ok || strings.Contains(s, "*") || ctgProtectedWord.MatchString(s) {
				return bad
			}
		}
	}
	return ""
}

func (f *ctgFile) doUpdatesProblem(v ast.Expr) string {
	call, ok := ctgUnwrap(v).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "an OnConflict DoUpdates that is not a call the guard can read"
	}
	switch ctgTypeName(call.Fun) {
	case "AssignmentColumns":
		lit, ok := ctgUnwrap(call.Args[0]).(*ast.CompositeLit)
		if !ok {
			return "AssignmentColumns with a list the guard cannot read"
		}
		for _, el := range lit.Elts {
			s, ok := f.fold(el)
			if !ok {
				return "AssignmentColumns with a name the guard cannot read"
			}
			if ctgProtectedName(s) {
				return "an OnConflict DoUpdates naming reason or add_childcare_ages_note"
			}
		}
		return ""
	case "Assignments":
		lit, ok := ctgUnwrap(call.Args[0]).(*ast.CompositeLit)
		if !ok {
			return "Assignments with a map the guard cannot read"
		}
		return f.litKeysProblem(lit)
	}
	return "an OnConflict DoUpdates that is neither clause.AssignmentColumns nor clause.Assignments"
}

// conflictProblem reads one Clauses argument on a contact_requests target. A
// literal of another clause type (Locking, Where) is not a conflict clause.
func (f *ctgFile) conflictProblem(arg ast.Expr) string {
	lit, ok := ctgUnwrap(arg).(*ast.CompositeLit)
	if !ok {
		return "a Clauses argument that is not a literal, so the guard cannot read UpdateAll or DoUpdates"
	}
	if ctgTypeName(lit.Type) != "OnConflict" {
		return ""
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return "an OnConflict literal without field names"
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "UpdateAll":
			if v, ok := kv.Value.(*ast.Ident); !ok || v.Name != "false" {
				return "OnConflict UpdateAll writes every non-key column, including reason and the ages note"
			}
		case "DoUpdates":
			if why := f.doUpdatesProblem(kv.Value); why != "" {
				return why
			}
		}
	}
	return ""
}

func (run *ctgRun) add(f *ctgFile, rule string, n ast.Node, parents []ast.Node, format string, args ...any) {
	where := "package level"
	if decl, _ := ctgEnclosing(parents); decl != nil {
		where = decl.Name.Name
	}
	run.bad = append(run.bad, fmt.Sprintf("%s:%d: %s: %s: %s", f.path, f.fset.Position(n.Pos()).Line, where, rule, fmt.Sprintf(format, args...)))
}

// imports is RC-1 (d): production code never imports internal/testdb, whose
// reset literal is the one session_replication_role allowance.
func (run *ctgRun) imports(f *ctgFile) {
	if strings.HasPrefix(f.path, "internal/testdb/") {
		return
	}
	for _, imp := range f.tree.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == "fold/internal/testdb" {
			run.bad = append(run.bad, fmt.Sprintf("%s:%d: package level: RC-1(d): production code imports fold/internal/testdb, whose reset sets session_replication_role (plain, aliased, blank and dot imports are all rejected)", f.path, f.fset.Position(imp.Pos()).Line))
		}
	}
}

func (run *ctgRun) file(f *ctgFile) {
	for _, decl := range f.tree.Decls {
		f.singleAssigned(decl)
		run.walk(f, decl)
	}
}

func (run *ctgRun) walk(f *ctgFile, root ast.Node) {
	var parents []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			parents = parents[:len(parents)-1]
			return true
		}
		parents = append(parents, n)
		switch x := n.(type) {
		case *ast.AssignStmt:
			run.assign(f, x, parents)
		case *ast.CompositeLit:
			run.literal(f, x, parents)
		case *ast.CallExpr:
			run.call(f, x, parents)
		case *ast.BasicLit:
			if x.Kind == token.STRING && !f.skip[x] {
				if s, err := strconv.Unquote(x.Value); err == nil {
					run.text(f, x, s, parents)
				}
			}
		case *ast.BinaryExpr:
			if s, ok := f.folded[x]; ok {
				run.text(f, x, s, parents)
			}
		case *ast.Ident:
			run.ident(f, x, parents)
		}
		return true
	})
}

// assign is R-1 (field writes) and the overwrite half of R-2.
func (run *ctgRun) assign(f *ctgFile, a *ast.AssignStmt, parents []ast.Node) {
	decl, _ := ctgEnclosing(parents)
	for i, lhs := range a.Lhs {
		if sel, ok := ctgUnparen(lhs).(*ast.SelectorExpr); ok && (sel.Sel.Name == "Reason" || sel.Sel.Name == "AddChildcareAgesNote") && f.bound(sel.X) {
			if !ctgCreationSite(f.path, decl, sel.Sel.Name, false) {
				run.add(f, "R-1", a, parents, "assignment to %s on a models.ContactRequest value outside its allowed site", sel.Sel.Name)
			}
		}
		if a.Tok != token.ASSIGN || len(a.Lhs) != len(a.Rhs) || !f.crValueLiteral(a.Rhs[i]) {
			continue
		}
		overwrite := false
		switch t := ctgUnwrap(lhs).(type) {
		case *ast.StarExpr:
			overwrite = f.bound(t.X)
		case *ast.IndexExpr:
			overwrite = f.bound(t.X)
		case *ast.Ident:
			// Rebinding a pointer variable to a fresh literal overwrites no row.
			overwrite = f.bound(t) && !ctgIsPointerLiteral(a.Rhs[i])
		}
		if overwrite && !ctgCreationSite(f.path, decl, "Reason", true) {
			run.add(f, "R-2", a, parents, "a models.ContactRequest literal assigned over an existing value; a literal that omits Reason silently blanks it")
		}
	}
}

func ctgIsPointerLiteral(e ast.Expr) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			return x.Op == token.AND
		default:
			return false
		}
	}
}

// literal is R-2 (composite literals that set a protected field).
func (run *ctgRun) literal(f *ctgFile, lit *ast.CompositeLit, parents []ast.Node) {
	decl, _ := ctgEnclosing(parents)
	for _, cr := range f.literalsOf(lit) {
		for _, el := range cr.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				run.add(f, "R-2", cr, parents, "a positional models.ContactRequest literal sets every field, including Reason")
				break
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || (key.Name != "Reason" && key.Name != "AddChildcareAgesNote") {
				continue
			}
			if !ctgCreationSite(f.path, decl, key.Name, true) {
				run.add(f, "R-2", cr, parents, "models.ContactRequest literal sets %s outside its allowed creation site", key.Name)
			}
		}
	}
}

// call applies R-2 (Save of a literal), R-3, R-4a, R-4b, R-4c and R-7 to a
// GORM chain that has a contact_requests target.
func (run *ctgRun) call(f *ctgFile, call *ast.CallExpr, parents []ast.Node) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !ctgTerminals[sel.Sel.Name] {
		return
	}
	name := sel.Sel.Name
	elems := f.chain(call)
	var first ast.Expr
	if len(call.Args) > 0 {
		first = call.Args[0]
	}
	if name == "Save" && first != nil && f.crLiteral(first) {
		run.add(f, "R-2", call, parents, "Save of a models.ContactRequest composite literal; save only a request loaded from the database")
	}
	explicit, hasModel := false, false
	for _, e := range elems {
		switch e.name {
		case "Model":
			hasModel = true
			if len(e.args) > 0 && f.bound(e.args[0]) {
				explicit = true
			}
		case "Table":
			if len(e.args) > 0 {
				if s, ok := f.fold(e.args[0]); ok && ctgTableName.MatchString(ctgNormalize(s)) {
					explicit = true
				}
			}
		}
	}
	implicit := false
	if first != nil && f.bound(first) {
		switch name {
		case "Create", "CreateInBatches", "Save", "FirstOrCreate", "FirstOrInit", "Delete":
			implicit = true
		case "Updates", "UpdateColumns":
			implicit = !hasModel
		}
	}
	if !explicit && !implicit {
		return
	}
	if name == "Delete" {
		run.add(f, "R-7", call, parents, "GORM Delete on a contact_requests target; the erasure authority deletes no request row")
		return
	}
	findOrCreate := name == "FirstOrCreate" || name == "FirstOrInit"
	for _, e := range elems {
		switch e.name {
		case "Clauses":
			for _, a := range e.args {
				if why := f.conflictProblem(a); why != "" {
					run.add(f, "R-4a", call, parents, "%s", why)
				}
			}
		case "Select":
			if name == "Save" || strings.HasPrefix(name, "Update") {
				if why := f.selectProblem(e.args); why != "" {
					run.add(f, "R-4c", call, parents, "%s", why)
				}
			}
		case "Assign", "Attrs":
			if findOrCreate {
				for _, a := range e.args {
					if why := f.conditionProblem(a); why != "" {
						run.add(f, "R-4b", call, parents, "%s in %s of %s", why, e.name, name)
					}
				}
			}
		case "Where":
			if findOrCreate && len(e.args) > 0 {
				if why := f.conditionProblem(e.args[0]); why != "" {
					run.add(f, "R-4b", call, parents, "%s in Where of %s", why, name)
				}
			}
		}
	}
	if findOrCreate && len(call.Args) > 1 {
		if why := f.conditionProblem(call.Args[1]); why != "" {
			run.add(f, "R-4b", call, parents, "%s as an inline condition of %s", why, name)
		}
	}
	switch name {
	case "Update", "UpdateColumn":
		s, ok := f.fold(first)
		if !ok || ctgProtectedName(s) {
			run.add(f, "R-3", call, parents, "%s names a column the guard cannot show is neither reason nor add_childcare_ages_note", name)
		}
	case "Updates", "UpdateColumns":
		if why := f.updatesProblem(first); why != "" {
			run.add(f, "R-3", call, parents, "%s: %s", name, why)
		}
	}
}

type ctgWrite struct{ kind, list string }

// ctgStatementEnd returns where the SQL statement at the front of s ends: the
// first semicolon outside a quoted string. It reads single-quoted strings
// (a doubled quote stays inside; after an E prefix a backslash escapes the next
// byte) and dollar quotes. A comment opener or a quote that never closes makes
// the rest of s one statement, so the guard reads more, never less. Double-quoted
// identifiers are handled earlier, by ctgUnquoteIdents, which hides their
// semicolons; s never holds a double quote.
func ctgStatementEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';':
			return i
		case '-':
			if strings.HasPrefix(s[i:], "--") {
				return len(s)
			}
		case '/':
			if strings.HasPrefix(s[i:], "/*") {
				return len(s)
			}
		case '\'':
			end := ctgQuoteEnd(s, i, ctgEscapeString(s, i))
			if end < 0 {
				return len(s)
			}
			i = end
		case '$':
			if tag := ctgDollarTag.FindString(s[i:]); tag != "" {
				j := strings.Index(s[i+len(tag):], tag)
				if j < 0 {
					return len(s)
				}
				i += len(tag) + j + len(tag) - 1
			}
		}
	}
	return len(s)
}

// ctgQuoteEnd returns the index of the quote that closes the string opened at
// s[open], or -1 when it never closes.
func ctgQuoteEnd(s string, open int, escapes bool) int {
	for j := open + 1; j < len(s); j++ {
		if escapes && s[j] == '\\' {
			j++
			continue
		}
		if s[j] != '\'' {
			continue
		}
		if j+1 < len(s) && s[j+1] == '\'' {
			j++
			continue
		}
		return j
	}
	return -1
}

// ctgSQLWrites finds the statements in normalized SQL that update a
// contact_requests row and mention a protected column after SET.
func ctgSQLWrites(t string) []ctgWrite {
	var out []ctgWrite
	scan := func(start, after *regexp.Regexp, kind string) {
		for _, m := range start.FindAllStringIndex(t, -1) {
			rest := t[m[1]:]
			rest = rest[:ctgStatementEnd(rest)]
			loc := after.FindStringIndex(rest)
			if loc == nil {
				continue
			}
			list := rest[loc[1]:]
			if ctgProtectedWord.MatchString(list) {
				out = append(out, ctgWrite{kind, list})
			}
		}
	}
	scan(ctgUpdateSQL, ctgSetKeyword, "UPDATE")
	scan(ctgInsertSQL, ctgDoUpdateSet, "INSERT ... ON CONFLICT DO UPDATE")
	scan(ctgMergeSQL, ctgUpdateSet, "MERGE ... UPDATE SET")
	return out
}

func ctgSetPart(list string) string {
	if loc := ctgSetListEnd.FindStringIndex(list); loc != nil {
		list = list[:loc[0]]
	}
	return strings.TrimSpace(list)
}

// resetAllowed is the one RC-1 (b) allowance: the exact reset literal as the
// argument of b.WriteString, directly in resetStatements of
// internal/testdb/testdb.go, once.
func (run *ctgRun) resetAllowed(f *ctgFile, n ast.Node, raw string, parents []ast.Node) bool {
	lit, ok := n.(*ast.BasicLit)
	if !ok || raw != ctgResetLiteral || f.path != "internal/testdb/testdb.go" || run.resetLiteral || len(parents) < 2 {
		return false
	}
	decl, inLit := ctgEnclosing(parents)
	if decl == nil || decl.Recv != nil || decl.Name.Name != "resetStatements" || inLit {
		return false
	}
	call, ok := parents[len(parents)-2].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Args[0] != ast.Expr(lit) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WriteString" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "b"
}

// text applies the string-literal rules: R-5 (raw SQL writes), R-7 (raw row
// deletion) and RC-1 (b) (trigger and replication-role bypasses).
func (run *ctgRun) text(f *ctgFile, n ast.Node, raw string, parents []ast.Node) {
	t := ctgNormalize(raw)
	if strings.Contains(t, "session_replication_role") {
		if run.resetAllowed(f, n, raw, parents) {
			run.resetLiteral = true
		} else {
			run.add(f, "RC-1(b)", n, parents, "session_replication_role in a Go string literal; the only allowance is the exact b.WriteString argument in resetStatements of internal/testdb/testdb.go")
		}
	}
	if ctgTriggerBypass.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "DISABLE TRIGGER, ENABLE REPLICA TRIGGER or ENABLE ALWAYS TRIGGER in a Go string literal; both triggers must stay enabled in origin mode")
	}
	if ctgDropTrigger.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "DROP TRIGGER in a Go string literal")
	}
	if ctgCreateFunction.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "CREATE FUNCTION for add_request_identity or add_request_completed in a Go string literal; only a migration defines them")
	}
	if ctgDeleteSQL.MatchString(t) || ctgTruncateSQL.MatchString(t) {
		run.add(f, "R-7", n, parents, "raw SQL deletes or truncates contact_requests; the erasure authority deletes no request row")
	}
	writes := ctgSQLWrites(t)
	if len(writes) == 0 {
		return
	}
	only := len(writes) == 1 && writes[0].kind == "UPDATE" && ctgSetPart(writes[0].list) == "add_childcare_ages_note = ''"
	onlyReason := len(writes) == 1 && writes[0].kind == "UPDATE" && ctgSetPart(writes[0].list) == "reason = ''"
	decl, inLit := ctgEnclosing(parents)
	site := f.path == "account_erasure.go" && decl != nil && decl.Recv == nil && decl.Name.Name == "eraseContactRequestTextIn" && !inLit && !run.agesLiteral
	if only && site {
		run.agesLiteral = true
		return
	}
	reasonSite := f.path == "account_erasure_reasons.go" && decl != nil && decl.Recv == nil && decl.Name.Name == "eraseContactRequestReasonsIn" && !inLit && !run.reasonLiteral
	if onlyReason && reasonSite {
		run.reasonLiteral = true
		return
	}
	run.add(f, "R-5", n, parents, "raw SQL writes reason or add_childcare_ages_note on contact_requests (%s); the only allowances are the single UPDATE literal in eraseContactRequestTextIn (account_erasure.go) whose SET list is add_childcare_ages_note = '' and the single one in eraseContactRequestReasonsIn (account_erasure_reasons.go) whose SET list is reason = ''", writes[0].kind)
}

// ident is R-6: exactly one direct call in each of the admin and tombstone
// a.contactMutationCtx callbacks. Each callback must be the last argument.
func (run *ctgRun) ident(f *ctgFile, id *ast.Ident, parents []ast.Node) {
	reasons := id.Name == "eraseContactRequestReasonsIn"
	if (id.Name != "eraseContactRequestTextIn" && !reasons) || len(parents) < 2 {
		return
	}
	switch p := parents[len(parents)-2].(type) {
	case *ast.FuncDecl:
		if p.Name == id {
			return
		}
	case *ast.CallExpr:
		if p.Fun == ast.Expr(id) {
			if reasons {
				run.reasonCalls++
				if run.reasonSites == nil {
					run.reasonSites = map[string]int{}
				}
				run.reasonSites[f.path]++
			} else {
				run.eraseCalls++
				if run.eraseSites == nil {
					run.eraseSites = map[string]int{}
				}
				run.eraseSites[f.path]++
			}
			if why := ctgEraseCallProblem(f, parents, p, reasons); why != "" {
				run.add(f, "R-6", id, parents, "%s", why)
			}
			return
		}
	}
	run.add(f, "R-6", id, parents, "%s used as a value (assigned, passed, returned, stored or turned into a method value); only a direct call is allowed", id.Name)
}

// ctgEraseCallProblem checks one call site against R-6. parents ends with the
// identifier; the call is its parent.
func ctgEraseCallProblem(f *ctgFile, parents []ast.Node, call *ast.CallExpr, reasons bool) string {
	path := f.path
	ci := len(parents) - 2
	lit := -1
	for i := ci - 1; i >= 0 && lit < 0; i-- {
		switch parents[i].(type) {
		case *ast.GoStmt, *ast.DeferStmt:
			return "called in a go or defer statement"
		case *ast.FuncLit:
			lit = i
		}
	}
	if lit < 1 {
		return "called outside an allowed a.contactMutationCtx callback"
	}
	fl, ok := parents[lit].(*ast.FuncLit)
	if !ok {
		return "called outside an allowed a.contactMutationCtx callback"
	}
	outer, ok := parents[lit-1].(*ast.CallExpr)
	if !ok {
		return "called in a func literal that is not the a.contactMutationCtx callback"
	}
	osel, ok := outer.Fun.(*ast.SelectorExpr)
	isArg := len(outer.Args) > 0 && outer.Args[len(outer.Args)-1] == ast.Expr(fl)
	if !ok || osel.Sel.Name != "contactMutationCtx" || !isArg {
		return "called in a func literal that is not the last argument of a.contactMutationCtx (a nested tx.Transaction body and the old a.contactMutation are not allowed)"
	}
	var decl *ast.FuncDecl
	for i := lit - 2; i >= 0 && decl == nil; i-- {
		switch n := parents[i].(type) {
		case *ast.FuncLit:
			return "the a.contactMutationCtx call is itself inside a func literal"
		case *ast.FuncDecl:
			decl = n
		}
	}
	admin := decl != nil && decl.Name.Name == "apiAdminDeleteUser" && path == "api_admin.go"
	tombstone := decl != nil && decl.Name.Name == "eraseTombstoneOnce" && path == "account_erasure_tombstones.go"
	if reasons && !admin {
		return "eraseContactRequestReasonsIn may be called only from apiAdminDeleteUser in api_admin.go (the legacy review adds its own site in T07B-3a)"
	}
	if !admin && !tombstone {
		return "called from a function other than apiAdminDeleteUser in api_admin.go or eraseTombstoneOnce in account_erasure_tombstones.go"
	}
	if !ctgIsAppReceiver(decl, osel.X) {
		return "the callback must be passed to contactMutationCtx on the allowed method's own App receiver, not on another value that has a method of that name"
	}
	viaExpr := false
	stmt := -1
	for i := ci - 1; i > lit && stmt < 0; i-- {
		switch parents[i].(type) {
		case *ast.ParenExpr:
		case *ast.BinaryExpr, *ast.UnaryExpr:
			viaExpr = true
		case *ast.ExprStmt, *ast.AssignStmt:
			if viaExpr {
				return "call must be a whole expression statement, an assignment or an if init or condition"
			}
			stmt = i
		case *ast.IfStmt:
			stmt = i
		default:
			return "call must sit directly in the callback body as an expression statement, an assignment or the init or condition of an if statement"
		}
	}
	if stmt > 0 {
		if is, ok := parents[stmt-1].(*ast.IfStmt); ok && is.Init != nil && ast.Node(is.Init) == parents[stmt] {
			stmt--
		}
	}
	if stmt != lit+2 || parents[lit+1] != ast.Node(fl.Body) {
		return "call must be a statement of the callback body itself, not nested in a block"
	}
	if fl.Type.Params == nil || len(fl.Type.Params.List) != 1 || len(fl.Type.Params.List[0].Names) != 1 {
		return "the callback must take one named *gorm.DB parameter"
	}
	param := ctgObj(fl.Type.Params.List[0].Names[0])
	if arg, ok := ctgFirstArg(call).(*ast.Ident); !ok || param == nil || ctgObj(arg) != param {
		return "the first argument must be the callback's own *gorm.DB parameter, not a local that reuses its name"
	}
	if tombstone {
		return ctgTombstoneLockProblem(f, fl, param, parents[stmt])
	}
	callAt, deleteAt := -1, -1
	for i, s := range fl.Body.List {
		if ast.Node(s) == parents[stmt] {
			callAt = i
		}
		if deleteAt >= 0 {
			continue
		}
		ast.Inspect(s, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) != 1 {
				return true
			}
			dsel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || dsel.Sel.Name != "Delete" {
				return true
			}
			recv, ok := dsel.X.(*ast.Ident)
			arg, aok := c.Args[0].(*ast.Ident)
			if ok && ctgObj(recv) == param && aok && arg.Name == "u" {
				deleteAt = i
			}
			return true
		})
	}
	if deleteAt < 0 {
		return "no tx.Delete(u) statement in the callback to come after"
	}
	if callAt <= deleteAt {
		return "call must come after the statement containing tx.Delete(u)"
	}
	if reasons {
		// Directly after the ages-note scrub: the statement before it holds the
		// eraseContactRequestTextIn call.
		prev := callAt - 1
		// An error check on a separately assigned result sits between them.
		if prev > 0 && !ctgHasCall(fl.Body.List[prev], "eraseContactRequestTextIn") {
			if is, ok := fl.Body.List[prev].(*ast.IfStmt); ok && is.Init == nil {
				prev--
			}
		}
		if prev < 0 || !ctgHasCall(fl.Body.List[prev], "eraseContactRequestTextIn") {
			return "call must be the statement directly after the eraseContactRequestTextIn call"
		}
	}
	return ""
}

// ctgHasCall reports whether n contains a call of the package function name.
func ctgHasCall(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if c, ok := m.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// The tombstone allowance requires the direct lock assignment and both
// refusal checks before the scrub. This is a syntactic check of the current
// production shape, not a claim to prove arbitrary control flow.
func ctgTombstoneLockProblem(f *ctgFile, fl *ast.FuncLit, param *ctgObject, scrub ast.Node) string {
	for i, stmt := range fl.Body.List {
		if ast.Node(stmt) == scrub {
			break
		}
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
			continue
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			continue
		}
		tx, txOK := call.Args[0].(*ast.Ident)
		if !ctgPackageIdent(f, call.Fun, "lockTombstoneIn") || !txOK || ctgObj(tx) != param {
			continue
		}
		found, foundOK := assign.Lhs[0].(*ast.Ident)
		err, errOK := assign.Lhs[1].(*ast.Ident)
		if !foundOK || !errOK || ctgObj(found) == nil || ctgObj(err) == nil || i+3 >= len(fl.Body.List) {
			continue
		}
		// Neither a nested check nor an unchecked lock grants this allowance.
		errCheck, ok := fl.Body.List[i+1].(*ast.IfStmt)
		if !ok || errCheck.Init != nil || errCheck.Else != nil {
			continue
		}
		cond, ok := errCheck.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.NEQ || ctgObj(ctgIdent(cond.X)) != ctgObj(err) || ctgIdent(cond.Y) == nil || ctgIdent(cond.Y).Name != "nil" {
			continue
		}
		if len(errCheck.Body.List) != 1 {
			continue
		}
		ret, ok := errCheck.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || ctgObj(ctgIdent(ret.Results[0])) != ctgObj(err) {
			continue
		}
		foundCheck, ok := fl.Body.List[i+2].(*ast.IfStmt)
		if !ok || foundCheck.Init != nil || foundCheck.Else != nil || len(foundCheck.Body.List) != 1 {
			continue
		}
		absent, ok := foundCheck.Cond.(*ast.UnaryExpr)
		if !ok || absent.Op != token.NOT || ctgObj(ctgIdent(absent.X)) != ctgObj(found) {
			continue
		}
		ret, ok = foundCheck.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || !ctgPackageIdent(f, ret.Results[0], "errTombstoneNoop") {
			continue
		}
		for _, later := range fl.Body.List[i+3:] {
			if ast.Node(later) == scrub {
				return ""
			}
		}
	}
	return "call must follow lockTombstoneIn on the callback's own transaction and checks returning its error or errTombstoneNoop when absent"
}

func ctgPackageIdent(f *ctgFile, expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name && (ctgObj(id) == nil || ctgObj(id) == f.tree.Scope.Lookup(name))
}

// ctgIsAppReceiver reports whether x is the receiver of decl, a method on App
// or *App. It compares the parser's resolution, not the spelling, so a local
// such as a := directRunner{...} that reuses the receiver's name is another
// value.
func ctgIsAppReceiver(decl *ast.FuncDecl, x ast.Expr) bool {
	if decl.Recv == nil || len(decl.Recv.List) != 1 || len(decl.Recv.List[0].Names) != 1 {
		return false
	}
	fld := decl.Recv.List[0]
	t := ctgUnparen(fld.Type)
	if star, ok := t.(*ast.StarExpr); ok {
		t = ctgUnparen(star.X)
	}
	if id, ok := t.(*ast.Ident); !ok || id.Name != "App" {
		return false
	}
	recv, use := ctgObj(fld.Names[0]), ctgObj(ctgIdent(ctgUnparen(x)))
	return recv != nil && use == recv
}

func ctgFirstArg(call *ast.CallExpr) ast.Expr {
	if len(call.Args) == 0 {
		return nil
	}
	return call.Args[0]
}

// ctgViolations checks production sources keyed by slash path from the module
// root. Test files are skipped, as walkProductionGo skips them.
func ctgViolations(sources map[string]string) []string {
	run := &ctgRun{}
	fset := token.NewFileSet()
	paths := make([]string, 0, len(sources))
	for path := range sources {
		if !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		tree, err := parser.ParseFile(fset, path, sources[path], 0)
		if err != nil {
			run.bad = append(run.bad, path+": cannot parse: "+err.Error())
			continue
		}
		f := newCtgFile(path, fset, tree)
		run.imports(f)
		run.file(f)
	}
	if run.eraseCalls != 2 || run.eraseSites["api_admin.go"] != 1 || run.eraseSites["account_erasure_tombstones.go"] != 1 {
		run.bad = append(run.bad, fmt.Sprintf("production tree: package level: R-6: eraseContactRequestTextIn is called %d times, want exactly two calls, one in apiAdminDeleteUser (api_admin.go) and one in eraseTombstoneOnce (account_erasure_tombstones.go), each in its own a.contactMutationCtx callback", run.eraseCalls))
	}
	if run.reasonCalls != 1 || run.reasonSites["api_admin.go"] != 1 {
		run.bad = append(run.bad, fmt.Sprintf("production tree: package level: R-6: eraseContactRequestReasonsIn is called %d times, want exactly one call, in apiAdminDeleteUser (api_admin.go), in its a.contactMutationCtx callback directly after eraseContactRequestTextIn", run.reasonCalls))
	}
	sort.Strings(run.bad)
	return run.bad
}

func TestContactRequestTextChangesStayInTheirHelpers(t *testing.T) {
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error {
		sources[path] = string(src)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bad := ctgViolations(sources)
	for _, v := range bad {
		t.Error(v)
	}
	if len(bad) > 0 {
		t.Log(ctgLimits)
	}
}

// The clean fixture tree holds each allowed site once: the creation functions,
// the one ages-note UPDATE in its plain batch loop, the two callback calls,
// the testdb reset literal, and the statement_timeout set_config calls. Each
// bypass below changes it in one way.

const ctgAgesUpdate = `"UPDATE contact_requests SET add_childcare_ages_note = '' WHERE id IN ? AND purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> ''"`

const ctgErasureLoop = "res := tx.Exec(" + ctgAgesUpdate + ", chunk, s.ID)\n\t\ttotal += res.RowsAffected"

const ctgEraseCall = "if err := eraseContactRequestTextIn(tx, s); err != nil {\n\t\t\treturn err\n\t\t}"

const ctgResetCall = `b.WriteString("SET LOCAL session_replication_role = replica;")`

// The reason helper's one UPDATE literal, its one call (directly after the
// ages-note call, in the admin callback) and its file (T07B-2b).
const ctgReasonUpdate = `"UPDATE contact_requests SET reason = '' WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?)"`

const ctgReasonLoop = "res := tx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID)\n\t\ttotal += res.RowsAffected"

const ctgReasonCall = "if err := eraseContactRequestReasonsIn(tx, s, plan); err != nil {\n\t\t\treturn err\n\t\t}"

func ctgReasonsFile(loop string) string {
	return `package main

import "gorm.io/gorm"

func eraseContactRequestReasonsIn(tx *gorm.DB, s erasureSubject, plan reasonPlan) error {
	var total int64
	for _, batch := range plan.batches {
		` + loop + `
	}
	_ = total
	return nil
}
`
}

func ctgErasureFile(loop string) string {
	return `package main

import (
	"fmt"

	"gorm.io/gorm"
)

func eraseContactRequestTextIn(tx *gorm.DB, s erasureSubject) error {
	var ids []uint
	if err := tx.Raw("SELECT id FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> '' ORDER BY id FOR UPDATE", s.ID).Scan(&ids).Error; err != nil {
		return err
	}
	var total int64
	for start := 0; start < len(ids); start += lookupChunk {
		chunk := ids[start:min(start+lookupChunk, len(ids))]
		` + loop + `
	}
	_ = fmt.Sprint(total)
	return nil
}
`
}

func ctgAdminFile(before, after string) string {
	return ctgAdminFileShape("err := a.contactMutationCtx(ctx, contactChange{UserID: u.ID}, ", ")", before, after)
}

// ctgAdminFileShape builds the admin fixture with the call's head (up to the
// callback) and the text after the callback's closing brace.
func ctgAdminFileShape(head, tail, before, after string) string {
	// Every fixture that calls the ages-note helper gets the reason helper's call
	// directly after it, as the production callback has, unless it brings its own.
	if strings.Contains(after, "eraseContactRequestTextIn") && !strings.Contains(after, "eraseContactRequestReasonsIn") && !strings.Contains(after, "// no reasons call") {
		after += "\n\t\t" + ctgReasonCall
	}
	return `package main

import (
	"fold/models"

	"gorm.io/gorm"
)

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), adminDeleteBudget)
	defer cancel()
	` + head + `func(tx *gorm.DB) error {
		` + before + `
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(u).Error; err != nil {
			return err
		}
		` + after + `
		return nil
	}` + tail + `
	_ = err
}
`
}

const ctgTombstoneLock = `found, err := lockTombstoneIn(tx, id)
		if err != nil {
			return err
		}
		if !found {
			return errTombstoneNoop
		}`

func ctgTombstoneFile(before, after string) string {
	return `package main

import "gorm.io/gorm"

func (a *App) eraseTombstoneOnce(ctx context.Context, id uint, version int) (tombstoneReport, error) {
	err := a.contactMutationCtx(ctx, contactChange{UserID: id}, func(tx *gorm.DB) error {
		` + before + `
		` + ctgTombstoneLock + `
		` + after + `
		return nil
	})
	return tombstoneReport{}, err
}
`
}

func ctgTestdbFile(extra string) string {
	return `package testdb

import (
	"strings"

	"gorm.io/gorm"
)

func resetStatements(db *gorm.DB) (string, string, error) {
	var b strings.Builder
	` + ctgResetCall + `
	for _, t := range tables {
		b.WriteString(" DELETE FROM " + t + ";")
	}
	return b.String(), "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE", nil
}
` + extra
}

const ctgContactFile = `package main

import (
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) apiCreateContactRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		r := models.ContactRequest{Purpose: "contact", Reason: strings.TrimSpace(body.Reason)}
		var count int64
		if err = tx.Model(&models.ContactRequest{}).Where("requester_id = ? AND status = ?", 1, "pending").Count(&count).Error; err != nil {
			return err
		}
		return tx.Create(&r).Error
	})
	_ = err
}

func (a *App) apiCreateAddRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		r := models.ContactRequest{Purpose: "group_add", Reason: strings.TrimSpace(body.Reason), AddOverLimit: body.OverLimit}
		r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote
		if body.ChildcareAgesNote != nil {
			r.AddChildcareAgesNote = strings.TrimSpace(*body.ChildcareAgesNote)
		}
		return tx.Create(&r).Error
	})
	_ = err
}

func (a *App) apiDecideContactRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		var r models.ContactRequest
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
			return err
		}
		r.Status = "approved"
		r.EndReason = "decided"
		return tx.Save(&r).Error
	})
	_ = err
}

func presentAddRequest(r models.ContactRequest) addRequestJSON {
	out := addRequestJSON{Reason: r.Reason}
	out.Reason = ""
	return out
}

func finishRequests(tx *gorm.DB, r *models.ContactRequest, rs []models.ContactRequest) error {
	if err := tx.Save(rs).Error; err != nil {
		return err
	}
	return tx.Save(r).Error
}
`

const ctgTimeoutFile = `package main

import "gorm.io/gorm"

func withStatementTimeout(tx *gorm.DB) error {
	var prior string
	if err := tx.Raw("SELECT current_setting('statement_timeout')").Scan(&prior).Error; err != nil {
		return err
	}
	if err := tx.Exec("SELECT set_config('statement_timeout', ?, true)", "5000ms").Error; err != nil {
		return err
	}
	return tx.Exec("SELECT set_config('statement_timeout', ?, true)", prior).Error
}
`

const ctgLifecycleFile = `package main

import "gorm.io/gorm"

func (a *App) contactMutationCtx(tx *gorm.DB, remaining string) error {
	return tx.Exec("SELECT set_config('statement_timeout', ?, true), set_config('idle_in_transaction_session_timeout', ?, true)", "20000ms", remaining).Error
}
`

const ctgModelsFile = `package models

type ContactRequest struct {
	ID     uint
	Reason string
}

func All() []any { return []any{&ContactRequest{}} }
`

func ctgClean() map[string]string {
	return map[string]string{
		"account_erasure.go":            ctgErasureFile(ctgErasureLoop),
		"account_erasure_reasons.go":    ctgReasonsFile(ctgReasonLoop),
		"account_erasure_tombstones.go": ctgTombstoneFile("", ctgEraseCall),
		"api_admin.go":                  ctgAdminFile("", ctgEraseCall),
		"api_contact.go":                ctgContactFile,
		"contact_lifecycle.go":          ctgLifecycleFile,
		"internal/testdb/testdb.go":     ctgTestdbFile(""),
		"statement_timeout.go":          ctgTimeoutFile,
		"models/models.go":              ctgModelsFile,
		"other.go":                      "package main\n\nfunc other() {}\n",
	}
}

// ctgBodyFile wraps statements in a function that has a loaded request r, a
// pointer p, a slice rs, a map m and a conflict clause oc in scope.
func ctgBodyFile(body string) string {
	return "package main\n\nimport (\n\t\"fold/models\"\n\n\t\"gorm.io/gorm\"\n\t\"gorm.io/gorm/clause\"\n)\n\n" +
		"func other(tx *gorm.DB, p *models.ContactRequest, rs []models.ContactRequest, m map[string]any, oc clause.OnConflict) {\n" +
		"\tvar r models.ContactRequest\n\t" + body + "\n}\n"
}

type ctgCase struct {
	name  string
	rule  string            // the rule that must reject it; empty means the tree must stay clean
	files map[string]string // replaces or adds files in the clean tree; "-" removes one
	want  string            // the file the violation must name (default other.go)
}

func ctgBodies(rule string, bodies ...string) []ctgCase {
	cases := make([]ctgCase, 0, len(bodies))
	for _, b := range bodies {
		cases = append(cases, ctgCase{name: b, rule: rule, files: map[string]string{"other.go": ctgBodyFile(b)}})
	}
	return cases
}

func ctgSource(name, rule, want string, files map[string]string) ctgCase {
	return ctgCase{name: name, rule: rule, want: want, files: files}
}

func ctgRunCase(t *testing.T, tc ctgCase, covered map[string]bool) {
	t.Helper()
	sources := ctgClean()
	for path, src := range tc.files {
		if src == "-" {
			delete(sources, path)
		} else {
			sources[path] = src
		}
	}
	bad := ctgViolations(sources)
	for _, v := range bad {
		if strings.Contains(v, "cannot parse") {
			t.Errorf("%s: the fixture does not parse: %s", tc.name, v)
			return
		}
	}
	if tc.rule == "" {
		if len(bad) != 0 {
			t.Errorf("%s: want no violation, got %v", tc.name, bad)
		}
		return
	}
	want := tc.want
	if want == "" {
		want = "other.go"
	}
	for _, v := range bad {
		if strings.HasPrefix(v, want) && strings.Contains(v, ": "+tc.rule+": ") {
			covered[tc.rule] = true
			return
		}
	}
	t.Errorf("%s: want a %s violation in %s, got %v", tc.name, tc.rule, want, bad)
}

func TestContactRequestTextGuardCatchesBypasses(t *testing.T) {
	if bad := ctgViolations(ctgClean()); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	if !strings.Contains(ctgLimits, "'DELETE FROM ' + t") || !strings.Contains(ctgLimits, "not a type checker") {
		t.Fatal("the limits text must say that computed-table-name SQL such as internal/testdb's 'DELETE FROM ' + t is not seen, and that the guard is not a type checker")
	}
	admin := ctgAdminFile("", ctgEraseCall)
	contactWith := func(old, replacement string) string {
		if !strings.Contains(ctgContactFile, old) {
			t.Fatalf("fixture anchor %q is missing from the clean api_contact.go", old)
		}
		return strings.Replace(ctgContactFile, old, replacement, 1)
	}
	testdbWith := func(old, replacement string) string {
		clean := ctgTestdbFile("")
		if !strings.Contains(clean, old) {
			t.Fatalf("fixture anchor %q is missing from the clean testdb.go", old)
		}
		return strings.Replace(clean, old, replacement, 1)
	}
	testdbOnly := func(source string) map[string]string {
		return map[string]string{"internal/testdb/testdb.go": source}
	}
	var cases []ctgCase

	// R-1: field writes on a bound ContactRequest value.
	cases = append(cases, ctgBodies("R-1",
		`r.Reason = ""`,
		`r.AddChildcareAgesNote = ""`,
		`rs[0].Reason = ""`,
		`p.Reason = ""`,
		`(*p).Reason = ""`,
		`r.Reason += "x"`,
		`r.Reason, r.Status = "", "x"`,
		`q := &rs[0]; q.Reason = ""`,
		`v := &r; v.AddChildcareAgesNote = ""`,
		`for _, x := range rs { x.AddChildcareAgesNote = "" }`,
		// T07A-GR-01: parentheses around the assigned selector change nothing.
		`(r.Reason) = ""`,
		`(r.AddChildcareAgesNote) = ""`,
		`((r.Reason)) = ""`,
		`(r).Reason = ""`,
		`(rs[0].Reason) = ""`,
		`(p.Reason) = ""`,
		`((*p).AddChildcareAgesNote) = ""`,
		`(r.Reason) += "x"`,
		`(r.Reason), r.Status = "", "x"`,
		`if err := tx.First(&r, 1).Error; err != nil { return }; (r.Reason) = ""; tx.Save(&r)`,
		`if err := tx.First(&r, 1).Error; err != nil { return }; (r.AddChildcareAgesNote) = ""; tx.Save(&r)`,
		// T07A-GR-03: a closure that captures the request is the same request, and a
		// block-scoped request is still a request.
		`func() { r.Reason = "" }()`,
		`{ r := models.ContactRequest{}; r.Reason = "" }`,
		`func(r models.ContactRequest) { r.Reason = "" }(r)`,
	)...)
	cases = append(cases,
		ctgSource("Reason assigned inside a creation function", "R-1", "api_contact.go", map[string]string{
			"api_contact.go": contactWith("r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote",
				"r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote\n\t\tr.Reason = \"x\""),
		}),
		ctgSource("ages note assigned in the other creation function", "R-1", "api_contact.go", map[string]string{
			"api_contact.go": contactWith("var count int64", "r.AddChildcareAgesNote = \"x\"\n\t\tvar count int64"),
		}),
		ctgSource("a package models method body", "R-1", "models/other.go", map[string]string{
			"models/other.go": "package models\n\nfunc f() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
		ctgSource("aliased models import", "R-1", "", map[string]string{
			"other.go": "package main\n\nimport (\n\tmm \"fold/models\"\n)\n\nfunc other() {\n\tvar r mm.ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
		ctgSource("dot-imported models", "R-1", "", map[string]string{
			"other.go": "package main\n\nimport . \"fold/models\"\n\nfunc other() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
	)

	// R-2: literals that set a protected field, and literal overwrites.
	cases = append(cases, ctgBodies("R-2",
		`_ = models.ContactRequest{Reason: "x"}`,
		`_ = &models.ContactRequest{AddChildcareAgesNote: "x"}`,
		`rs = []models.ContactRequest{{Reason: "x"}}`,
		`_ = []*models.ContactRequest{{Reason: "x"}}`,
		`_ = map[string]models.ContactRequest{"a": {Reason: "x"}}`,
		`_ = models.ContactRequest{1, 2}`,
		`*p = models.ContactRequest{ID: r.ID}`,
		`rs[0] = models.ContactRequest{ID: 1}`,
		`r = models.ContactRequest{ID: r.ID}`,
		`tx.Save(&models.ContactRequest{ID: 1})`,
	)...)
	cases = append(cases,
		ctgSource("a creation function name in another file", "R-2", "api_other.go", map[string]string{
			"api_other.go": "package main\n\nimport \"fold/models\"\n\nfunc apiCreateAddRequest() { _ = models.ContactRequest{Reason: \"x\"} }\n",
		}),
		ctgSource("a third function in api_contact.go", "R-2", "api_contact.go", map[string]string{
			"api_contact.go": ctgContactFile + "\nfunc (a *App) apiCreateGroupAsk() { _ = models.ContactRequest{Reason: \"x\"} }\n",
		}),
		ctgSource("ages note literal in the contact creator", "R-2", "api_contact.go", map[string]string{
			"api_contact.go": contactWith(`Purpose: "contact",`, `Purpose: "contact", AddChildcareAgesNote: "x",`),
		}),
	)

	// R-3: GORM update chains on a contact_requests target (kinds a to d).
	cases = append(cases, ctgBodies("R-3",
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).Update("reason", "")`,
		`tx.Table("contact_requests").Where("id = ?", 1).UpdateColumn("reason", "")`,
		`tx.Table("public.contact_requests").Update("reason", "")`,
		`tx.Table("contact_requests AS c").Update("reason", "")`,
		`tx.Table("\"public\".\"contact_requests\"").Update("reason", "")`,
		`tx.Model(&r).Updates(map[string]any{"reason": ""})`,
		`tx.Model(&r).UpdateColumns(map[string]any{"add_childcare_ages_note": ""})`,
		`tx.Model(&r).Updates(map[string]any{"Reason": ""})`,
		`tx.Model(&r).Updates(models.ContactRequest{Reason: "x"})`,
		`tx.Model(&r).Updates(&models.ContactRequest{AddChildcareAgesNote: "x"})`,
		`tx.Model(&r).Updates(m)`,
		`tx.Model(&r).Updates(r)`,
		`tx.Where("id = ?", 1).Updates(&r)`,
		`tx.Where("id = ?", 1).UpdateColumns(r)`,
		`tx.Where("id = ?", 1).Updates(&models.ContactRequest{Reason: ""})`,
		`q := tx.Model(&r).Where("id = ?", 1); q.Update("reason", "")`,
		`tx.Unscoped().Model(&r).Update("Reason", "")`,
		`tx.Model(&r).Update("contact_requests.reason", "")`,
		`col := "reason"; tx.Model(&r).Update(col, "")`,
		// T07A-GR-03: a same-named q declared in a closure is another variable, so
		// the outer q keeps its contact_requests prefix; a closure that captures q
		// still sees it; a shadowing q that holds the request chain is read too.
		`q := tx.Model(&r).Where("id = ?", id); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Update("reason", "")`,
		`q := tx.Model(&r).Where("id = ?", id); { q := tx.Model(&models.Setting{}); _ = q }; q.Update("reason", "")`,
		`q := tx.Model(&r); func() { q.Update("reason", "") }()`,
		`q := tx.Model(&models.Setting{}); _ = q; func() { q := tx.Model(&r); q.Update("reason", "") }()`,
		`var q *gorm.DB; q = tx.Model(&r); q.Update("reason", "")`,
	)...)
	cases = append(cases,
		ctgSource("aliased models import", "R-3", "", map[string]string{
			"other.go": "package main\n\nimport (\n\tmm \"fold/models\"\n)\n\nfunc other() {\n\tvar r mm.ContactRequest\n\ttx.Model(&r).Updates(map[string]any{\"reason\": \"\"})\n}\n",
		}),
		ctgSource("dot-imported models", "R-3", "", map[string]string{
			"other.go": "package main\n\nimport . \"fold/models\"\n\nfunc other() {\n\tvar r ContactRequest\n\ttx.Model(&r).Update(\"reason\", \"\")\n}\n",
		}),
	)

	// R-4a: conflict clauses, including UpdateAll.
	cases = append(cases, ctgBodies("R-4a",
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"reason"})}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"add_childcare_ages_note"})}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"reason": ""})}).CreateInBatches(&rs, 100)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Save(&r)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.ContactRequest{ID: 1})`,
		`tx.Clauses(oc).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: someSet}).Create(&r)`,
		// T07A-GR-03: the conflict clause sits in a once-assigned q whose name is also
		// declared in a closure.
		`q := tx.Clauses(clause.OnConflict{UpdateAll: true}); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Create(&r)`,
	)...)
	cases = append(cases,
		ctgSource("UpdateAll inside both creation functions", "R-4a", "api_contact.go", map[string]string{
			"api_contact.go": strings.ReplaceAll(ctgContactFile, "return tx.Create(&r).Error", "return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error"),
		}),
		ctgSource("conflict clause through a cl alias", "R-4a", "", map[string]string{
			"other.go": "package main\n\nimport (\n\t\"fold/models\"\n\tcl \"gorm.io/gorm/clause\"\n)\n\nfunc other(tx *gorm.DB) {\n\tvar r models.ContactRequest\n\ttx.Clauses(cl.OnConflict{UpdateAll: true}).Create(&r)\n}\n",
		}),
	)

	// R-4b: find-or-create forms.
	cases = append(cases, ctgBodies("R-4b",
		`tx.Where("id = ?", 1).Assign(map[string]any{"reason": ""}).FirstOrCreate(&r)`,
		`tx.Where("id = ?", 1).Assign(models.ContactRequest{Reason: ""}).FirstOrCreate(&models.ContactRequest{})`,
		`tx.Where(map[string]any{"reason": "x"}).FirstOrCreate(&r)`,
		`tx.FirstOrCreate(&r, map[string]any{"reason": "x"})`,
		`tx.Attrs(map[string]any{"add_childcare_ages_note": ""}).FirstOrInit(&r)`,
		`tx.Assign(m).FirstOrCreate(&r)`,
		`tx.Where("reason = ?", "x").FirstOrCreate(&r)`,
		`tx.Where("id = ?", 1).Attrs(models.ContactRequest{Reason: "x"}).FirstOrCreate(&r)`,
		`tx.Where(m).FirstOrCreate(&r)`,
		`q := tx.Where("reason = ?", "x"); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.FirstOrCreate(&r)`,
	)...)

	// R-4c: Select before a write.
	cases = append(cases, ctgBodies("R-4c",
		`tx.Model(&r).Select("reason").Updates(map[string]any{"name": ""})`,
		`tx.Select("reason").Save(&r)`,
		`tx.Select("*").Save(&r)`,
		`tx.Select(cols).Save(&r)`,
		`tx.Select([]string{"reason"}).Save(&r)`,
		`q := tx.Select("reason"); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Save(&r)`,
	)...)

	// R-5: raw SQL writes.
	cases = append(cases, ctgBodies("R-5",
		`tx.Exec("UPDATE contact_requests SET reason = ''")`,
		"tx.Exec(`UPDATE public.\"contact_requests\" AS c SET \"reason\" = ''`)",
		`tx.Exec("UPDATE contact_requests SET add_childcare_ages_note = '', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'x' WHERE reason <> ''")`,
		`tx.Exec("UPDATE ONLY contact_requests SET reason = ''")`,
		`tx.Exec("INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET reason = ''")`,
		`tx.Exec("MERGE INTO contact_requests c USING x ON c.id = x.id WHEN MATCHED THEN UPDATE SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests " + "SET reason = ''")`,
		`const q = "UPDATE contact_requests SET "; tx.Exec(q + "reason = ''")`,
		`tx.Exec(`+ctgAgesUpdate+`, 1, 2)`,
		// T07A-GR-02: a semicolon inside a quote, comment or dollar quote does not end
		// the statement, so the SET list is read to its real end.
		`tx.Exec("UPDATE contact_requests SET status = status || left(';', 0), reason = '' WHERE purpose = 'contact'")`,
		`tx.Exec("UPDATE contact_requests SET status = 'a;b', add_childcare_ages_note = '' WHERE id = 1")`,
		`tx.Exec("UPDATE contact_requests SET status = 'it''s;', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = E'\\';', reason = ''")`,
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0), reason = '' WHERE purpose = 'contact'`)",
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0), add_childcare_ages_note = '' WHERE id = 1`)",
		`tx.Exec("UPDATE contact_requests SET status = $$;$$, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = $q$ ; $q$, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = status -- ;\n, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = status /* ; */, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'open, reason = ''")`,
		`tx.Exec("INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET status = ';', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'x'; UPDATE contact_requests SET reason = ''")`,
		// T07A-GFR-01: a semicolon inside a double-quoted identifier does not end the
		// statement either, so a SET list after the alias is still read.
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET reason = '' WHERE purpose = 'contact'`)",
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET add_childcare_ages_note = '' WHERE id = 1`)",
		"tx.Exec(`UPDATE \"contact_requests\" AS \"r;kept\" SET \"reason\" = ''`)",
		"tx.Exec(`UPDATE contact_requests SET \"st;atus\" = 'x', reason = ''`)",
		"tx.Exec(`UPDATE contact_requests AS \"r\"\";kept\" SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests AS \"r;kept SET reason = ''`)",
		"tx.Exec(`INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET \"a;b\" = 1, reason = ''`)",
		// T07A-GFR-02: a constant that a closure or block redeclares is its own
		// constant; the use after the closure still reads the outer one.
		`const q = "UPDATE contact_requests SET "; func() { const q = "UPDATE settings SET "; _ = q }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE settings SET "; func() { const q = "UPDATE contact_requests SET "; tx.Exec(q + "reason = ''") }()`,
		`const q = "UPDATE contact_requests SET "; { const q = "UPDATE settings SET "; _ = q }; tx.Exec(q + "reason = ''")`,
		`const a = "UPDATE contact_requests "; const q = a + "SET "; func() { const a = "UPDATE settings "; _ = a }(); tx.Exec(q + "reason = ''")`,
	)...)
	cases = append(cases,
		ctgSource("a package constant used by a function", "R-5", "other.go", map[string]string{
			"other.go": "package main\n\nconst q = \"UPDATE contact_requests SET \"\n\nfunc other(tx *gorm.DB) {\n\ttx.Exec(q + \"reason = ''\")\n}\n",
		}),
		ctgSource("a package constant declared after its use", "R-5", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other(tx *gorm.DB) {\n\ttx.Exec(q + \"reason = ''\")\n}\n\nconst q = \"UPDATE contact_requests SET \"\n",
		}),
		ctgSource("a second literal in eraseContactRequestTextIn that sets reason", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop + "\n\t\ttx.Exec(\"UPDATE contact_requests SET reason = '' WHERE id = 1\")"),
		}),
		ctgSource("the ages literal in a func literal", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile("func() { tx.Exec(" + ctgAgesUpdate + ", chunk, s.ID) }()"),
		}),
		ctgSource("the ages literal twice", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop + "\n\t\ttx.Exec(" + ctgAgesUpdate + ", chunk, s.ID)"),
		}),
		ctgSource("the ages literal with reason in its SET list", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile("res := tx.Exec(\"UPDATE contact_requests SET add_childcare_ages_note = '', reason = '' WHERE id IN ?\", chunk)\n\t\ttotal += res.RowsAffected"),
		}),
		ctgSource("the ages literal in another function of account_erasure.go", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop) + "\nfunc eraseElsewhere(tx *gorm.DB) { tx.Exec(" + ctgAgesUpdate + ", 1, 2) }\n",
		}),
		ctgSource("the ages literal in a copy of the helper in another file", "R-5", "api_other.go", map[string]string{
			"account_erasure.go": ctgErasureFile("total++"),
			"api_other.go":       "package main\n\nfunc eraseContactRequestTextInCopy(tx *gorm.DB) { tx.Exec(" + ctgAgesUpdate + ", 1, 2) }\n",
		}),
	)

	// T07B-2b: the reason column is blanked by one literal in
	// eraseContactRequestReasonsIn and nowhere else.
	reasons := ctgAdminFile("", ctgEraseCall)
	cases = append(cases,
		ctgSource("a second reason literal in the helper", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile(ctgReasonLoop + "\n\t\ttx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID)"),
		}),
		ctgSource("the reason literal in a func literal", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("func() { tx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID) }()"),
		}),
		ctgSource("the reason literal in another function of the file", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile(ctgReasonLoop) + "\nfunc blankElsewhere(tx *gorm.DB) { tx.Exec(" + ctgReasonUpdate + ", 1, 2, 3) }\n",
		}),
		ctgSource("the reason literal in another file", "R-5", "api_other.go", map[string]string{
			"api_other.go": "package main\n\nfunc blank(tx *gorm.DB) { tx.Exec(" + ctgReasonUpdate + ", 1, 2, 3) }\n",
		}),
		ctgSource("the reason literal with the ages note in its SET list", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("res := tx.Exec(\"UPDATE contact_requests SET reason = '', add_childcare_ages_note = '' WHERE id = ANY(?::bigint[])\", batch)\n\t\ttotal += res.RowsAffected"),
		}),
		ctgSource("the reason literal setting another column too", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("res := tx.Exec(\"UPDATE contact_requests SET reason = '', status = 'x' WHERE id = ANY(?::bigint[])\", batch)\n\t\ttotal += res.RowsAffected"),
		}),
		ctgSource("the reason helper's call twice", "R-6", "production tree", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\t"+ctgReasonCall+"\n\t\t"+ctgReasonCall),
		}),
		ctgSource("no call of the reason helper", "R-6", "production tree", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\t// no reasons call"),
		}),
		ctgSource("the reason call before the ages-note call", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgReasonCall+"\n\t\t"+ctgEraseCall),
		}),
		ctgSource("a statement between the two calls", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\t_ = plan\n\t\t"+ctgReasonCall),
		}),
		ctgSource("the reason call before tx.Delete(u)", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile(ctgReasonCall, ctgEraseCall),
		}),
		ctgSource("the reason call passing a.DB", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\tif err := eraseContactRequestReasonsIn(a.DB, s, plan); err != nil {\n\t\t\treturn err\n\t\t}"),
		}),
		ctgSource("the reason call nested in a block", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\tif true {\n\t\t\t_ = eraseContactRequestReasonsIn(tx, s, plan)\n\t\t}"),
		}),
		ctgSource("the reason call in a go statement", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\tgo eraseContactRequestReasonsIn(tx, s, plan)"),
		}),
		ctgSource("the reason call from another function", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(reasons, "apiAdminDeleteUser", "apiAdminOtherThing", 1),
		}),
		ctgSource("the reason call from the tombstone callback", "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ctgEraseCall+"\n\t\t"+ctgReasonCall),
		}),
		ctgSource("the reason helper declared as a method", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": strings.Replace(ctgReasonsFile(ctgReasonLoop), "func eraseContactRequestReasonsIn(", "func (a *App) eraseContactRequestReasonsIn(", 1),
		}),
		ctgSource("the reason call on a copy of the callback's tx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\ttx2 := tx\n\t\tif err := eraseContactRequestReasonsIn(tx2, s, plan); err != nil {\n\t\t\treturn err\n\t\t}"),
		}),
		ctgSource("the reason call inside a closure of the callback", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\tfunc() { _ = eraseContactRequestReasonsIn(tx, s, plan) }()"),
		}),
		ctgSource("the reason call in a defer statement", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall+"\n\t\tdefer eraseContactRequestReasonsIn(tx, s, plan)"),
		}),
		ctgSource("the reason helper used as a value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\tf := eraseContactRequestReasonsIn\n\t_ = f\n}\n",
		}),
	)

	// R-6: each of the two named call sites, with no extra sites.
	call := func(statement string) string { return ctgAdminFile("", statement) }
	cases = append(cases,
		ctgSource("a second call", "R-6", "production tree", map[string]string{"api_admin.go": call(ctgEraseCall + "\n\t\t" + ctgEraseCall)}),
		ctgSource("no call at all", "R-6", "production tree", map[string]string{"api_admin.go": call("")}),
		ctgSource("a call before tx.Delete(u)", "R-6", "api_admin.go", map[string]string{"api_admin.go": ctgAdminFile(ctgEraseCall, "")}),
		ctgSource("a call passing a.DB", "R-6", "api_admin.go", map[string]string{"api_admin.go": call("if err := eraseContactRequestTextIn(a.DB, s); err != nil {\n\t\t\treturn err\n\t\t}")}),
		ctgSource("a call in a tx.Transaction body", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": call("if err := tx.Transaction(func(tx2 *gorm.DB) error {\n\t\t\treturn eraseContactRequestTextIn(tx2, s)\n\t\t}); err != nil {\n\t\t\treturn err\n\t\t}"),
		}),
		ctgSource("a go statement", "R-6", "api_admin.go", map[string]string{"api_admin.go": call("go eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a defer statement", "R-6", "api_admin.go", map[string]string{"api_admin.go": call("defer eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a call nested in a block", "R-6", "api_admin.go", map[string]string{"api_admin.go": call("if true {\n\t\t\t_ = eraseContactRequestTextIn(tx, s)\n\t\t}")}),
		ctgSource("a returned call", "R-6", "api_admin.go", map[string]string{"api_admin.go": call("return eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a call from another function of api_admin.go", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "apiAdminDeleteUser", "apiAdminOtherThing", 1),
		}),
		ctgSource("the call in another file", "R-6", "api_admin_extra.go", map[string]string{"api_admin.go": "-", "api_admin_extra.go": admin}),
		ctgSource("a call from any other function", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other(tx *gorm.DB, s erasureSubject) error {\n\treturn eraseContactRequestTextIn(tx, s)\n}\n",
		}),
		ctgSource("a function value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\tf := eraseContactRequestTextIn\n\t_ = f\n}\n",
		}),
		ctgSource("a method value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc (a *App) other() {\n\tf := a.eraseContactRequestTextIn\n\t_ = f\n}\n",
		}),
		ctgSource("an argument", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\trun(eraseContactRequestTextIn)\n}\n",
		}),
		ctgSource("a returned function", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() any {\n\treturn eraseContactRequestTextIn\n}\n",
		}),
		// T07A-GR-04: a method named contactMutation on another receiver is not the
		// transaction-and-gate wrapper, whether it is a different variable or a local
		// that reuses the receiver's name.
		ctgSource("contactMutation on another receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "err := a.contactMutationCtx(", "runner := directRunner{db: a.DB}\n\terr := runner.contactMutationCtx(", 1),
			"api_runner.go": "package main\n\nimport \"gorm.io/gorm\"\n\ntype directRunner struct{ db *gorm.DB }\n\n" +
				"func (d directRunner) contactMutationCtx(_ context.Context, _ contactChange, fn func(*gorm.DB) error) error { return fn(d.db) }\n",
		}),
		ctgSource("contactMutation on a local that shadows the receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(strings.Replace(admin, "err := a.contactMutationCtx(", "{\n\t\ta := directRunner{db: a.DB}\n\t\terr := a.contactMutationCtx(", 1), "_ = err\n}", "_ = err\n\t}\n}", 1),
			"api_runner.go": "package main\n\nimport \"gorm.io/gorm\"\n\ntype directRunner struct{ db *gorm.DB }\n\n" +
				"func (d directRunner) contactMutationCtx(_ context.Context, _ contactChange, fn func(*gorm.DB) error) error { return fn(d.db) }\n",
		}),
		ctgSource("contactMutation on a parameter, not the receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(strings.Replace(admin, "(a *App) apiAdminDeleteUser(c *gin.Context)", "(a *App) apiAdminDeleteUser(c *gin.Context, runner directRunner)", 1),
				"err := a.contactMutationCtx(", "err := runner.contactMutationCtx(", 1),
		}),
		// R4-CTX: the callback is the last argument of a.contactMutationCtx; the
		// old call shape fails at the admin site.
		ctgSource("the old a.contactMutation at the admin site", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileShape("err := a.contactMutation(contactChange{UserID: u.ID}, ", ")", "", ctgEraseCall),
		}),
		ctgSource("a callback that is not the last argument", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileShape("err := a.contactMutationCtx(ctx, contactChange{UserID: u.ID}, ", ", nil)", "", ctgEraseCall),
		}),
		ctgSource("apiAdminDeleteUser as a function, not an App method", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "func (a *App) apiAdminDeleteUser(", "func apiAdminDeleteUser(a *App, ", 1),
		}),
		ctgSource("apiAdminDeleteUser on another type", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "(a *App) apiAdminDeleteUser", "(a *Other) apiAdminDeleteUser", 1),
		}),
		// T07A-GFR-03: the first argument is the callback's own parameter, not a
		// local that reuses its name, and the same holds for the tx.Delete(u) that
		// the call must follow.
		ctgSource("an if-init local that shadows the callback tx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": call("if tx := a.DB; eraseContactRequestTextIn(tx, s) != nil {\n\t\t\treturn gorm.ErrInvalidDB\n\t\t}"),
		}),
		ctgSource("a callback parameter named _ and an unresolved tx argument", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "func(tx *gorm.DB)", "func(_ *gorm.DB)", 1),
		}),
		ctgSource("a tx.Delete(u) on a local that shadows the callback tx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "if err := tx.Delete(u).Error; err != nil {", "if tx := a.DB; tx.Delete(u).Error != nil {", 1),
		}),
	)
	tombstone := ctgTombstoneFile("", ctgEraseCall)
	tombstoneBad := []struct{ name, source string }{
		{"a tombstone call before its lock", ctgTombstoneFile(ctgEraseCall, "")},
		{"a tombstone call in another function", strings.Replace(tombstone, "eraseTombstoneOnce(", "other(", 1)},
		{"a tombstone call in runSweepPage", strings.Replace(tombstone, "eraseTombstoneOnce(", "runSweepPage(", 1)},
		{"a tombstone call in a nested transaction", ctgTombstoneFile("", "return tx.Transaction(func(tx2 *gorm.DB) error { return eraseContactRequestTextIn(tx2, s) })")},
		{"a tombstone call passing a.DB", strings.Replace(tombstone, "eraseContactRequestTextIn(tx, s)", "eraseContactRequestTextIn(a.DB, s)", 1)},
		{"a tombstone method on another type", strings.Replace(tombstone, "(a *App)", "(a *Other)", 1)},
		{"a tombstone callback not last", strings.Replace(tombstone, "\n\t})", "\n\t}, nil)", 1)},
		{"a tombstone call on another receiver", strings.Replace(tombstone, "a.contactMutationCtx", "runner.contactMutationCtx", 1)},
		{"a tombstone call in a job handler", strings.Replace(tombstone, ctgEraseCall, "return func(ctx context.Context) error { "+ctgEraseCall+"; return nil }(ctx)", 1)},
		{"a tombstone call nested in a block", ctgTombstoneFile("", "if true { "+ctgEraseCall+" }")},
		{"a tombstone go call", ctgTombstoneFile("", "go eraseContactRequestTextIn(tx, s)")},
		{"a tombstone deferred call", ctgTombstoneFile("", "defer eraseContactRequestTextIn(tx, s)")},
		{"a tombstone call with shadowed tx", ctgTombstoneFile("", "if tx := a.DB; eraseContactRequestTextIn(tx, s) != nil { return gorm.ErrInvalidDB }")},
		{"a tombstone lock on a.DB", strings.Replace(tombstone, "lockTombstoneIn(tx, id)", "lockTombstoneIn(a.DB, id)", 1)},
		{"a tombstone lock without error check", strings.Replace(tombstone, "if err != nil {\n\t\t\treturn err\n\t\t}", "", 1)},
		{"a tombstone lock without found check", strings.Replace(tombstone, "if !found {\n\t\t\treturn errTombstoneNoop\n\t\t}", "", 1)},
		{"a tombstone lock with reversed found check", strings.Replace(tombstone, "if !found", "if found", 1)},
		{"a tombstone lock with a swallowed error", strings.Replace(tombstone, "return err\n", "return nil\n", 1)},
		{"a shadowed tombstone lock helper", ctgTombstoneFile("lockTombstoneIn := otherLock", ctgEraseCall)},
		{"a shadowed tombstone no-op sentinel", ctgTombstoneFile("errTombstoneNoop := otherError", ctgEraseCall)},
	}
	for _, tc := range tombstoneBad {
		cases = append(cases, ctgSource(tc.name, "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": tc.source,
		}))
	}
	cases = append(cases,
		ctgSource("a second tombstone callback call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ctgEraseCall+"\n"+ctgEraseCall),
		}),
		ctgSource("no tombstone call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ""),
		}),
		ctgSource("two admin calls cannot replace the tombstone call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ""),
			"api_admin.go":                  call(ctgEraseCall + "\n" + ctgEraseCall),
		}),
		ctgSource("a third site in the tombstone file", "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": tombstone + "\nfunc other(tx *gorm.DB, s erasureSubject) error { return eraseContactRequestTextIn(tx, s) }\n",
		}),
	)

	// R-7: row deletion.
	cases = append(cases, ctgBodies("R-7",
		`tx.Delete(&r)`,
		`tx.Delete(&rs, 1)`,
		`tx.Unscoped().Delete(p)`,
		`tx.Where("id = ?", 1).Delete(&models.ContactRequest{})`,
		`tx.Table("contact_requests").Where("id = ?", 1).Delete(nil)`,
		`tx.Model(&r).Where("id = ?", 1).Delete(nil)`,
		// T07A-GR-03: the prefix survives a same-named q declared in a closure.
		`q := tx.Model(&r).Where("id = ?", 1); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Delete(nil)`,
		`q := tx.Table("contact_requests"); func() { q := tx.Table("settings"); _ = q }(); q.Delete(nil)`,
		`tx.Exec("DELETE FROM contact_requests WHERE id = 1")`,
		`tx.Exec("DELETE FROM ONLY public.contact_requests")`,
		`tx.Exec("TRUNCATE TABLE users, contact_requests")`,
	)...)
	cases = append(cases,
		ctgSource("aliased models import", "R-7", "", map[string]string{
			"other.go": "package main\n\nimport mm \"fold/models\"\n\nfunc other() {\n\ttx.Delete(&mm.ContactRequest{})\n}\n",
		}),
	)

	// RC-1 (b), (c) and (e): trigger and replication-role bypasses in Go literals.
	cases = append(cases, ctgBodies("RC-1(b)",
		`tx.Exec("ALTER TABLE contact_requests DISABLE TRIGGER add_request_identity")`,
		`tx.Exec("ALTER TABLE contact_requests ENABLE REPLICA TRIGGER add_request_identity")`,
		`tx.Exec("ALTER TABLE contact_requests ENABLE ALWAYS TRIGGER add_request_completed")`,
		`tx.Exec("DROP TRIGGER add_request_identity ON contact_requests")`,
		`tx.Exec("CREATE OR REPLACE FUNCTION add_request_identity() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql")`,
		`tx.Exec("CREATE FUNCTION public.add_request_completed() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql")`,
		`tx.Exec("SET LOCAL session_replication_role = replica")`,
		`tx.Exec("SET LOCAL session_replication_role = replica;")`,
		`tx.Exec("SELECT set_config(?, ?, true)", "session_replication_role", "replica")`,
		`tx.Exec("SELECT set_config('session_replication_role', 'replica', true)")`,
		`tx.Exec("SET LOCAL session_" + "replication_role = replica")`,
	)...)
	cases = append(cases,
		ctgSource("the reset literal in a resetStatements of another file", "RC-1(b)", "internal/testdb/reset.go", map[string]string{
			"internal/testdb/reset.go": "package testdb\n\nimport \"strings\"\n\nfunc resetStatements() {\n\tvar b strings.Builder\n\t" + ctgResetCall + "\n}\n",
		}),
		ctgSource("the reset literal in another function of testdb.go", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			ctgTestdbFile("\nfunc other(b *strings.Builder) {\n\t"+ctgResetCall+"\n}\n"))),
		ctgSource("a second reset literal in the real shape", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, ctgResetCall+"\n\t"+ctgResetCall))),
		ctgSource("a changed reset literal", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `b.WriteString("SET session_replication_role = replica;")`))),
		ctgSource("the reset literal sent through tx.Exec", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `db.Exec("SET LOCAL session_replication_role = replica;")`))),
		ctgSource("the reset literal on another builder", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `sb.WriteString("SET LOCAL session_replication_role = replica;")`))),
		ctgSource("the reset literal in a func literal", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, "func() {\n\t\t"+ctgResetCall+"\n\t}()"))),
		ctgSource("the reset literal in a method of the same name", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith("func resetStatements(", "func (x thing) resetStatements("))),
	)

	// RC-1 (d): production code never imports internal/testdb.
	for name, imp := range map[string]string{
		"plain": `import "fold/internal/testdb"`, "aliased": `import td "fold/internal/testdb"`,
		"blank": `import _ "fold/internal/testdb"`, "dot": `import . "fold/internal/testdb"`,
	} {
		cases = append(cases,
			ctgSource("a "+name+" import of internal/testdb", "RC-1(d)", "api_x.go", map[string]string{"api_x.go": "package main\n\n" + imp + "\n"}),
			ctgSource("a "+name+" import of internal/testdb from a deep package", "RC-1(d)", "internal/other/x.go", map[string]string{"internal/other/x.go": "package other\n\n" + imp + "\n"}),
		)
	}

	// Allowed: the forms the design lists as negative fixtures, each of which
	// must leave the clean tree clean.
	allowed := []string{
		`var out struct{ Reason string }; out.Reason = ""`,
		`r.EndReason = "x"`,
		`tx.Exec("UPDATE messages SET reason = CASE WHEN x THEN '' ELSE reason END")`,
		`meta := map[string]any{"reason": "x"}; _ = meta`,
		`var n int64; tx.Model(&models.ContactRequest{}).Where("requester_id = ?", 1).Count(&n)`,
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).Find(&rs)`,
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).First(&r)`,
		`var x []string; tx.Model(&models.ContactRequest{}).Pluck("reason", &x)`,
		`tx.Save(&r)`,
		`tx.Save(p)`,
		`tx.Save(&rs)`,
		`tx.Save(rs)`,
		`tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.ContactRequest{ID: 1})`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"status"})}).Create(&r)`,
		`tx.Clauses(clause.Locking{Strength: "UPDATE"}).Save(&r)`,
		`tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, 1)`,
		`tx.Where(models.Setting{Key: "k"}).Assign(map[string]any{"value": "v"}).FirstOrCreate(&models.Setting{})`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.Setting{})`,
		`s := models.Setting{}; tx.Clauses(clause.OnConflict{UpdateAll: true, DoUpdates: someSet}).Create(&s)`,
		`tx.Model(&r).Update("status", "x")`,
		`tx.Model(&r).Updates(map[string]any{"status": "x"})`,
		`tx.Omit("reason").Save(&r)`,
		`tx.Select("status").Save(&r)`,
		`tx.Where("id = ?", 1).FirstOrCreate(&r)`,
		`tx.FirstOrCreate(&r, "id = ?", 1)`,
		`tx.Where("end_reason = ?", 1).FirstOrCreate(&r)`,
		`tx.Model(&models.Registration{}).Where("id = ?", 1).Update("manage_token_hash", "")`,
		`tx.Where("id = ?", 1).Delete(&models.Notification{})`,
		`tx.Exec("DELETE FROM guest_signups WHERE email = ?", "x")`,
		`tx.Exec("SELECT set_config('statement_timeout', ?, true)", "5000ms")`,
		`tx.Raw("SELECT current_setting('statement_timeout')").Scan(&x)`,
		// T07A-GR-01: parentheses around an unprotected or non-request selector.
		`var out struct{ Reason string }; (out.Reason) = ""`,
		`(r.EndReason) = "x"`,
		// T07A-GR-02: the statement still ends at its real semicolon.
		`tx.Exec("UPDATE contact_requests SET status = 'x' WHERE id = 1; UPDATE messages SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = ';' WHERE id = 1; UPDATE messages SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = $$;$$ WHERE id = 1; UPDATE messages SET reason = ''")`,
		// T07A-GFR-01: the statement still ends at its real semicolon after a quoted
		// alias, and a double quote inside a single-quoted string opens no identifier.
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET status = 'x' WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests SET status = 'say \"hi' WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0) WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE messages AS \"r;kept\" SET reason = ''`)",
		// T07A-GFR-02: a constant that a closure, block or local variable redeclares
		// is not the outer constant, in either direction.
		`const q = "UPDATE settings SET "; func() { const q = "UPDATE contact_requests SET "; _ = q }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE settings SET "; { const q = "UPDATE contact_requests SET "; _ = q }; tx.Exec(q + "reason = ''")`,
		`const a = "UPDATE settings "; const q = a + "SET "; func() { const a = "UPDATE contact_requests "; _ = a }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE contact_requests SET "; func() { q := "UPDATE settings SET "; tx.Exec(q + "reason = ''") }()`,
		// T07A-GR-03: a DTO or a Setting chain that reuses a request's name in an
		// inner scope is not the request, in either direction.
		`func(r struct{ Reason string }) { r.Reason = "" }(struct{ Reason string }{})`,
		`_ = r; func(r struct{ Reason string }) { r.Reason = "" }(struct{ Reason string }{})`,
		`{ r := struct{ Reason string }{}; r.Reason = "" }`,
		`func(p *struct{ Reason string }) { p.Reason = "" }(nil)`,
		`q := tx.Model(&r); func() { q := tx.Model(&models.Setting{}); q.Update("reason", "") }(); _ = q`,
		`q := tx.Model(&models.Setting{}); func() { q := tx.Model(&r); _ = q }(); q.Update("reason", "")`,
		`q := tx.Model(&models.Setting{}); func() { q := tx.Model(&r); _ = q }(); q.Delete(nil)`,
	}
	for _, b := range allowed {
		cases = append(cases, ctgCase{name: b, files: map[string]string{"other.go": ctgBodyFile(b)}})
	}
	cases = append(cases,
		ctgCase{name: "tombstone package declarations resolve in their own file", files: map[string]string{
			"account_erasure_tombstones.go": tombstone + "\nfunc lockTombstoneIn(tx *gorm.DB, id uint) (bool, error) { return true, nil }\nvar errTombstoneNoop = errors.New(\"no work\")\n",
		}},
		ctgCase{name: "tombstone renamed callback parameter", files: map[string]string{
			"account_erasure_tombstones.go": strings.NewReplacer("func(tx *gorm.DB)", "func(tombTx *gorm.DB)", "lockTombstoneIn(tx,", "lockTombstoneIn(tombTx,", "eraseContactRequestTextIn(tx,", "eraseContactRequestTextIn(tombTx,").Replace(tombstone),
		}},
		ctgCase{name: "tombstone renamed lock results", files: map[string]string{
			"account_erasure_tombstones.go": strings.NewReplacer("found", "locked", "err :=", "lockErr :=", "err !=", "lockErr !=", "return err\n", "return lockErr\n", "{}, err", "{}, lockErr").Replace(tombstone),
		}},
		ctgCase{name: "an assignment call in the callback body", files: map[string]string{
			"api_admin.go": ctgAdminFile("", "err = eraseContactRequestTextIn(tx, s)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}"),
		}},
		ctgCase{name: "an expression statement call in the callback body", files: map[string]string{
			"api_admin.go": ctgAdminFile("", "eraseContactRequestTextIn(tx, s)"),
		}},
		// T07A-GFR-03: a call in an if condition that uses the callback's own tx.
		ctgCase{name: "a condition call using the callback parameter", files: map[string]string{
			"api_admin.go": ctgAdminFile("", "if eraseContactRequestTextIn(tx, s) != nil {\n\t\t\treturn gorm.ErrInvalidDB\n\t\t}"),
		}},
		ctgCase{name: "a condition call with an init that does not shadow tx", files: map[string]string{
			"api_admin.go": ctgAdminFile("", "if n := 1; eraseContactRequestTextIn(tx, s) != nil && n > 0 {\n\t\t\treturn gorm.ErrInvalidDB\n\t\t}"),
		}},
		ctgCase{name: "a ContactRequest type of another package", files: map[string]string{
			"other.go": "package main\n\nimport \"fold/internal/policy\"\n\nfunc other() {\n\tvar r policy.ContactRequest\n\tr.Reason = \"\"\n}\n",
		}},
		ctgCase{name: "a bare ContactRequest in package policy", files: map[string]string{
			"internal/policy/contact.go": "package policy\n\ntype ContactRequest struct{ Reason string }\n\nfunc f() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}},
		ctgCase{name: "a test file that sets the role and imports internal/testdb", files: map[string]string{
			"api_x_test.go": "package main\n\nimport \"fold/internal/testdb\"\n\nvar q = \"SET LOCAL session_replication_role = replica; ALTER TABLE x DISABLE TRIGGER y\"\n",
		}},
		ctgCase{name: "a package inside internal/testdb", files: map[string]string{
			"internal/testdb/helper.go": "package testdb\n\nfunc helper() {}\n",
		}},
		ctgCase{name: "the models package declaring the type", files: map[string]string{"models/models.go": ctgModelsFile}},
	)

	covered := map[string]bool{}
	for _, tc := range cases {
		ctgRunCase(t, tc, covered)
	}
	// Mutation M11: deleting any one rule must fail the matching positive fixture.
	for _, rule := range []string{"R-1", "R-2", "R-3", "R-4a", "R-4b", "R-4c", "R-5", "R-6", "R-7", "RC-1(b)", "RC-1(d)"} {
		if !covered[rule] {
			t.Errorf("no positive fixture proves that %s rejects anything", rule)
		}
	}
}

// The scan must reach the files the real allowances and the import check are
// about. A tree that stops holding them would let the guard pass on nothing.
func TestContactRequestTextGuardReadsTheRealTree(t *testing.T) {
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error {
		sources[path] = string(src)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"account_erasure.go", "account_erasure_tombstones.go", "api_admin.go", "api_contact.go", "internal/testdb/testdb.go"} {
		if _, ok := sources[path]; !ok {
			t.Errorf("walkProductionGo no longer reads %s, which the contact-request text guard binds its allowances to", path)
		}
	}
	for path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			t.Errorf("walkProductionGo returned the test file %s", path)
		}
	}
}

// emailChoiceViolations is parse-only. It cannot see SQL assembled from
// non-literal pieces, reflection, SQL sent around GORM, dot-imports of
// fold/models, or cross-package helpers. Inventory Table literals are data,
// not queries, and use the same narrowly parsed exception as suppression SQL.
func emailChoiceViolations(sources map[string]string) []string {
	var bad []string
	positions := token.NewFileSet()
	for path, src := range sources {
		if filepath.Dir(path) != "." && !strings.HasPrefix(path, "internal/") {
			continue
		}
		tree, err := parser.ParseFile(positions, path, src, 0)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		names := emailGuardImports(tree)
		inventory := erasureInventoryTableLiterals(path, tree)
		for _, decl := range tree.Decls {
			owner := ""
			if f, ok := decl.(*ast.FuncDecl); ok {
				owner = f.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				flagged := false
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind == token.STRING && !inventory[n] {
						s, _ := strconv.Unquote(n.Value)
						flagged = strings.Contains(strings.ToLower(s), "email_address_optouts") && path != "email_optouts.go"
					}
				case *ast.SelectorExpr:
					id, ok := n.X.(*ast.Ident)
					if !ok || names[id.Name] != "fold/models" {
						break
					}
					switch n.Sel.Name {
					case "EmailAddressOptOut":
						flagged = path != "email_optouts.go"
					case "ConsentEmail":
						flagged = path != "email_optouts.go" && !(path == "email_unsubscribe.go" && owner == "setEmailConsent") && !(path == "api_notifications.go" && owner == "apiUpdateNotificationSettings")
					}
				}
				if flagged {
					bad = append(bad, positions.Position(n.Pos()).String()+": email choices must use readEmailOff")
				}
				return true
			})
		}
	}
	return bad
}

func emailGuardImports(tree *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range tree.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}

func emailGuardSources(t *testing.T) map[string]string {
	t.Helper()
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error { sources[path] = string(src); return nil }); err != nil {
		t.Fatal(err)
	}
	return sources
}

func TestEmailChoiceSQLStaysInOneHelper(t *testing.T) {
	for _, bad := range emailChoiceViolations(emailGuardSources(t)) {
		t.Error(bad)
	}
}

func TestEmailChoiceGuardCatchesBypasses(t *testing.T) {
	for name, src := range map[string]string{
		"table":    `package main; func x() { _ = "SELECT * FROM email_address_optouts" }`,
		"model":    `package main; import m "fold/models"; func x() { _ = m.EmailAddressOptOut{} }`,
		"consents": `package main; import "fold/models"; func x() { _ = models.ConsentEmail }`,
	} {
		t.Run(name, func(t *testing.T) {
			path := "messages_send.go"
			if name == "model" {
				path = "api_admin_suppressions.go"
			}
			if bad := emailChoiceViolations(map[string]string{path: src}); len(bad) == 0 {
				t.Fatal("bypass accepted")
			}
		})
	}
	if bad := emailChoiceViolations(map[string]string{"email_optouts.go": `package main; import "fold/models"; func readEmailOff() { _ = models.ConsentEmail; _ = "SELECT * FROM email_address_optouts" }`}); len(bad) != 0 {
		t.Fatal(bad)
	}
}

// optionalMailViolations is parse-only. It cannot see messages built in other
// packages, reflection, values copied through untyped containers, or a sender
// that ignores Optional. Elided literals such as []mail.Message{{Optional: true}}
// are not matched. It checks send-boundary call locations, the batch's lexical
// claim/read/render order, and a notification .Off call, as well as explicit
// optional/broadcast constructors and assignments, following import aliases.
// Lexical order does not prove execution order, that the SQL literal claims a
// row, or that the choice result controls sending; behavioral tests cover those.
func optionalMailViolations(sources map[string]string) []string {
	positions := token.NewFileSet()
	var bad []string
	functions := map[string]*ast.FuncDecl{}
	allowed := map[string]string{"api_notifications.go": "notificationEmailMessage", "messages_send.go": "messageEmail", "api_message_preview.go": "apiTestMessage"}
	for path, src := range sources {
		dir := filepath.Dir(path)
		if dir != "." && (!strings.HasPrefix(path, "internal/") || strings.HasPrefix(path, "internal/mail/") || strings.HasPrefix(path, "internal/providers/")) {
			continue
		}
		tree, err := parser.ParseFile(positions, path, src, 0)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		names := emailGuardImports(tree)
		isFalse := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "false" }
		isBroadcast := func(e ast.Expr) bool {
			s, ok := e.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			id, ok := s.X.(*ast.Ident)
			return ok && names[id.Name] == "fold/internal/providers" && s.Sel.Name == "StreamBroadcast"
		}
		inspect := func(node ast.Node, owner string, appMethod bool) {
			ast.Inspect(node, func(n ast.Node) bool {
				flagged := false
				switch n := n.(type) {
				case *ast.CompositeLit:
					sel, ok := n.Type.(*ast.SelectorExpr)
					if !ok {
						break
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok || names[id.Name] != "fold/internal/mail" || sel.Sel.Name != "Message" {
						break
					}
					for _, elt := range n.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						flagged = flagged || key.Name == "Optional" && !isFalse(kv.Value) || key.Name == "Stream" && isBroadcast(kv.Value)
					}
				case *ast.AssignStmt:
					for i, lhs := range n.Lhs {
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok || i >= len(n.Rhs) {
							continue
						}
						flagged = flagged || sel.Sel.Name == "Optional" && !isFalse(n.Rhs[i]) || sel.Sel.Name == "Stream" && isBroadcast(n.Rhs[i])
					}
				}
				if flagged && (allowed[path] != owner || !appMethod) {
					bad = append(bad, positions.Position(n.Pos()).String()+": optional mail needs an effective-choice send boundary")
				}
				return true
			})
		}
		for _, decl := range tree.Decls {
			f, ok := decl.(*ast.FuncDecl)
			if !ok {
				inspect(decl, "", false)
				continue
			}
			appMethod := false
			if f.Recv != nil && len(f.Recv.List) == 1 {
				if p, ok := f.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := p.X.(*ast.Ident); ok {
						appMethod = id.Name == "App"
					}
				}
			}
			functions[path+":"+f.Name.Name] = f
			inspect(f, f.Name.Name, appMethod)
		}
	}
	hasCall := func(node ast.Node, name string) bool {
		found := false
		ast.Inspect(node, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
			return true
		})
		return found
	}
	for _, edge := range [][3]string{
		{"api_notifications.go:readEmailRecipients", "readEmailOff", "effective recipient choices"},
		{"api_notifications.go:readEmailRecipient", "readEmailRecipients", "effective recipient read"},
		{"notify_deliver.go:sendHeldNotification", "readEmailRecipient", "notification boundary"},
	} {
		if f := functions[edge[0]]; f == nil || !hasCall(f, edge[1]) {
			bad = append(bad, edge[0]+": missing "+edge[2])
		}
	}
	if f := functions["notify_deliver.go:sendHeldNotification"]; f != nil {
		usesChoice := false
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Off" {
					usesChoice = true
				}
			}
			return true
		})
		if !usesChoice {
			bad = append(bad, "notify_deliver.go:sendHeldNotification: missing effective-choice .Off call")
		}
	}
	for _, key := range []string{"messages_send.go:deliverMessageBatchOnce", "api_message_preview.go:apiTestMessage"} {
		f := functions[key]
		if f == nil {
			bad = append(bad, key+": missing send boundary")
			continue
		}
		var stack []ast.Node
		inside, outside := 0, 0
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "readEmailOff" {
				return true
			}
			inRange := false
			var claimTransaction *ast.FuncLit
			for i, parent := range stack {
				if _, ok := parent.(*ast.RangeStmt); ok {
					inRange = true
				}
				literal, ok := parent.(*ast.FuncLit)
				if !ok || i == 0 {
					continue
				}
				transaction, ok := stack[i-1].(*ast.CallExpr)
				if !ok {
					continue
				}
				sel, ok := transaction.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Transaction" {
					continue
				}
				for _, arg := range transaction.Args {
					if arg == literal {
						claimTransaction = literal
					}
				}
			}
			if claimTransaction != nil && (key != "messages_send.go:deliverMessageBatchOnce" || inRange) {
				inside++
				if key == "messages_send.go:deliverMessageBatchOnce" {
					claimedBefore, renderedAfter := false, false
					ast.Inspect(claimTransaction.Body, func(n ast.Node) bool {
						// A nested closure cannot supply this transaction's claim
						// or rendering evidence merely by containing the tokens.
						if _, ok := n.(*ast.FuncLit); ok {
							return false
						}
						if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING {
							text, err := strconv.Unquote(literal.Value)
							if err == nil && strings.Contains(text, "FOR UPDATE SKIP LOCKED") && literal.End() < call.Pos() {
								claimedBefore = true
							}
						}
						if render, ok := n.(*ast.CallExpr); ok {
							if sel, ok := render.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "messageEmail" && render.Pos() > call.End() {
								renderedAfter = true
							}
						}
						return true
					})
					if !claimedBefore || !renderedAfter {
						bad = append(bad, key+": readEmailOff must follow the claim and precede messageEmail in the same transaction")
					}
				}
			} else {
				outside++
			}
			return true
		})
		if inside == 0 || outside != 0 {
			bad = append(bad, key+": readEmailOff must run only inside each claim transaction")
		}
	}
	return bad
}

func TestOptionalMailChecksTheEffectiveChoice(t *testing.T) {
	for _, bad := range optionalMailViolations(emailGuardSources(t)) {
		t.Error(bad)
	}
}

func cleanOptionalMailSources() map[string]string {
	return map[string]string{
		"api_notifications.go": `package main
func readEmailRecipients() { readEmailOff() }
func readEmailRecipient() { readEmailRecipients() }
func (a *App) notificationEmailMessage() {}`,
		"notify_deliver.go":      `package main; func (a *App) sendHeldNotification() { readEmailRecipient(); off.Off() }`,
		"messages_send.go":       `package main; func (a *App) deliverMessageBatchOnce() { for range rows { db.Transaction(func() { tx.Raw("FOR UPDATE SKIP LOCKED"); readEmailOff(); a.messageEmail() }) } }`,
		"api_message_preview.go": `package main; func (a *App) apiTestMessage() { db.Transaction(func() { readEmailOff() }) }`,
		"mail_jobs.go":           `package main; func sendDirect() { msg.Optional = false }`,
	}
}

func TestOptionalMailGuardCatchesBypasses(t *testing.T) {
	if bad := optionalMailViolations(cleanOptionalMailSources()); len(bad) != 0 {
		t.Fatal(bad)
	}
	for name, src := range map[string]string{
		"optional constructor":         `package main; import m "fold/internal/mail"; func other() { _ = m.Message{Optional: true} }`,
		"broadcast constructor":        `package main; import m "fold/internal/mail"; import p "fold/internal/providers"; func other() { _ = m.Message{Stream: p.StreamBroadcast} }`,
		"optional assignment":          `package main; func other() { msg.Optional = true }`,
		"broadcast assignment":         `package main; import p "fold/internal/providers"; func other() { msg.Stream = p.StreamBroadcast }`,
		"batch cache":                  `package main; func (a *App) deliverMessageBatchOnce() { readEmailOff(); for range rows { db.Transaction(func() {}) } }`,
		"batch choice before claim":    `package main; func (a *App) deliverMessageBatchOnce() { for range rows { db.Transaction(func() { readEmailOff(); tx.Raw("FOR UPDATE SKIP LOCKED"); a.messageEmail() }) } }`,
		"batch choice after render":    `package main; func (a *App) deliverMessageBatchOnce() { for range rows { db.Transaction(func() { tx.Raw("FOR UPDATE SKIP LOCKED"); a.messageEmail(); readEmailOff() }) } }`,
		"batch claim in other closure": `package main; func (a *App) deliverMessageBatchOnce() { for range rows { db.Transaction(func() { _ = func() { tx.Raw("FOR UPDATE SKIP LOCKED") }; readEmailOff(); a.messageEmail() }) } }`,
		"notification ignores choice":  `package main; func (a *App) sendHeldNotification() { readEmailRecipient() }`,
		"missing composer choice":      `package main; func (a *App) apiTestMessage() { db.Transaction(func() {}) }`,
	} {
		t.Run(name, func(t *testing.T) {
			sources := cleanOptionalMailSources()
			path := "other.go"
			if strings.HasPrefix(name, "batch ") {
				path = "messages_send.go"
			}
			if name == "notification ignores choice" {
				path = "notify_deliver.go"
			}
			if name == "missing composer choice" {
				path = "api_message_preview.go"
			}
			sources[path] = src
			if bad := optionalMailViolations(sources); len(bad) == 0 {
				t.Fatal("bypass accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T07B-1b guards. Parse-only: they read syntax, not types, and bind names with
// go/parser's object resolution inside one file.
// ---------------------------------------------------------------------------

// fenceNamed lists the functions that run a fenced a.contactMutationCtx: every
// call in them takes a restore ticket first in its callback, abandons it by a
// defer before the call and acknowledges it in the very next statement.
var fenceNamed = []struct{ file, fn string }{
	{"api_admin.go", "apiAdminDeleteUser"},
	{"admin_operation_attempts.go", "apiAdminSettleOperationAttempt"},
}

func fenceIsSel(e ast.Expr, name string) (*ast.SelectorExpr, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	return sel, ok && sel.Sel.Name == name
}

func fenceSameVar(a, b *ast.Ident) bool {
	return a != nil && b != nil && a.Obj != nil && a.Obj == b.Obj
}

// fenceRefOf returns the identifier in &x.
func fenceRefOf(e ast.Expr) *ast.Ident {
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return nil
	}
	id, _ := u.X.(*ast.Ident)
	return id
}

func fenceCallOf(s ast.Stmt) (*ast.CallExpr, bool) {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	c, ok := es.X.(*ast.CallExpr)
	return c, ok
}

// fenceProblems checks every a.contactMutationCtx call of the named functions.
func fenceProblems(sources map[string]string) []string {
	var bad []string
	fset := token.NewFileSet()
	for _, n := range fenceNamed {
		src, ok := sources[n.file]
		if !ok {
			bad = append(bad, n.file+": the file is gone; update fenceNamed")
			continue
		}
		tree, err := parser.ParseFile(fset, n.file, src, 0)
		if err != nil {
			bad = append(bad, n.file+": cannot parse: "+err.Error())
			continue
		}
		var decl *ast.FuncDecl
		for _, d := range tree.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Name.Name == n.fn {
				decl = fd
			}
		}
		if decl == nil {
			bad = append(bad, n.file+": "+n.fn+" no longer exists; update fenceNamed")
			continue
		}
		calls := 0
		var stack []ast.Node
		ast.Inspect(decl.Body, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, node)
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if _, ok := fenceIsSel(call.Fun, "contactMutationCtx"); !ok {
				return true
			}
			calls++
			if why := fenceCallProblem(append([]ast.Node(nil), stack...)); why != "" {
				bad = append(bad, fmt.Sprintf("%s: %s: %s", n.file, n.fn, why))
			}
			return true
		})
		if calls == 0 {
			bad = append(bad, n.file+": "+n.fn+" has no a.contactMutationCtx call; update fenceNamed")
		}
	}
	sort.Strings(bad)
	return bad
}

// fenceCallProblem checks one call; stack ends with the call.
func fenceCallProblem(stack []ast.Node) string {
	call := stack[len(stack)-1].(*ast.CallExpr)
	for _, n := range stack[:len(stack)-1] {
		switch n.(type) {
		case *ast.FuncLit, *ast.GoStmt, *ast.DeferStmt:
			return "the call sits inside a func literal, go or defer"
		}
	}
	if len(stack) < 3 {
		return "the call is not a statement of a block"
	}
	var result *ast.Ident
	stmt := stack[len(stack)-2]
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != ast.Expr(call) {
			return "the call's result must be assigned to one variable"
		}
		result, _ = s.Lhs[0].(*ast.Ident)
	case *ast.ExprStmt:
		return "the call's result is not captured, so it cannot be acknowledged"
	default:
		return "the call must be a whole statement of a block"
	}
	block, ok := stack[len(stack)-3].(*ast.BlockStmt)
	if !ok {
		return "the call's statement must sit directly in a block"
	}
	at := -1
	for i, s := range block.List {
		if ast.Node(s) == stmt {
			at = i
		}
	}
	lit, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
	if !ok {
		return "the callback must be a func literal (a non-literal callback hides the ticket)"
	}
	if lit.Type.Params == nil || len(lit.Type.Params.List) != 1 || len(lit.Type.Params.List[0].Names) != 1 {
		return "the callback must take one named *gorm.DB parameter"
	}
	param := lit.Type.Params.List[0].Names[0]
	if len(lit.Body.List) == 0 {
		return "the callback is empty"
	}
	var take *ast.AssignStmt
	switch first := lit.Body.List[0].(type) {
	case *ast.AssignStmt:
		take = first
	case *ast.IfStmt:
		take, _ = first.Init.(*ast.AssignStmt)
	}
	if take == nil || take.Tok != token.ASSIGN || len(take.Lhs) != 2 || len(take.Rhs) != 1 {
		return "the callback's first statement must assign the ticket with = from a.takeRestoreTicket"
	}
	tc, ok := take.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "the callback's first statement must assign from a.takeRestoreTicket"
	}
	if _, ok := fenceIsSel(tc.Fun, "takeRestoreTicket"); !ok || len(tc.Args) != 1 {
		return "the callback's first statement must assign from a.takeRestoreTicket(tx)"
	}
	if arg, ok := tc.Args[0].(*ast.Ident); !ok || !fenceSameVar(arg, param) {
		return "the ticket must be taken on the callback's own *gorm.DB parameter, not a.DB or a local that reuses its name"
	}
	tk, ok := take.Lhs[0].(*ast.Ident)
	if !ok || tk.Obj == nil || tk.Obj.Pos() >= lit.Pos() {
		return "the ticket variable must be declared before the call, outside the callback"
	}
	// The abandon defer comes before the call, in the same block.
	abandoned := false
	for _, s := range block.List[:at] {
		d, ok := s.(*ast.DeferStmt)
		if !ok {
			continue
		}
		if _, ok := fenceIsSel(d.Call.Fun, "abandonRestoreTicket"); ok && len(d.Call.Args) == 1 && fenceSameVar(fenceRefOf(d.Call.Args[0]), tk) {
			abandoned = true
		}
	}
	if !abandoned {
		return "defer a.abandonRestoreTicket(&ticket) must come before the call"
	}
	// The acknowledgment is the very next statement, with the call's result.
	if at+1 >= len(block.List) {
		return "a.acknowledgeRestoreTicket must be the statement right after the call"
	}
	ac, ok := fenceCallOf(block.List[at+1])
	if !ok {
		return "a.acknowledgeRestoreTicket must be a plain statement right after the call (not deferred, not a goroutine)"
	}
	if _, ok := fenceIsSel(ac.Fun, "acknowledgeRestoreTicket"); !ok || len(ac.Args) != 2 {
		return "the statement right after the call must be a.acknowledgeRestoreTicket(&ticket, err)"
	}
	if !fenceSameVar(fenceRefOf(ac.Args[0]), tk) {
		return "a.acknowledgeRestoreTicket must acknowledge the ticket the callback took"
	}
	if r, ok := ac.Args[1].(*ast.Ident); !ok || !fenceSameVar(r, result) {
		return "a.acknowledgeRestoreTicket must receive the call's own result"
	}
	// Neither acknowledgment nor abandon belongs inside the callback.
	inside := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if _, ok := fenceIsSel(c.Fun, "acknowledgeRestoreTicket"); ok {
				inside = true
			}
			if _, ok := fenceIsSel(c.Fun, "abandonRestoreTicket"); ok {
				inside = true
			}
		}
		return true
	})
	if inside {
		return "the callback must not acknowledge or abandon its ticket"
	}
	return ""
}

const fenceBase = `package main

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	var err error
	for {
		var tk restoreTicket
		var terr error
		defer a.abandonRestoreTicket(&tk)
		err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
			if tk, terr = a.takeRestoreTicket(tx); terr != nil {
				return terr
			}
			return nil
		})
		a.acknowledgeRestoreTicket(&tk, err)
	}
}

func (a *App) apiAdminSettleOperationAttempt(c *gin.Context) {
	var err error
	for {
		var tk restoreTicket
		var terr error
		defer a.abandonRestoreTicket(&tk)
		err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
			if tk, terr = a.takeRestoreTicket(tx); terr != nil {
				return terr
			}
			return nil
		})
		a.acknowledgeRestoreTicket(&tk, err)
	}
}
`

func fenceSources(src string) map[string]string {
	// Both named functions live in the one fixture; each file entry parses it.
	return map[string]string{"api_admin.go": src, "admin_operation_attempts.go": src}
}

func TestFencedTransactionsTakeTheTicketFirst(t *testing.T) {
	sources := map[string]string{}
	for _, n := range fenceNamed {
		b, err := os.ReadFile(n.file)
		if err != nil {
			t.Fatal(err)
		}
		sources[n.file] = string(b)
	}
	for _, v := range fenceProblems(sources) {
		t.Error(v)
	}
	if bad := fenceProblems(fenceSources(fenceBase)); len(bad) != 0 {
		t.Fatalf("the positive fixture failed: %v", bad)
	}
}

func TestFencedTransactionsGuardCatchesBypasses(t *testing.T) {
	// Every case changes the clean fixture in one way (both named functions
	// carry the change, because the replacement applies to every occurrence).
	with := func(old, replacement string) map[string]string {
		if !strings.Contains(fenceBase, old) {
			t.Fatalf("fixture anchor %q is missing", old)
		}
		return fenceSources(strings.ReplaceAll(fenceBase, old, replacement))
	}
	take := "if tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\t\treturn terr\n\t\t\t}\n"
	ack := "a.acknowledgeRestoreTicket(&tk, err)\n"
	cases := []struct {
		name    string
		sources map[string]string
	}{
		{"ticket not first", with(take, "x := 1\n\t\t\t_ = x\n\t\t\t"+take)},
		{"taken with a.DB", with("a.takeRestoreTicket(tx)", "a.takeRestoreTicket(a.DB)")},
		{"taken on a shadowing local", with("func(tx *gorm.DB) error {", "func(db *gorm.DB) error {")},
		{"no ticket", with(take, "terr = nil\n\t\t\t")},
		{"ticket declared inside the callback", with("if tk, terr = ", "if tk, terr := ")},
		{"acknowledge missing", with(ack, "")},
		{"acknowledge not next", with(ack, "x := 1\n\t\t_ = x\n\t\t"+ack)},
		{"acknowledge deferred", with(ack, "defer "+ack)},
		{"acknowledge in a goroutine", with(ack, "go "+ack)},
		{"acknowledge on another variable", with(ack, "var other restoreTicket\n\t\ta.acknowledgeRestoreTicket(&other, err)\n")},
		{"acknowledge with another result", with(ack, "a.acknowledgeRestoreTicket(&tk, terr)\n")},
		{"acknowledge inside the callback", with("return nil\n\t\t})", "a.acknowledgeRestoreTicket(&tk, nil)\n\t\t\treturn nil\n\t\t})")},
		{"abandon missing", with("defer a.abandonRestoreTicket(&tk)\n", "")},
		{"non-literal callback", with("func(tx *gorm.DB) error {\n\t\t\tif tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\t\treturn terr\n\t\t\t}\n\t\t\treturn nil\n\t\t})", "callback)")},
		{"result not captured", with("err = a.contactMutationCtx", "a.contactMutationCtx")},
		{"call in a goroutine", with("err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {", "go a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {")},
		{"listed function gone", fenceSources(strings.ReplaceAll(fenceBase, "apiAdminSettleOperationAttempt", "renamed"))},
		{"no call in a listed function", fenceSources(strings.Replace(fenceBase, "err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {", "err = other(ctx, change, func(tx *gorm.DB) error {", 2))},
	}
	for _, tc := range cases {
		if bad := fenceProblems(tc.sources); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
	// R-6 stays satisfied by the ticket statement before the soft delete: the
	// admin callback with the ticket first and eraseContactRequestTextIn after
	// the soft delete passes both guards.
	ticket := "if tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\treturn terr\n\t\t}"
	head := "var tk restoreTicket\n\tvar terr error\n\tvar err error\n\tdefer a.abandonRestoreTicket(&tk)\n\terr = a.contactMutationCtx(ctx, contactChange{UserID: u.ID}, "
	tail := ")\n\ta.acknowledgeRestoreTicket(&tk, err)"
	admin := ctgAdminFileShape(head, tail, ticket, ctgEraseCall)
	clean := ctgClean()
	clean["api_admin.go"] = admin
	if bad := ctgViolations(clean); len(bad) != 0 {
		t.Errorf("R-6 rejected the fenced admin callback: %v", bad)
	}
	if bad := fenceProblems(map[string]string{"api_admin.go": admin, "admin_operation_attempts.go": fenceBase}); len(bad) != 0 {
		// The fixture file holds no settle function, so only that is allowed to complain.
		for _, b := range bad {
			if !strings.Contains(b, "apiAdminSettleOperationAttempt") && !strings.Contains(b, "admin_operation_attempts.go") {
				t.Errorf("the fenced admin callback failed the ticket guard: %s", b)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// R5-C01 and R6-C01: the actor seal is NOWAIT, loads what CanSignIn needs and
// is the last locking statement of the transactions that run it.
// ---------------------------------------------------------------------------

const sealSQL = "SELECT id, status, deleted_at, role, account_type, birth_date, password_hash, session_version, logout_version FROM users WHERE id = ? FOR SHARE NOWAIT"

var sealForRe = regexp.MustCompile(`\bFOR\b`)

// sealForbiddenCalls are never called inside a seal body: a seal reads and
// decides, it writes nothing and sends nothing.
var sealForbiddenCalls = map[string]bool{
	"Exec": true, "Create": true, "Save": true, "Update": true, "Updates": true, "Delete": true,
	"Send": true, "Write": true, "WriteAll": true, "Resolve": true, "Clauses": true, "Locking": true,
}

// sealAfterTransition are the only names that may follow the ledger
// transition inside settle's seal: plain reads of the receipt and the subject,
// the seam and the facts being filled in.
var sealAfterTransition = map[string]bool{
	"Raw": true, "Scan": true, "Error": true, "settleAfterFence": true,
	"receipt": true, "subjectDeleted": true, "state": true,
}

func sealNormalize(s string) string { return strings.Join(strings.Fields(s), " ") }

func sealCallName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

func sealFuncDecl(tree *ast.File, name string) *ast.FuncDecl {
	for _, d := range tree.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

// sealRunLiteral reports whether lit is the value of the run field of a
// contactSeal composite literal, given lit's ancestors (outermost first).
func sealRunLiteral(parents []ast.Node, lit *ast.FuncLit) bool {
	if len(parents) < 2 {
		return false
	}
	kv, ok := parents[len(parents)-1].(*ast.KeyValueExpr)
	if !ok || kv.Value != ast.Expr(lit) {
		return false
	}
	if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "run" {
		return false
	}
	cl, ok := parents[len(parents)-2].(*ast.CompositeLit)
	if !ok {
		return false
	}
	t, ok := cl.Type.(*ast.Ident)
	return ok && t.Name == "contactSeal"
}

// sealProblems checks the seal's contract over admin_operation_attempts.go,
// contact_lifecycle.go and api_host_handoff.go.
func sealProblems(sources map[string]string) []string {
	var bad []string
	fset := token.NewFileSet()
	trees := map[string]*ast.File{}
	for _, name := range []string{"admin_operation_attempts.go", "contact_lifecycle.go", "api_host_handoff.go"} {
		src, ok := sources[name]
		if !ok {
			bad = append(bad, name+": missing")
			continue
		}
		tree, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			bad = append(bad, name+": cannot parse: "+err.Error())
			continue
		}
		trees[name] = tree
	}
	if len(bad) > 0 {
		return bad
	}

	// currentAdminIn itself.
	attempts := trees["admin_operation_attempts.go"]
	cur := sealFuncDecl(attempts, "currentAdminIn")
	if cur == nil {
		return append(bad, "admin_operation_attempts.go: currentAdminIn is gone")
	}
	var names []string
	for _, p := range cur.Type.Params.List {
		for _, n := range p.Names {
			names = append(names, n.Name)
		}
	}
	if strings.Join(names, ",") != "tx,me,pred" {
		bad = append(bad, "currentAdminIn must take exactly tx, me and pred")
	}
	literals := 0
	var actorDeclared, scanned, canSignIn bool
	ast.Inspect(cur.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil && strings.HasPrefix(s, "SELECT") {
					literals++
					if sealNormalize(s) != sealSQL {
						bad = append(bad, "currentAdminIn's SQL is not the sealed statement: "+sealNormalize(s))
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := x.Decl.(*ast.GenDecl); ok {
				for _, sp := range gd.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "actor" {
						if sel, ok := vs.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "User" {
							actorDeclared = true
						}
					}
				}
			}
		case *ast.CallExpr:
			if sealCallName(x) == "Scan" && len(x.Args) == 1 {
				if u, ok := x.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok && id.Name == "actor" {
						scanned = true
					}
				}
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CanSignIn" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "actor" {
					canSignIn = true
				}
			}
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "PasswordHash" {
					bad = append(bad, "currentAdminIn assigns a PasswordHash")
				}
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "me" && x.Sel.Name != "ID" && x.Sel.Name != "SessionVersion" && x.Sel.Name != "LogoutVersion" {
				bad = append(bad, "currentAdminIn reads me."+x.Sel.Name+"; only the id and the two versions come from the session")
			}
		}
		return true
	})
	if literals != 1 {
		bad = append(bad, fmt.Sprintf("currentAdminIn must hold exactly one SELECT literal, found %d", literals))
	}
	if !actorDeclared || !scanned {
		bad = append(bad, "currentAdminIn must scan into a models.User declared inside it")
	}
	if !canSignIn {
		bad = append(bad, "currentAdminIn must decide sign-in with actor.CanSignIn()")
	}

	// Every call of currentAdminIn sits in a contactSeal run literal; seal
	// bodies hold no write, lock or notice.
	var stack []ast.Node
	ast.Inspect(attempts, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "currentAdminIn" {
			return true
		}
		inSeal := false
		for i := len(stack) - 2; i >= 0; i-- {
			if lit, ok := stack[i].(*ast.FuncLit); ok {
				inSeal = sealRunLiteral(stack[:i], lit)
				break
			}
			if _, ok := stack[i].(*ast.FuncDecl); ok {
				break
			}
		}
		if !inSeal {
			bad = append(bad, fmt.Sprintf("admin_operation_attempts.go:%d: currentAdminIn is called outside a contactSeal run function", fset.Position(call.Pos()).Line))
		}
		return true
	})
	stack = nil
	ast.Inspect(attempts, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		lit, ok := n.(*ast.FuncLit)
		if !ok || !sealRunLiteral(stack[:len(stack)-1], lit) {
			return true
		}
		transition := -1
		for i, s := range lit.Body.List {
			ast.Inspect(s, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.CallExpr:
					if sealForbiddenCalls[sealCallName(x)] {
						bad = append(bad, "a seal body calls "+sealCallName(x))
					}
					if sealCallName(x) == "settleTransition" && transition < 0 {
						transition = i
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && sealForRe.MatchString(x.Value) {
						bad = append(bad, "a seal body holds SQL with FOR: "+x.Value)
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "Locking" {
						bad = append(bad, "a seal body names clause.Locking")
					}
				}
				return true
			})
		}
		if transition >= 0 {
			for _, s := range lit.Body.List[transition+1:] {
				ast.Inspect(s, func(m ast.Node) bool {
					switch x := m.(type) {
					case *ast.SelectorExpr:
						if !sealAfterTransition[x.Sel.Name] {
							bad = append(bad, "after the ledger transition the seal may only read plainly; it uses ."+x.Sel.Name)
						}
					case *ast.BasicLit:
						if x.Kind == token.STRING && sealForRe.MatchString(x.Value) {
							bad = append(bad, "a locking read follows the ledger transition: "+x.Value)
						}
					}
					return true
				})
			}
		}
		return true
	})

	// contactMutationOn: reconcileContacts, the seal, then the reserve check,
	// with nothing between.
	lc := sealFuncDecl(trees["contact_lifecycle.go"], "contactMutationOn")
	if lc == nil {
		bad = append(bad, "contact_lifecycle.go: contactMutationOn is gone")
	} else {
		at := -1
		for i, s := range lc.Body.List {
			is, ok := s.(*ast.IfStmt)
			if !ok || is.Init == nil {
				continue
			}
			found := false
			ast.Inspect(is.Init, func(m ast.Node) bool {
				if c, ok := m.(*ast.CallExpr); ok && sealCallName(c) == "reconcileContacts" {
					found = true
				}
				return true
			})
			if found {
				at = i
			}
		}
		okShape := at >= 0 && at+2 < len(lc.Body.List)
		if okShape {
			seal, sok := lc.Body.List[at+1].(*ast.IfStmt)
			reserve, rok := lc.Body.List[at+2].(*ast.IfStmt)
			okShape = sok && rok
			if okShape {
				hasSeal, hasReserve := false, false
				ast.Inspect(seal, func(m ast.Node) bool {
					if c, ok := m.(*ast.CallExpr); ok && sealCallName(c) == "run" {
						hasSeal = true
					}
					return true
				})
				ast.Inspect(reserve, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && id.Name == "errCommitReserve" {
						hasReserve = true
					}
					return true
				})
				okShape = hasSeal && hasReserve
			}
		}
		if !okShape {
			bad = append(bad, "contactMutationOn must run reconcileContacts, then change.Seal.run, then the commit-reserve check, with no statement between")
		}
	}

	// deletionHostPlan's actor read takes no lock.
	dp := sealFuncDecl(trees["api_host_handoff.go"], "deletionHostPlan")
	if dp == nil {
		bad = append(bad, "api_host_handoff.go: deletionHostPlan is gone")
	} else {
		reads := 0
		ast.Inspect(dp.Body, func(m ast.Node) bool {
			c, ok := m.(*ast.CallExpr)
			if !ok || sealCallName(c) != "First" || len(c.Args) < 1 {
				return true
			}
			u, ok := c.Args[0].(*ast.UnaryExpr)
			if !ok {
				return true
			}
			if id, ok := u.X.(*ast.Ident); !ok || id.Name != "actor" {
				return true
			}
			reads++
			ast.Inspect(c.Fun, func(k ast.Node) bool {
				if kc, ok := k.(*ast.CallExpr); ok && (sealCallName(kc) == "Clauses") {
					bad = append(bad, "deletionHostPlan's actor read takes a lock")
				}
				return true
			})
			return true
		})
		if reads != 1 {
			bad = append(bad, fmt.Sprintf("deletionHostPlan must read the actor once with tx.First(&actor, ...), found %d", reads))
		}
	}
	sort.Strings(bad)
	return bad
}

func sealRealSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"admin_operation_attempts.go", "contact_lifecycle.go", "api_host_handoff.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func TestActorSealIsLast(t *testing.T) {
	real := sealRealSources(t)
	for _, v := range sealProblems(real) {
		t.Error(v)
	}
	with := func(file, old, replacement string) map[string]string {
		if strings.Count(real[file], old) != 1 {
			t.Fatalf("fixture anchor %q must occur once in %s, found %d", old, file, strings.Count(real[file], old))
		}
		out := map[string]string{}
		for k, v := range real {
			out[k] = v
		}
		out[file] = strings.Replace(real[file], old, replacement, 1)
		return out
	}
	const aops = "admin_operation_attempts.go"
	cases := []struct {
		name    string
		sources map[string]string
	}{
		{"FOR SHARE without NOWAIT", with(aops, "users WHERE id = ? FOR SHARE NOWAIT\"", "users WHERE id = ? FOR SHARE\"")},
		{"a projection without password_hash", with(aops, "birth_date, password_hash, session_version", "birth_date, session_version")},
		{"a password_hash IS NOT NULL projection", with(aops, "birth_date, password_hash, session_version", "birth_date, (password_hash IS NOT NULL) AS password_hash, session_version")},
		{"a deleted_at IS NULL filter", with(aops, "FROM users WHERE id = ? FOR SHARE NOWAIT\"", "FROM users WHERE id = ? AND deleted_at IS NULL FOR SHARE NOWAIT\"")},
		{"a PasswordHash copied from another value", with(aops, "\tvar actor models.User\n\terr := tx.Raw(", "\tvar actor models.User\n\tactor.PasswordHash = me.PasswordHash\n\terr := tx.Raw(")},
		{"a sign-in check other than CanSignIn", with(aops, "!actor.CanSignIn()", "actor.Status != models.UserActive")},
		{"a currentAdminIn call outside a seal", with(aops, "// --- mint and settle routes", "func (a *App) notASeal(tx *gorm.DB, me *models.User) error { return currentAdminIn(tx, me, nil) }\n\n// --- mint and settle routes")},
		{"a statement after the seal in contactMutationOn", with("contact_lifecycle.go", "\tif ctx.Err() != nil || (bounded && time.Until(deadline) < contactCommitReserve) {", "\t_ = tx\n\tif ctx.Err() != nil || (bounded && time.Until(deadline) < contactCommitReserve) {")},
		{"an Update in a seal", with(aops, "\t\treturn currentAdminIn(tx, me, *pred)\n\t}}", "\t\ttx.Exec(\"UPDATE users SET name = name\")\n\t\treturn currentAdminIn(tx, me, *pred)\n\t}}")},
		{"a locking read after the ledger transition", with(aops, "\t\t\tfacts.state = st\n", "\t\t\tfacts.state = st\n\t\t\ttx.Raw(\"SELECT id FROM users WHERE id = 1 FOR UPDATE\")\n")},
		{"a Locking clause on deletionHostPlan's actor read", with("api_host_handoff.go", "tx.First(&actor, me.ID)", "tx.Clauses(clause.Locking{Strength: \"SHARE\"}).First(&actor, me.ID)")},
	}
	for _, tc := range cases {
		if bad := sealProblems(tc.sources); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// T07B-2b: the reason-review page is an ungated read that ends with the actor
// seal, like the settle and deletion transactions (R5-C01).
// ---------------------------------------------------------------------------

// reasonPageSealProblems checks account_erasure_reasons.go: currentAdminIn is
// called once, as the return of the reasonPageTx callback in reasonPageAttempt,
// right after the readBeforeSeal seam, so nothing locks after it.
func reasonPageSealProblems(src string) []string {
	fset := token.NewFileSet()
	tree, err := parser.ParseFile(fset, "account_erasure_reasons.go", src, 0)
	if err != nil {
		return []string{"account_erasure_reasons.go: cannot parse: " + err.Error()}
	}
	var bad []string
	calls := 0
	ast.Inspect(tree, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && sealCallName(c) == "currentAdminIn" {
			calls++
		}
		return true
	})
	if calls != 1 {
		bad = append(bad, fmt.Sprintf("account_erasure_reasons.go: currentAdminIn must be called exactly once, found %d", calls))
	}
	fd := sealFuncDecl(tree, "reasonPageAttempt")
	if fd == nil {
		return append(bad, "account_erasure_reasons.go: reasonPageAttempt is gone")
	}
	var lit *ast.FuncLit
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && sealCallName(c) == "reasonPageTx" && len(c.Args) == 2 {
			lit, _ = c.Args[1].(*ast.FuncLit)
		}
		return true
	})
	if lit == nil || len(lit.Body.List) < 2 {
		return append(bad, "account_erasure_reasons.go: reasonPageAttempt must pass a function literal to a.reasonPageTx")
	}
	last := lit.Body.List[len(lit.Body.List)-1]
	ret, ok := last.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return append(bad, "account_erasure_reasons.go: the page transaction must end with return currentAdminIn(tx, me, pred)")
	}
	if c, ok := ret.Results[0].(*ast.CallExpr); !ok || sealCallName(c) != "currentAdminIn" || len(c.Args) != 3 {
		bad = append(bad, "account_erasure_reasons.go: the page transaction's last statement must be return currentAdminIn(tx, me, pred)")
	}
	seam, ok := lit.Body.List[len(lit.Body.List)-2].(*ast.IfStmt)
	seamCalled := false
	if ok {
		ast.Inspect(seam, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && sealCallName(c) == "readBeforeSeal" {
				seamCalled = true
			}
			return true
		})
	}
	if !seamCalled {
		bad = append(bad, "account_erasure_reasons.go: the readBeforeSeal seam must be the statement just before the seal")
	}
	return bad
}

func TestReasonPageSealIsLast(t *testing.T) {
	b, err := os.ReadFile("account_erasure_reasons.go")
	if err != nil {
		t.Fatal(err)
	}
	real := string(b)
	for _, v := range reasonPageSealProblems(real) {
		t.Error(v)
	}
	with := func(old, replacement string) string {
		if strings.Count(real, old) != 1 {
			t.Fatalf("fixture anchor %q must occur once, found %d", old, strings.Count(real, old))
		}
		return strings.Replace(real, old, replacement, 1)
	}
	seal := "\t\t// Last statement: nothing locks after it.\n\t\treturn currentAdminIn(tx, me, pred)"
	for _, tc := range []struct{ name, src string }{
		{"no seal", with(seal, "\t\treturn nil")},
		{"a statement after the seal", with(seal, "\t\tif err := currentAdminIn(tx, me, pred); err != nil {\n\t\t\treturn err\n\t\t}\n\t\treturn tx.Exec(\"SELECT 1\").Error")},
		{"a locking statement after the seam", with(seal, "\t\ttx.Exec(\"SELECT id FROM users WHERE id = 1 FOR UPDATE\")\n\t\treturn currentAdminIn(tx, me, pred)")},
		{"a second seal call", with("// --- the scanner -", "func (a *App) otherPage(tx *gorm.DB, me *models.User) error { return currentAdminIn(tx, me, nil) }\n\n// --- the scanner -")},
		{"the seam moved", with("\t\tif a.readBeforeSeal != nil {\n\t\t\ta.readBeforeSeal(tx)\n\t\t}\n", "")},
	} {
		if bad := reasonPageSealProblems(tc.src); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// Signed request-id order (T07B-2 scaling, F1).
// ---------------------------------------------------------------------------

// reasonSQLOrderProblems parses src and checks every SQL string built from
// reasonSelect or reasonProjection() that orders its rows. reasonProjection
// casts the bigint id with id::text and no alias, so the output column is also
// called id and a bare ORDER BY id binds to that text: -1 sorts before -2 and
// 10 before 2, while the scanner's cursor (which selects the real bigint)
// follows signed order. The statement must say ORDER BY contact_requests.id,
// which binds to the table's bigint column. Parse-only: it joins the string
// literals of a + chain that names one of the two identifiers; it does not see
// a statement assembled from variables, a constant in another file or SQL sent
// around GORM.
func reasonSQLOrderProblems(path, src string) []string {
	fset := token.NewFileSet()
	tree, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return []string{path + ": cannot parse: " + err.Error()}
	}
	var bad []string
	inner := map[ast.Node]bool{}
	ast.Inspect(tree, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.ADD || inner[be] {
			return true
		}
		var parts []ast.Expr
		var flatten func(e ast.Expr)
		flatten = func(e ast.Expr) {
			if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
				inner[b] = true
				flatten(b.X)
				flatten(b.Y)
				return
			}
			parts = append(parts, e)
		}
		flatten(be)
		var sql strings.Builder
		names := false
		for _, p := range parts {
			switch v := p.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						sql.WriteString(s)
					}
				}
			case *ast.Ident:
				if v.Name == "reasonSelect" {
					names = true
					sql.WriteString(" ")
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "reasonProjection" {
					names = true
					sql.WriteString(" ")
				}
			}
		}
		text := sql.String()
		if names && strings.Contains(strings.ToUpper(text), "ORDER BY") && !strings.Contains(text, "ORDER BY contact_requests.id") {
			bad = append(bad, fmt.Sprintf("%s:%d: a statement that selects the review projection orders by something other than contact_requests.id: %q", path, fset.Position(be.Pos()).Line, strings.TrimSpace(text)))
		}
		return true
	})
	return bad
}

func TestReasonSQLOrderGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "reasonSelect") || strings.Contains(string(b), "reasonProjection") {
			found++
		}
		for _, v := range reasonSQLOrderProblems(name, string(b)) {
			t.Error(v)
		}
	}
	if found == 0 {
		t.Fatal("no production file names reasonSelect: the guard reads nothing")
	}
	wrap := func(clause string) string {
		return "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT \"+reasonSelect+\" FROM contact_requests WHERE reason <> '' " + clause + "\")\n}\n"
	}
	for name, src := range map[string]string{
		"ORDER BY id":                     wrap("ORDER BY id"),
		"ORDER BY id FOR UPDATE":          wrap("ORDER BY id FOR UPDATE"),
		"ORDER BY 1":                      wrap("ORDER BY 1"),
		"lower case":                      wrap("order by id"),
		"a projection call, not a select": "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT \" + reasonProjection() + \" FROM contact_requests ORDER BY id\")\n}\n",
	} {
		if len(reasonSQLOrderProblems("fixture.go", src)) == 0 {
			t.Errorf("%s: the guard accepted it", name)
		}
	}
	for name, src := range map[string]string{
		"qualified":           wrap("ORDER BY contact_requests.id"),
		"qualified, locked":   wrap("ORDER BY contact_requests.id FOR UPDATE"),
		"no ordering":         wrap(""),
		"another select list": "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT id FROM contact_requests ORDER BY id\")\n}\n",
	} {
		if bad := reasonSQLOrderProblems("fixture.go", src); len(bad) != 0 {
			t.Errorf("%s: the guard refused it: %v", name, bad)
		}
	}
}

// The scanner's statements carry no NULL-or-condition: a generic plan cannot
// serve "$1 IS NULL OR id > $1" from the primary key, so a continuation would
// rescan from the start (F5). Source-level check; TestReasonScanSQLShape
// proves the plans.
func TestReasonScanSQLHasNoNullOrCondition(t *testing.T) {
	for name, sql := range map[string]string{"initial": reasonScanInitialSQL, "continuation": reasonScanContinuationSQL} {
		up := strings.ToUpper(sql)
		if strings.Contains(up, " IS NULL") || strings.Contains(up, " OR ") {
			t.Errorf("the %s scanner statement has a NULL-or condition: %s", name, sql)
		}
		if !strings.Contains(up, "ORDER BY ID LIMIT") {
			t.Errorf("the %s scanner statement must order by id and limit: %s", name, sql)
		}
	}
	if strings.Contains(strings.ToUpper(reasonScanInitialSQL), "WHERE") {
		t.Error("the initial scanner statement must have no id predicate")
	}
	if !strings.Contains(reasonScanContinuationSQL, "WHERE id > ?::bigint") {
		t.Error("the continuation must filter on id > the cursor")
	}
}

// The probe for "is there any reason" asks the server for one row. LIMIT 1
// bounds what comes back and what is locked, not what is read to find it; this
// pins that the probe is not a Go-side early break over a full read.
func TestReasonProbeUsesLimitOne(t *testing.T) {
	if !strings.Contains(strings.ToUpper(reasonProbeSQL), "LIMIT 1") || strings.Contains(strings.ToUpper(reasonProbeSQL), "FOR UPDATE") {
		t.Errorf("the probe must be a plain LIMIT 1 statement: %s", reasonProbeSQL)
	}
	b, err := os.ReadFile("account_erasure_reasons.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func reasonSetExistsIn(")
	if i < 0 {
		t.Fatal("reasonSetExistsIn is gone")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "reasonProbeSQL") || strings.Count(body, "rows.Next()") != 1 {
		t.Errorf("the probe must run reasonProbeSQL and read at most one row:\n%s", body)
	}
}
