package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // a timezone by name even where the system has no zoneinfo
)

// `fridolin calendar` is the brain's Google Calendar. It asks the bridge,
// which holds the brain's token and keeps it to the calendars ticked on
// the brain's page (google_calendar.go); this side turns dates and clock
// times into times in the brain's timezone and prints the results.

const calendarUsage = `Usage: fridolin calendar [--calendar-id ID] [--timezone ZONE] COMMAND [flags]

The brain's Google Calendar, connected on its page in the dashboard (Brains
→ brain → Calendar). The bridge keeps it to the calendars ticked there.
Every command prints JSON; COMMAND -h shows its flags.

Commands:
  calendars   the calendars this brain may use
  list        events from --from to --to (YYYY-MM-DD; default today), across
              all of the brain's calendars
  create      an event (--title, --start, --end; a date for all day) in the
              default calendar
  free        the busy times and free windows of --date
  delete      an event, by --event-id

--calendar-id picks one of the brain's calendars; "primary" is its default.
Times are in --timezone, else $CALENDAR_TZ, else the system's timezone.

Flags:
`

// calendarCmd is `fridolin calendar`.
func calendarCmd(args []string, t toolIO) int {
	global := flag.NewFlagSet("fridolin calendar", flag.ContinueOnError)
	global.SetOutput(t.stderr)
	calID := global.String("calendar-id", "", "one of the brain's calendars (default: all of them to read, the default one to write)")
	zone := global.String("timezone", "", "IANA timezone such as Europe/Berlin (default: $CALENDAR_TZ, else the system's)")
	global.Usage = func() {
		fmt.Fprint(global.Output(), calendarUsage)
		global.PrintDefaults()
	}
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return t.exitFor("fridolin calendar", err)
	}
	if global.NArg() == 0 {
		global.Usage()
		return t.fail("fridolin calendar: give a command: calendars, list, create, free or delete")
	}
	cmd, rest := global.Arg(0), global.Args()[1:]
	c := &calendarRun{t: t}

	flags := flag.NewFlagSet("fridolin calendar "+cmd, flag.ContinueOnError)
	// Before the command or after it, as the Python tool's global flags.
	flags.StringVar(calID, "calendar-id", *calID, "one of the brain's calendars (default: all of them to read, the default one to write)")
	flags.StringVar(zone, "timezone", *zone, "IANA timezone such as Europe/Berlin (default: $CALENDAR_TZ, else the system's)")
	var run func() (any, error)
	switch cmd {
	case "calendars":
		run = c.calendars
	case "list":
		from := flags.String("from", "", "first day, YYYY-MM-DD (default: today)")
		to := flags.String("to", "", "last day, YYYY-MM-DD (default: --from)")
		limit := flags.Int("max-results", 100, "the most events, across all calendars; the result says truncated when there were more")
		run = func() (any, error) { return c.list(*calID, *from, *to, *limit) }
	case "create":
		var ev NewEvent
		flags.StringVar(&ev.Title, "title", "", "the event's title (required)")
		start := flags.String("start", "", "YYYY-MM-DDTHH:MM[:SS], or YYYY-MM-DD for all day (required)")
		end := flags.String("end", "", "the same as --start; an all-day event's end date is the day after its last day (required)")
		flags.StringVar(&ev.Description, "description", "", "notes")
		flags.StringVar(&ev.Location, "location", "", "where")
		attendees := flags.String("attendees", "", "email addresses, separated by commas")
		reminders := flags.String("reminders", "", "method:minutes pairs, such as popup:10,email:60 (default popup:10)")
		flags.StringVar(&ev.SendUpdates, "send-updates", "all", "whether Google emails the attendees: all, externalOnly or none")
		run = func() (any, error) { return c.create(*calID, ev, *start, *end, *attendees, *reminders) }
	case "free":
		date := flags.String("date", "", "the day, YYYY-MM-DD (required)")
		hours := flags.String("hours", "08:00-18:00", "the window to search, HH:MM-HH:MM; the end may be 24:00")
		minSlot := flags.Int("min-slot", 30, "leave out free windows shorter than this many minutes")
		run = func() (any, error) { return c.free(*calID, *date, *hours, *minSlot) }
	case "delete":
		id := flags.String("event-id", "", "the event's ID, from list (required)")
		run = func() (any, error) { return c.delete(*calID, *id) }
	case "auth":
		return t.fail("fridolin calendar: a brain's calendar is connected in the dashboard, not here: Brains → this brain → Calendar → Connect Google Calendar")
	default:
		global.Usage()
		return t.fail("fridolin calendar: unknown command %q: use calendars, list, create, free or delete", cmd)
	}
	positional, err := t.parseTool(flags, rest)
	if err != nil {
		return t.exitFor("fridolin calendar "+cmd, err)
	}
	if len(positional) > 0 {
		return t.fail("fridolin calendar %s: unexpected argument %q", cmd, positional[0])
	}
	if c.loc, err = calendarZone(*zone, t); err != nil {
		return t.fail("%v", err)
	}
	if c.client, err = brainClientFor(t); err != nil {
		return t.fail("%v", err)
	}
	out, err := run()
	if err != nil {
		return t.fail("%v", err)
	}
	return t.emit(out)
}

