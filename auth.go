package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/chat/v1"
	"google.golang.org/api/pubsub/v1"
)

const keyringService = "mutter"

var scopes = []string{
	"openid",
	"email",
	chat.ChatSpacesReadonlyScope,
	chat.ChatMessagesScope,
	chat.ChatMembershipsReadonlyScope,
	chat.ChatUsersReadstateScope,
	chat.ChatUsersSpacesettingsScope,
	chat.ChatUsersSectionsReadonlyScope,
	chat.ChatUsersAvailabilityScope,
	chat.ChatSpacesCreateScope,
	chat.ChatCustomemojisReadonlyScope,
	pubsub.PubsubScope,
}

type config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Topic        string `json:"topic"` // projects/P/topics/T for live events
}

func (c config) oauth() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       scopes,
	}
}

// tokenSource returns a token source that persists refreshed tokens to the
// OS keychain. It runs the browser login flow when no usable token is stored.
func tokenSource(ctx context.Context, cfg *oauth2.Config) (oauth2.TokenSource, error) {
	tok, err := loadToken()
	if err != nil {
		if tok, err = login(ctx, cfg); err != nil {
			return nil, err
		}
		if err := saveToken(tok); err != nil {
			return nil, err
		}
	}
	return &savingSource{src: cfg.TokenSource(ctx, tok), last: tok.AccessToken}, nil
}

type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if err := saveToken(tok); err != nil {
			return nil, err
		}
	}
	return tok, nil
}

// storedToken records the scopes a token was granted with, so adding a scope
// triggers a fresh login instead of permission errors.
type storedToken struct {
	Token  *oauth2.Token `json:"token"`
	Scopes []string      `json:"scopes"`
}

func loadToken() (*oauth2.Token, error) {
	s, err := keyring.Get(keyringService, "token")
	if err != nil {
		return nil, err
	}
	var st storedToken
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return nil, err
	}
	switch {
	case st.Token == nil || st.Token.RefreshToken == "":
		return nil, errors.New("stored token has no refresh token")
	case !slices.Equal(st.Scopes, scopes):
		return nil, errors.New("stored token has different scopes")
	}
	return st.Token, nil
}

func saveToken(tok *oauth2.Token) error {
	b, err := json.Marshal(storedToken{Token: tok, Scopes: scopes})
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, "token", string(b))
}

func logout() error {
	return keyring.Delete(keyringService, "token")
}

// login runs the installed-app loopback flow with PKCE.
func login(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	c := *cfg
	c.RedirectURL = fmt.Sprintf("http://%s/", ln.Addr())
	state := randHex()
	verifier := oauth2.GenerateVerifier()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Only the redirect carries our state. Browsers also ask for
		// /favicon.ico, and any local process can reach the port.
		if r.URL.Path != "/" || q.Get("state") != state {
			http.NotFound(w, r)
			return
		}
		res := result{code: q.Get("code")}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			res = result{err: fmt.Errorf("oauth: %s", e)}
		} else {
			fmt.Fprintln(w, "mutter is logged in. You can close this tab.")
		}
		// A repeated redirect finds the channel full and is dropped.
		select {
		case ch <- res:
		default:
		}
	})}
	// Serve returns ErrServerClosed once the login finishes.
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	url := c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	fmt.Printf("Opening browser for login. If it does not open, visit:\n%s\n", url)
	openBrowser(url)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return c.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	}
}

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == darwin {
		cmd = "open"
	}
	// #nosec G204 -- the command is fixed. Callers pass our own login URL,
	// an http(s) link checked by openURL, or a file mutter downloaded.
	_ = exec.Command(cmd, url).Start()
}

func randHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
