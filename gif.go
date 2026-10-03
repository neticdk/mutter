package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	modeGIF = "gif"
	// maxGIFResults is how many GIFs the picker shows in one row.
	maxGIFResults = 6
	gifThumbRows  = 6
)

// giphySearch is GIPHY's search endpoint. Tests point it at a fake.
var giphySearch = "https://api.giphy.com/v1/gifs/search"

// gifResult is one GIF: a small rendition to preview and a larger one to
// send.
type gifResult struct {
	id, title    string
	thumb, large string
}

type gifResultsMsg struct {
	query   string
	results []gifResult
}

// giphyResponse is the part of a GIPHY search response mutter reads.
type giphyResponse struct {
	Data []giphyGIF `json:"data"`
}

type giphyGIF struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Images giphyImages `json:"images"`
}

// giphyImages are the renditions mutter picks from. Small ones preview,
// downsized ones, under 2 MB, get sent.
type giphyImages struct {
	FixedHeightSmall giphyRendition `json:"fixed_height_small"`
	FixedHeight      giphyRendition `json:"fixed_height"`
	Downsized        giphyRendition `json:"downsized"`
	Original         giphyRendition `json:"original"`
}

type giphyRendition struct {
	URL string `json:"url"`
}

// searchGIFs asks GIPHY for GIFs matching q.
func searchGIFs(ctx context.Context, key, q string) ([]gifResult, error) {
	v := url.Values{"api_key": {key}, "q": {q}, "limit": {fmt.Sprint(maxGIFResults)}, "rating": {"pg"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, giphySearch+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, keyless(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("giphy search: %s", resp.Status)
	}
	var body giphyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("giphy search: %w", err)
	}
	var out []gifResult
	for _, d := range body.Data {
		r := gifResult{
			id:    d.ID,
			title: d.Title,
			thumb: firstHTTPS(d.Images.FixedHeightSmall.URL, d.Images.FixedHeight.URL),
			large: firstHTTPS(d.Images.Downsized.URL, d.Images.Original.URL),
		}
		if r.thumb != "" && r.large != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// keyless drops the request URL from a client error, since it carries the
// API key and errors reach the status line and the log.
func keyless(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("giphy: %w", ue.Err)
	}
	return err
}

// firstHTTPS returns the first https URL among urls. Results come from a
// third party, so nothing else is fetched.
func firstHTTPS(urls ...string) string {
	for _, u := range urls {
		if p, err := url.Parse(u); err == nil && p.Scheme == "https" && p.Host != "" {
			return u
		}
	}
	return ""
}

// gifCmd runs /gif QUERY.
func (m *model) gifCmd(q string) tea.Cmd {
	q = strings.TrimSpace(q)
	switch {
	case m.giphyKey == "":
		m.notice = "/gif needs a GIPHY API key in GIPHY_API_KEY"
		return nil
	case q == "":
		m.notice = "usage: /gif QUERY"
		return nil
	}
	m.notice = "searching GIPHY…"
	ctx, key := m.ctx, m.giphyKey
	return func() tea.Msg {
		r, err := searchGIFs(ctx, key, q)
		if err != nil {
			return errMsg(err)
		}
		return gifResultsMsg{q, r}
	}
}

// showGIFs opens the picker on search results and loads their previews.
func (m *model) showGIFs(msg gifResultsMsg) tea.Cmd {
	if len(msg.results) == 0 {
		m.notice = "no GIFs for " + msg.query
		return nil
	}
	m.notice = ""
	m.mode, m.gifs, m.gifIdx = modeGIF, msg.results, 0
	lay := m.gifLayout()
	cmds := make([]tea.Cmd, 0, len(msg.results))
	for _, r := range msg.results {
		cmds = append(cmds, m.imgs.fetchURL(m.ctx, r.thumb, r.thumb, lay))
	}
	return tea.Batch(cmds...)
}

// gifLayout fits one row of previews across the window.
func (m *model) gifLayout() layout {
	return layout{cellW: m.imgs.layout.cellW, cellH: m.imgs.layout.cellH, maxCols: max(8, m.width/maxGIFResults-2), maxRows: gifThumbRows}
}

// updateGIF handles the picker: arrows and tab move, enter attaches the GIF
// to the next message, anything else closes it.
func (m model) updateGIF(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch {
	case k == "right" || tabStep(k) == 1:
		m.gifIdx = (m.gifIdx + 1) % len(m.gifs)
		return m, nil
	case k == "left" || tabStep(k) == -1:
		m.gifIdx = (m.gifIdx + len(m.gifs) - 1) % len(m.gifs)
		return m, nil
	}
	g := m.gifs[m.gifIdx]
	m.mode, m.gifs = "", nil
	m.render() // previews leave the screen
	if k != keyEnter {
		return m, nil
	}
	m.notice = "downloading the GIF…"
	ctx := m.ctx
	return m, func() tea.Msg {
		data, err := fetchGIF(ctx, g.large)
		if err != nil {
			return errMsg(err)
		}
		return pastedMsg{name: "giphy-" + safeName(g.id) + ".gif", data: data}
	}
}

func fetchGIF(ctx context.Context, u string) ([]byte, error) {
	resp, err := httpGet(ctx, u)() //nolint:bodyclose // closed below
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download GIF: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, errors.New("the GIF is too big to send")
	}
	return data, nil
}

// gifView is the picker: previews in a row with their numbers, the
// selected one highlighted, and GIPHY's attribution, which its terms
// require.
func (m *model) gifView(height int) string {
	w := m.gifLayout().maxCols
	cells := make([]string, 0, len(m.gifs))
	for i, g := range m.gifs {
		label := fmt.Sprintf("%d", i+1)
		if i == m.gifIdx {
			label = selStyle.Render("▶ " + label)
		}
		img := m.imgs.render(g.thumb)
		if img == "" {
			img = dimStyle.Render(truncate(clean(g.title), w))
		}
		cells = append(cells, lipgloss.NewStyle().Width(w+2).Render(img+"\n"+label))
	}
	title := clean(m.gifs[m.gifIdx].title)
	body := lipgloss.JoinHorizontal(lipgloss.Top, cells...) + "\n\n" + truncate(title, m.width) + "\n\n" + dimStyle.Render("Powered by GIPHY")
	return lipgloss.NewStyle().Width(m.width).Height(height).MaxHeight(height).Render(body)
}