// calendarRun is one calendar command: its timezone and its way to the
// bridge.
type calendarRun struct {
	t      toolIO
	loc    *time.Location
	client *brainClient
}

func (c *calendarRun) call(method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return c.client.call(ctx, method, path, body, out)
}

func (c *calendarRun) calendars() (any, error) {
	var out any
	err := c.call("GET", "/brain/calendar/calendars", nil, &out)
	return out, err
}

func (c *calendarRun) list(calendar, from, to string, limit int) (any, error) {
	if limit < 1 {
		return nil, fmt.Errorf("--max-results must be at least 1, got %d", limit)
	}
	first := c.t.now.In(c.loc)
	if from != "" {
		var err error
		if first, err = c.day(from); err != nil {
			return nil, err
		}
	}
	first = midnight(first)
	last := first
	if to != "" {
		var err error
		if last, err = c.day(to); err != nil {
			return nil, err
		}
	}
	if last.Before(first) {
		return nil, fmt.Errorf("--to %s comes before --from %s", last.Format(time.DateOnly), first.Format(time.DateOnly))
	}
	// Up to the start of the day after --to, so the last day is whole and
	// its all-day events are in.
	q := url.Values{
		"from":      {first.Format(time.RFC3339)},
		"to":        {midnight(last).AddDate(0, 0, 1).Format(time.RFC3339)},
		"time_zone": {c.loc.String()},
		"max":       {strconv.Itoa(min(limit, 2500))},
	}
	if calendar != "" {
		q.Set("calendar", calendar)
	}
	var out any
	err := c.call("GET", "/brain/calendar/events?"+q.Encode(), nil, &out)
	return out, err
}

func (c *calendarRun) create(calendar string, ev NewEvent, start, end, attendees, reminders string) (any, error) {
	if ev.Title == "" || start == "" || end == "" {
		return nil, errors.New("create needs --title, --start and --end")
	}
	if !slices.Contains(sendUpdatesValues, ev.SendUpdates) {
		return nil, fmt.Errorf("--send-updates must be one of %s, got %q", strings.Join(sendUpdatesValues, ", "), ev.SendUpdates)
	}
	var err error
	if ev.Start, err = c.eventTime(start); err != nil {
		return nil, err
	}
	if ev.End, err = c.eventTime(end); err != nil {
		return nil, err
	}
	ev.Calendar, ev.TimeZone = calendar, c.loc.String()
	for _, a := range strings.Split(attendees, ",") {
		if a = strings.TrimSpace(a); a != "" {
			ev.Attendees = append(ev.Attendees, a)
		}
	}
	if ev.Reminders, err = parseReminders(reminders); err != nil {
		return nil, err
	}
	var out any
	err = c.call("POST", "/brain/calendar/events", ev, &out)
	return out, err
}

func (c *calendarRun) delete(calendar, id string) (any, error) {
	if id == "" {
		return nil, errors.New("delete needs --event-id")
	}
	path := "/brain/calendar/events/" + url.PathEscape(id)
	if calendar != "" {
		path += "?" + url.Values{"calendar": {calendar}}.Encode()
	}
	var out any
	err := c.call("DELETE", path, nil, &out)
	return out, err
}

