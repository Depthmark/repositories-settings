package server

import (
	"regexp"
	"strings"
)

// Command is one of the verbs the bot understands when mentioned in a
// PR comment. Empty string means "no command found in this comment".
type Command string

const (
	CmdNone    Command = ""
	CmdRecheck Command = "recheck" // re-run dry-run, refresh the diff comment
	CmdDiff    Command = "diff"    // alias of recheck
	CmdApply   Command = "apply"   // apply the configuration now (privileged)
	CmdHelp    Command = "help"    // post the command list
)

// privilegedCommands require write access. Comparisons are against the
// payload's `author_association`. GitHub guarantees these strings.
var privilegedAssociations = map[string]bool{
	"OWNER":       true,
	"MEMBER":      true,
	"COLLABORATOR": true,
}

// ParsedCommand is the result of scanning a comment body. Slug is the
// lowercase mention that triggered the parse (without the leading @);
// it's empty for slash-command form.
type ParsedCommand struct {
	Command Command
	Slug    string
}

// IsPrivileged reports whether this command needs write access.
func (c Command) IsPrivileged() bool { return c == CmdApply }

// ParseComment scans body for either an @-mention of botSlug followed
// by a verb, or a slash command "/<botSlug> <verb>" or just "/<verb>"
// when botSlug is empty.
//
// Recognized forms (case-insensitive):
//
//	@<botSlug> recheck
//	/<botSlug> recheck
//	/repo-settings recheck
//
// Anything before the trigger is ignored, so quoting prior comments
// doesn't accidentally re-fire a command. The first matching command
// wins; a comment that mentions the bot without a known verb returns
// CmdNone.
func ParseComment(body, botSlug string) ParsedCommand {
	if body == "" {
		return ParsedCommand{}
	}
	slug := strings.ToLower(strings.TrimSpace(botSlug))
	// Patterns are restricted to start-of-line (after optional whitespace)
	// to avoid firing on quoted markdown like "> @bot apply".
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ">") {
			continue
		}
		if cmd, found := matchTrigger(trimmed, slug); found {
			return cmd
		}
	}
	return ParsedCommand{}
}

var verbRE = regexp.MustCompile(`(?i)^(recheck|diff|apply|help)\b`)

// matchTrigger returns the command word that follows an @-mention or
// slash invocation of slug. Tolerates a colon and one or more spaces
// between the trigger and the verb ("@bot: apply").
func matchTrigger(line, slug string) (ParsedCommand, bool) {
	low := strings.ToLower(line)
	rest := ""
	switch {
	case slug != "" && strings.HasPrefix(low, "@"+slug):
		rest = line[len("@"+slug):]
	case slug != "" && strings.HasPrefix(low, "/"+slug):
		rest = line[len("/"+slug):]
	case strings.HasPrefix(low, "/repo-settings"):
		rest = line[len("/repo-settings"):]
	default:
		return ParsedCommand{}, false
	}
	rest = strings.TrimLeft(rest, ": \t")
	m := verbRE.FindString(rest)
	if m == "" {
		// Triggered but no verb — caller may want to reply with help.
		return ParsedCommand{Slug: slug, Command: CmdHelp}, true
	}
	return ParsedCommand{Slug: slug, Command: normalizeVerb(m)}, true
}

func normalizeVerb(s string) Command {
	switch strings.ToLower(s) {
	case "recheck", "diff":
		return CmdRecheck // diff is an alias
	case "apply":
		return CmdApply
	case "help":
		return CmdHelp
	}
	return CmdNone
}

// IsPrivilegedAuthor returns true when the author_association string
// from a webhook payload represents a user with write access.
func IsPrivilegedAuthor(association string) bool {
	return privilegedAssociations[strings.ToUpper(association)]
}

// HelpMarkdown is what the bot replies when asked for help or when an
// unrecognized verb follows a valid trigger.
func HelpMarkdown(slug string) string {
	if slug == "" {
		slug = "repo-settings"
	}
	return "**" + slug + "** commands:\n\n" +
		"- `@" + slug + " recheck` (alias `diff`) — re-run the dry-run and refresh the diff comment.\n" +
		"- `@" + slug + " apply` — apply the configuration now. Requires write access.\n" +
		"- `@" + slug + " help` — show this message.\n"
}
