package toolcli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"strconv"
)

const pollsUsage = `Usage: fridolin polls [--id MSG_ID | --recent N] [--chat JID]

The results of the polls this brain sent: the 10 newest, the --recent N
newest, or the one poll --id names. --chat keeps to one of the brain's
chats. Prints JSON: {"polls": [...], "count"}, or the one poll with --id.
Each poll has question, options, total_voters (people with a current vote)
and per option its votes and voters.

Flags:
`

// pollsCmd is `fridolin polls`.
func Polls(args []string, t IO) int {
	flags := flag.NewFlagSet("fridolin polls", flag.ContinueOnError)
	id := flags.String("id", "", "one poll, by the message ID it was sent as")
	recent := flags.Int("recent", 10, "how many of the newest polls")
	chat := flags.String("chat", "", "only polls in this chat JID")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), pollsUsage)
		flags.PrintDefaults()
	}
	positional, err := t.parseTool(flags, args)
	if err != nil {
		return t.exitFor("fridolin polls", err)
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case len(positional) > 0:
		return t.fail("fridolin polls: unexpected argument %q", positional[0])
	case *id != "" && (set["recent"] || set["chat"]):
		// Both would be ignored with --id; say so rather than answer a
		// question that was not asked.
		return t.fail("fridolin polls: --id shows one poll; it does not combine with --recent or --chat")
	case *recent < 1 || *recent > 100:
		return t.fail("fridolin polls: --recent must be from 1 to 100, got %d", *recent)
	}
	c, err := BrainClientFor(t)
	if err != nil {
		return t.fail("%v", err)
	}
	q := url.Values{}
	if *id != "" {
		q.Set("id", *id)
	} else {
		q.Set("recent", strconv.Itoa(*recent))
		if *chat != "" {
			q.Set("chat", *chat)
		}
	}
	var out any
	if err := c.call(context.Background(), "GET", "/brain/polls?"+q.Encode(), nil, &out); err != nil {
		return t.fail("%v", err)
	}
	return t.emit(out)
}