// FreeDay is what `free` prints.
type FreeDay struct {
	Date        string       `json:"date"`
	Timezone    string       `json:"timezone"`
	Hours       string       `json:"hours"`
	Busy        []clockRange `json:"busy"`
	FreeWindows []freeWindow `json:"free_windows"`
}

type clockRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type freeWindow struct {
	Start   string `json:"start"`
	End     string `json:"end"`
	Minutes int    `json:"duration_minutes"`
}

func (c *calendarRun) free(calendar, date, hours string, minSlot int) (any, error) {
	if date == "" {
		return nil, errors.New("free needs --date")
	}
	day, err := c.day(date)
	if err != nil {
		return nil, err
	}
	first, last, err := parseHours(hours)
	if err != nil {
		return nil, err
	}
	next := day.AddDate(0, 0, 1)
	req := map[string]string{"from": day.Format(time.RFC3339), "to": next.Format(time.RFC3339), "time_zone": c.loc.String(), "calendar": calendar}
	var resp struct {
		Busy []BusySlot `json:"busy"`
	}
	if err := c.call("POST", "/brain/calendar/freebusy", req, &resp); err != nil {
		return nil, err
	}
	var busy []timeRangeOf
	out := FreeDay{Date: date, Timezone: c.loc.String(), Hours: clock(first) + "-" + clock(last), Busy: []clockRange{}}
	for _, b := range resp.Busy {
		s, err1 := time.Parse(time.RFC3339, b.Start)
		e, err2 := time.Parse(time.RFC3339, b.End)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("the bridge returned a busy time that is not RFC 3339: %s to %s", b.Start, b.End)
		}
		s, e = s.In(c.loc), e.In(c.loc)
		busy = append(busy, timeRangeOf{s, e})
		out.Busy = append(out.Busy, clockRange{s.Format("15:04"), e.Format("15:04")})
	}
	// Wall-clock minutes after midnight, as the hours were given: on the
	// day the clocks change, 08:00 is still 08:00.
	windowStart := time.Date(day.Year(), day.Month(), day.Day(), 0, first, 0, 0, c.loc)
	windowEnd := time.Date(day.Year(), day.Month(), day.Day(), 0, last, 0, 0, c.loc)
	out.FreeWindows = freeWindows(busy, windowStart, windowEnd, minSlot)
	return out, nil
}

// timeRangeOf is a busy slot as times.
type timeRangeOf struct{ start, end time.Time }

// freeWindows are the gaps of at least minSlot minutes between the busy
// slots, inside the window. Each slot is clipped to the window first:
// otherwise a meeting at 19:00 would stretch the last free window of an
// 08:00-18:00 search to 19:00, offering a slot outside the hours asked for.
func freeWindows(busy []timeRangeOf, windowStart, windowEnd time.Time, minSlot int) []freeWindow {
	busy = slices.Clone(busy)
	slices.SortFunc(busy, func(a, b timeRangeOf) int { return cmp.Or(a.start.Compare(b.start), a.end.Compare(b.end)) })
	var gaps []timeRangeOf
	cursor := windowStart
	for _, b := range busy {
		s, e := later(b.start, windowStart), earlier(b.end, windowEnd)
		if !s.Before(e) {
			continue // entirely outside the window
		}
		if s.After(cursor) {
			gaps = append(gaps, timeRangeOf{cursor, s})
		}
		cursor = later(cursor, e)
	}
	if cursor.Before(windowEnd) {
		gaps = append(gaps, timeRangeOf{cursor, windowEnd})
	}
	// A window that runs to midnight ends at 24:00, not 00:00.
	clockOf := func(t time.Time) string {
		if t.After(windowStart) && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
			return "24:00"
		}
		return t.Format("15:04")
	}
	out := []freeWindow{}
	for _, g := range gaps {
		if minutes := int(g.end.Sub(g.start).Minutes()); minutes >= minSlot {
			out = append(out, freeWindow{clockOf(g.start), clockOf(g.end), minutes})
		}
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

var hoursPattern = regexp.MustCompile(`^(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})$`)

// parseHours reads --hours HH:MM-HH:MM as minutes after midnight; the end
// may be 24:00.
func parseHours(s string) (int, int, error) {
	m := hoursPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, fmt.Errorf("--hours: use HH:MM-HH:MM, such as 08:00-18:00 (got %q)", s)
	}
	n := make([]int, 4)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	first, last := n[0]*60+n[1], n[2]*60+n[3]
	if n[0] > 23 || n[1] > 59 || n[3] > 59 || last > 24*60 {
		return 0, 0, fmt.Errorf("--hours: times run from 00:00 to 24:00 (got %q)", s)
	}
	if first >= last {
		return 0, 0, fmt.Errorf("--hours: the start must come before the end (got %q)", s)
	}
	return first, last, nil
}

