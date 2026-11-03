package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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
// shared helpers, these repo-wide guards, and the migration drift check.
var testLayoutAllowed = map[string]bool{
	"testhelpers_test.go":      true,
	"guards_test.go":           true,
	"migrations/drift_test.go": true,
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
// notify.Categories, the notifications.category CHECK (migration 00010)
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
	sqlCats := pick("migrations/00010_notifications.sql", regexp.MustCompile(`CHECK \(category IN \(([^)]*)\)\)`))
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
// Go application source. Keep this file discovery shared by both guards.
func productionMailViolations(root string, checkMailer bool) ([]string, error) {
	positions := token.NewFileSet()
	packages := map[string][]*ast.File{}
	err := fs.WalkDir(os.DirFS(root), ".", func(path string, entry fs.DirEntry, err error) error {
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
