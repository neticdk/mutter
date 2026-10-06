package main

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// helpKeys and helpCommands are the overlay's two columns. The README has
// the long form.
var (
	helpKeys = [][2]string{
		{"enter", "send, or open the selected thread"},
		{"shift+enter, alt+enter", "newline"},
		{"↑ ↓", "select a thread or message"},
		{"pgup pgdown", "scroll"},
		{"esc", "cancel, end selection, leave thread"},
		{"tab shift+tab", "complete @mention, /dm or :emoji:"},
		{keySwitch, "switch space"},
		{keyNext, "next unread thread or space"},
		{"ctrl+b", "toggle the sidebar"},
		{"alt+1…9 ctrl+1…9", "open a sidebar entry"},
		{"ctrl+e", "edit the draft in $EDITOR"},
		{"ctrl+v", "paste an image"},
		{"ctrl+x", "drop the last pending image"},
		{"f1", "this help, also /help"},
		{"ctrl+c", "quit"},
		{"", ""},
		{"selected message", ""},
		{"r", "react, again removes it"},
		{"e d", "edit, delete your own"},
		{"q", "quote"},
		{"y", "copy text"},
		{"l", "open a link"},
		{"c", "copy its link"},
		{"b", "open in the browser"},
		{"v", "view its image"},
		{"u", "unread from here"},
		{"o s", "open, save files"},
	}
	helpCommands = [][2]string{
		{"/find QUERY", "search all spaces"},
		{"/mentions", "messages that mention you"},
		{"/dm WHO", "open or start a DM"},
		{"/new NAME", "create a space"},
		{"/rename NAME", "rename the space"},
		{"/invite WHO", "add someone to the space"},
		{"/leave", "leave the space"},
		{"/mute /unmute", "silence the space"},
		{"/attach PATH [text]", "upload a file"},
		{"/gif QUERY", "search GIPHY, needs GIPHY_API_KEY"},
		{"/open [n]", "open a file"},
		{"/save [n]", "save a file to ~/Downloads"},
		{"/read [all]", "mark the space, or all, read"},
		{"/unread", "mark unread from the thread"},
		{"/dnd [DURATION]", "Do Not Disturb"},
		{cmdAway, "show as away"},
		{"/active [DURATION]", "show as active"},
		{"/status [EMOJI] [TEXT]", "set or clear your status"},
		{"/web", "open in the browser"},
		{"/help", "this help"},
		{"/logout", "delete token and cache, quit"},
		{"/quit", "quit"},
	}
)

func helpColumn(title string, rows [][2]string) string {
	w := 0
	for _, r := range rows {
		w = max(w, lipgloss.Width(r[0]))
	}
	lines := []string{boldStyle.Render(title), ""}
	for _, r := range rows {
		switch {
		case r[0] == "":
			lines = append(lines, "")
		case r[1] == "":
			lines = append(lines, boldStyle.Render(r[0]))
		default:
			lines = append(lines, r[0]+strings.Repeat(" ", w-lipgloss.Width(r[0])+2)+dimStyle.Render(r[1]))
		}
	}
	return strings.Join(lines, "\n")
}

// helpView lays out the overlay in width×height cells: keys and commands
// side by side when they fit, stacked otherwise, cut to the height.
func helpView(width, height int) string {
	keys, cmds := helpColumn("Keys", helpKeys), helpColumn("Commands", helpCommands)
	body := lipgloss.JoinHorizontal(lipgloss.Top, keys, "    ", cmds)
	if lipgloss.Width(body) > width {
		body = keys + "\n\n" + cmds
	}
	lines := strings.Split(body+"\n\n"+dimStyle.Render("any key closes"), "\n")
	if len(lines) > height {
		lines = append(lines[:max(0, height-1)], dimStyle.Render("… more in the README · any key closes"))
	}
	return strings.Join(lines, "\n")
}