func clock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// day parses YYYY-MM-DD as the start of that day in the run's timezone.
func (c *calendarRun) day(s string) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, c.loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot read the date %q: use YYYY-MM-DD", s)
	}
	return d, nil
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// eventTimeLayouts are the clock times --start and --end take without an
// offset, which are times in the run's timezone.
var eventTimeLayouts = []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04"}

// eventTime turns --start or --end into what the bridge takes: a date for
// an all-day event, else a time with an explicit offset. Google can take a
// time without one as UTC even when a time zone is given, which would move
// the event by hours.
func (c *calendarRun) eventTime(s string) (string, error) {
	unreadable := fmt.Errorf("cannot read %q: use YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD for an all-day event", s)
	if !strings.Contains(s, "T") {
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return "", unreadable
		}
		return s, nil
	}
	if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return at.Format(time.RFC3339), nil
	}
	for _, layout := range eventTimeLayouts {
		if at, err := time.ParseInLocation(layout, s, c.loc); err == nil {
			return at.Format(time.RFC3339), nil
		}
	}
	return "", unreadable
}

// parseReminders reads --reminders popup:10,email:60. Empty means the
// default, one popup ten minutes before.
func parseReminders(s string) ([]Reminder, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []Reminder
	for _, r := range strings.Split(s, ",") {
		method, minutes, ok := strings.Cut(strings.TrimSpace(r), ":")
		n, err := strconv.Atoi(strings.TrimSpace(minutes))
		if !ok || strings.TrimSpace(method) == "" || err != nil || n < 0 {
			return nil, fmt.Errorf("bad --reminders entry %q: use method:minutes, such as popup:10", r)
		}
		out = append(out, Reminder{Method: strings.TrimSpace(method), Minutes: n})
	}
	return out, nil
}

// zoneinfoLink is the system's timezone setting; tests point it elsewhere.
var zoneinfoLink = "/etc/localtime"

// calendarZone is the run's timezone: --timezone, else CALENDAR_TZ, else
// the system's. Google takes only IANA names, and guessing would create
// events at the wrong time while looking like it worked, so when none can
// be found that is an error.
func calendarZone(flagValue string, t toolIO) (*time.Location, error) {
	if name := cmp.Or(flagValue, t.getenv("CALENDAR_TZ")); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil || name == "Local" {
			return nil, fmt.Errorf("unknown timezone %q: use an IANA name such as Europe/Berlin", name)
		}
		return loc, nil
	}
	if name := systemZoneName(t); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, nil
		}
	}
	return nil, errors.New("could not tell your timezone: give it with --timezone Europe/Berlin, or set CALENDAR_TZ=Europe/Berlin")
}

// systemZoneName is the system timezone's IANA name: TZ, else the name in
// the zoneinfo path /etc/localtime links to, as on macOS and Linux.
func systemZoneName(t toolIO) string {
	if tz := strings.TrimPrefix(t.getenv("TZ"), ":"); tz != "" && !filepath.IsAbs(tz) {
		return tz
	}
	target, err := filepath.EvalSymlinks(zoneinfoLink)
	if err != nil {
		return ""
	}
	if _, name, ok := strings.Cut(filepath.ToSlash(target), "/zoneinfo/"); ok {
		return name
	}
	return ""
}
